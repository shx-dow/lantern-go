"""Unit tests for lantern_py against an in-process fake lanternd."""

import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer

import sys
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from lantern_py import Client
from lantern_py.errors import AuthError, LanternError, NotFoundError, TransferFailed

TOKEN = "unit-test-token"

REC = {
    "id": "abc123",
    "kind": "share",
    "code": "abc123",
    "file_name": "f.bin",
    "file_size": 100,
    "bytes": 100,
    "total": 100,
    "state": "done",
    "started_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-01T00:00:01Z",
}

SSE_FRAMES = [
    'event: progress\ndata: {"type":"progress","id":"abc123","file_name":"f.bin","bytes":50,"total":100}\n\n',
    ": ping\n\n",
    'event: done\ndata: {"type":"done","id":"abc123","file_name":"f.bin","bytes":100,"total":100}\n\n',
]


class FakeDaemon(BaseHTTPRequestHandler):
    token_required = True
    mode = "ok"  # ok | terminal-running | flip (running, then done)
    get_count = 0

    def log_message(self, *args):
        pass

    def _auth_ok(self):
        if not self.token_required:
            return True
        return self.headers.get("Authorization") == "Bearer " + TOKEN

    def _send(self, code, obj=None):
        body = json.dumps(obj).encode() if obj is not None else b""
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _guard(self):
        if not self._auth_ok():
            self._send(401, {"error": "missing or invalid daemon token"})
            return False
        return True

    def do_GET(self):
        if not self._guard():
            return
        if self.path == "/v1/events":
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            for frame in SSE_FRAMES:
                self.wfile.write(frame.encode())
                self.wfile.flush()
            return
        if self.path == "/v1/status":
            return self._send(200, {"peer_id": "p1", "addrs": [], "lan_only": True})
        if self.path == "/v1/peers":
            return self._send(200, {"peers": [{"id": "p9", "addrs": [], "connected": True}]})
        if self.path == "/v1/history":
            return self._send(200, {"history": [REC]})
        if self.path == "/v1/trust":
            return self._send(200, {"trusted": [{"peer_id": "p9", "alias": "laptop", "added_at": "2026-01-01T00:00:00Z"}]})
        if self.path == "/v1/files" or self.path.startswith("/v1/files?"):
            return self._send(200, {"files": [{"name": "a.txt", "size": 3, "mod_time": "2026-01-01T00:00:00Z", "is_dir": False}]})
        if self.path.startswith("/v1/peers/") and self.path.endswith("/files"):
            return self._send(200, {"files": [{"name": "r.txt", "size": 5, "mod_time": "2026-01-01T00:00:00Z", "is_dir": False}]})
        if self.path == "/v1/transfers":
            return self._send(200, {"transfers": [REC]})
        if self.path == "/v1/shares":
            return self._send(200, {"shares": [REC]})
        if self.path.startswith("/v1/transfers/"):
            if self.path.endswith("/missing"):
                return self._send(404, {"error": "transfer not found"})
            rec = dict(REC)
            if self.mode == "terminal-running":
                rec = dict(rec, state="running", bytes=10)
            elif self.mode == "flip":
                FakeDaemon.get_count += 1
                if FakeDaemon.get_count <= 2:
                    rec = dict(rec, state="running", bytes=10)
            return self._send(200, rec)
        return self._send(404, {"error": "nope"})

    def do_POST(self):
        if not self._guard():
            return
        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length).decode() or "{}")
        if self.path == "/v1/shares":
            if not body.get("path"):
                return self._send(400, {"error": "path must not be empty"})
            return self._send(201, dict(REC, kind="share"))
        if self.path == "/v1/fetches":
            return self._send(201, dict(REC, kind="fetch"))
        if self.path == "/v1/trust":
            return self._send(201, {"peer_id": body.get("peer_id"), "alias": body.get("alias", ""), "added_at": "2026-01-01T00:00:00Z"})
        return self._send(404, {"error": "nope"})

    def do_DELETE(self):
        if not self._guard():
            return
        if self.path.endswith("/missing"):
            return self._send(404, {"error": "transfer not found"})
        self.send_response(204)
        self.end_headers()


class ClientTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = HTTPServer(("127.0.0.1", 0), FakeDaemon)
        cls.port = cls.server.server_address[1]
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()

    def client(self, **kw):
        kw.setdefault("base_url", f"http://127.0.0.1:{self.port}")
        kw.setdefault("token", TOKEN)
        return Client(**kw)

    def test_share_fetch_get(self):
        c = self.client()
        rec = c.share("/tmp/x.bin", ttl_seconds=60)
        self.assertEqual(rec.id, rec.code)
        self.assertTrue(rec.ok)
        rec = c.fetch("abc123")
        self.assertEqual(rec.kind, "fetch")
        self.assertEqual(c.get("abc123").file_name, "f.bin")

    def test_share_validation(self):
        with self.assertRaises(LanternError):
            self.client().share("")
        with self.assertRaises(ValueError):
            self.client().share("/tmp/x.bin", ttl_seconds=-1)

    def test_lists_status_peers_history(self):
        c = self.client()
        self.assertEqual(len(c.list()), 1)
        self.assertEqual(len(c.shares()), 1)
        self.assertEqual(len(c.history()), 1)
        st = c.status()
        self.assertTrue(st.lan_only and st.peer_id == "p1")
        peers = c.peers()
        self.assertEqual(peers[0].id, "p9")
        with self.assertRaises(ValueError):
            c.list("nope")

    def test_revoke_and_not_found(self):
        c = self.client()
        c.revoke("abc123")  # 204, no error
        with self.assertRaises(NotFoundError):
            c.get("missing")
        with self.assertRaises(NotFoundError):
            c.revoke("missing")

    def test_discover_trust_files(self):
        c = self.client()
        d = c.discover()
        self.assertEqual(d["self"].peer_id, "p1")
        self.assertEqual(d["peers"][0].id, "p9")
        trusted = c.trust_list()
        self.assertEqual(trusted[0].peer_id, "p9")
        added = c.trust_add("p9", alias="laptop")
        self.assertEqual(added.alias, "laptop")
        c.trust_remove("p9")  # 204, no error
        with self.assertRaises(ValueError):
            c.trust_add("")
        self.assertEqual(c.files()[0].name, "a.txt")
        self.assertEqual(c.remote_files("p9")[0].name, "r.txt")
        with self.assertRaises(ValueError):
            c.remote_files("")

    def test_auth(self):
        with self.assertRaises(AuthError):
            self.client(token="wrong").status()
        with self.assertRaises(AuthError):
            self.client(token="").status()

    def test_events_and_wait(self):
        c = self.client()
        seen = [e for e in c.events()]
        self.assertEqual([e.type for e in seen], ["progress", "done"])
        self.assertEqual(seen[0].bytes, 50)
        # Already terminal: fast path returns without streaming.
        progress = []
        rec = c.wait("abc123", on_progress=progress.append)
        self.assertTrue(rec.ok)
        self.assertEqual(progress, [])
        # Streaming path: record flips running -> done mid-wait.
        FakeDaemon.get_count = 0
        FakeDaemon.mode = "flip"
        try:
            progress = []
            rec = c.wait("abc123", timeout=15, poll_interval=0.1,
                         on_progress=progress.append)
            self.assertTrue(rec.ok)
            self.assertGreaterEqual(len(progress), 1)
        finally:
            FakeDaemon.mode = "ok"

    def test_wait_timeout_and_failed(self):
        c = self.client()
        FakeDaemon.mode = "terminal-running"
        try:
            # Record stays running: wait must give up at the deadline.
            with self.assertRaises(TimeoutError):
                c.wait("abc123", timeout=3, poll_interval=0.1)
            # A failed terminal record raises TransferFailed with context.
            failed = dict(REC, state="failed", error="boom")
            from lantern_py.models import Record as RecordModel

            with self.assertRaises(TransferFailed) as ctx:
                Client._terminal_or_raise(RecordModel.from_dict(failed))
            self.assertIn("boom", str(ctx.exception))
            self.assertEqual(ctx.exception.record.id, "abc123")
        finally:
            FakeDaemon.mode = "ok"


if __name__ == "__main__":
    unittest.main()
