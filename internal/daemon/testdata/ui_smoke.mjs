// Load the embedded UI page in a JavaScript runtime and let it talk to a fake
// daemon. The page is only useful if its script runs: a route test that fetches
// /ui proves the bytes were served, not that the page can reach the API.
//
// Usage: node ui_smoke.mjs <path-to-index.html>
//
// Node is not a build dependency. internal/daemon/ui_smoke_test.go skips this
// when no node binary is on PATH.

import fs from "node:fs";
import http from "node:http";
import vm from "node:vm";

const pagePath = process.argv[2];
if (!pagePath) {
  console.error("usage: node ui_smoke.mjs <path-to-index.html>");
  process.exit(2);
}

const html = fs.readFileSync(pagePath, "utf8");
const scriptMatch = html.match(/<script[^>]*>([\s\S]*?)<\/script>/);
if (!scriptMatch) {
  console.error("the page has no script");
  process.exit(1);
}
const script = scriptMatch[1];

const seen = [];
let statusServed;
const statusReady = new Promise((resolve) => {
  statusServed = resolve;
});

function send(res, body) {
  res.writeHead(200, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

const server = http.createServer((req, res) => {
  seen.push({ path: req.url, auth: req.headers.authorization || "" });
  if (req.url === "/v1/status") {
    send(res, { peer_id: "12D3KooWsmoke0123456789", lan_only: false });
    statusServed();
  } else if (req.url === "/v1/transfers") {
    send(res, { transfers: [] });
  } else if (req.url === "/v1/history") {
    send(res, { history: [] });
  } else if (req.url === "/v1/peers") {
    send(res, { peers: [] });
  } else if (req.url === "/v1/events") {
    // The page streams events forever. Never resolving this keeps the loop
    // parked while the assertions run.
    res.writeHead(200, { "Content-Type": "text/event-stream" });
  } else {
    res.writeHead(404, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ error: "not found" }));
  }
});

await new Promise((resolve, reject) => {
  server.once("error", reject);
  server.listen(0, "127.0.0.1", resolve);
});
const base = `http://127.0.0.1:${server.address().port}`;

const elements = new Map();
function element(id) {
  if (!elements.has(id)) {
    elements.set(id, {
      id,
      style: {},
      classList: { add() {}, remove() {} },
      addEventListener() {},
      textContent: "",
      innerHTML: "",
      value: "",
      files: [],
      disabled: false,
      onclick: null,
      onchange: null,
    });
  }
  return elements.get(id);
}

const stored = new Map([["lantern.token", "smoke-token"]]);
const context = {
  console,
  document: { getElementById: element },
  localStorage: {
    getItem: (key) => (stored.has(key) ? stored.get(key) : null),
    setItem: (key, value) => stored.set(key, String(value)),
  },
  navigator: { clipboard: { writeText: async () => {} } },
  fetch: async (input, init) => {
    const url = typeof input === "string" ? input : input.url;
    if (url === "/v1/events") {
      await new Promise(() => {});
    }
    return fetch(url.startsWith("/") ? base + url : url, init);
  },
  setInterval: () => 1,
  clearInterval: () => {},
  setTimeout: () => 1,
  clearTimeout: () => {},
  FormData,
  FileReader: class {},
};
vm.createContext(context);
vm.runInContext(script, context, { filename: "index.html" });

async function waitFor(check, label) {
  const deadline = Date.now() + 3000;
  while (Date.now() < deadline) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error(`timed out waiting for ${label}`);
}

try {
  await statusReady;
  await waitFor(
    () => (elements.get("status")?.textContent || "").includes("12D3KooWsmok"),
    "the status line to render the peer",
  );
  const status = elements.get("status").textContent;
  if (!status.includes("global") || status.includes("offline")) {
    throw new Error(`status line = ${JSON.stringify(status)}, want an online peer`);
  }

  for (const path of ["/v1/transfers", "/v1/history", "/v1/peers"]) {
    await waitFor(() => seen.some((r) => r.path === path), `a request to ${path}`);
  }
  for (const req of seen) {
    if (!req.path.startsWith("/v1/")) {
      throw new Error(`unexpected request ${req.path}`);
    }
    if (req.path !== "/v1/events" && req.auth !== "Bearer smoke-token") {
      throw new Error(`${req.path} did not carry the stored token: ${req.auth}`);
    }
    if (req.path.includes("bindings") || req.path.includes("guiservice")) {
      throw new Error(`the page reached a removed desktop binding: ${req.path}`);
    }
  }
  console.log("ui smoke: the page loaded and used the HTTP API");
  process.exitCode = 0;
} catch (err) {
  console.error(`ui smoke failed: ${err.message}`);
  process.exitCode = 1;
} finally {
  server.closeAllConnections?.();
  server.close();
}
