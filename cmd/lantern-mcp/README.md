# lantern-mcp (thin shim over lanternd)

MCP stdio server. Every tool proxies to `lanternd`'s localhost v1 API
(`api/openapi.yaml`) — no transfer logic lives here.

```json
{"mcpServers": {"lantern": {"command": "lantern-mcp"}}}
```

Env: `LANTERND_URL` (default `http://127.0.0.1:43782`),
`LANTERN_DAEMON_TOKEN` (or `LANTERND_TOKEN`).

Tools: `share`, `fetch`, `status`, `discover`, `transfers`, `transfer`,
`history`, `cancel`. `discover` returns `{self, peers}`.
