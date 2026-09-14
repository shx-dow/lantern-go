"""Blocking HTTP client for the lanternd v1 API (stdlib only)."""

import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Callable, Dict, Iterator, List, Optional

from .errors import AuthError, LanternError, NotFoundError, TransferFailed
from .models import Event, FileEntry, PeerInfo, Record, Status, TrustEntry

DEFAULT_URL = "http://127.0.0.1:43782"


def _default_url() -> str:
    return os.environ.get("LANTERND_URL", DEFAULT_URL).rstrip("/")


def _default_token() -> str:
    return os.environ.get("LANTERN_DAEMON_TOKEN", "")


class Client:
    """Talk to one lanternd over localhost HTTP.

    Args:
        base_url: e.g. "http://127.0.0.1:43782" (or $LANTERND_URL).
        token: bearer token (or $LANTERN_DAEMON_TOKEN).
        timeout: seconds for unary requests (streaming uses its own loop).
    """

    def __init__(
        self,
        base_url: Optional[str] = None,
        token: Optional[str] = None,
        timeout: float = 15.0,
    ):
        self.base_url = (base_url or _default_url()).rstrip("/")
        if "://" not in self.base_url:
            self.base_url = "http://" + self.base_url
        self.token = token if token is not None else _default_token()
        self.timeout = timeout

    # -- low level ------------------------------------------------------

    def _request(
        self,
        method: str,
        path: str,
        body: Optional[Dict[str, Any]] = None,
        timeout: Optional[float] = None,
    ) -> Any:
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(
            self.base_url + path, data=data, method=method,
            headers={"Content-Type": "application/json"},
        )
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout if timeout is None else timeout) as resp:
                raw = resp.read().decode()
                return json.loads(raw) if raw.strip() else None
        except urllib.error.HTTPError as e:
            try:
                payload = json.loads(e.read().decode() or "{}")
                msg = payload.get("error", e.reason)
            except (ValueError, OSError):
                msg = str(e.reason)
            if e.code == 401:
                raise AuthError(
                    msg + " (set token= or $LANTERN_DAEMON_TOKEN)", status=401
                ) from None
            if e.code == 404:
                raise NotFoundError(msg, status=404) from None
            raise LanternError(f"{msg} (status {e.code})", status=e.code) from None
        except OSError as e:
            raise LanternError(
                f"cannot reach lanternd at {self.base_url}: {e} (is it running?)"
            ) from None

    # -- shares & fetches -----------------------------------------------

    def share(self, path: str, ttl_seconds: int = 0) -> Record:
        """Advertise a file; returns the Record (ID == share code)."""
        body: Dict[str, Any] = {"path": path}
        if ttl_seconds:
            if ttl_seconds < 0:
                raise ValueError("ttl_seconds must not be negative")
            body["ttl_seconds"] = ttl_seconds
        return Record.from_dict(self._request("POST", "/v1/shares", body))

    def fetch(self, code: str, out_dir: str = ".") -> Record:
        """Start fetching `code` into `out_dir`; returns the Record."""
        return Record.from_dict(
            self._request("POST", "/v1/fetches", {"code": code, "out_dir": out_dir})
        )

    def revoke(self, id: str) -> None:
        """Revoke a share / cancel a transfer (no-op if already terminal)."""
        self._request("DELETE", "/v1/transfers/" + id)

    cancel = revoke  # alias: cancelling and revoking are the same call

    # -- inspection -----------------------------------------------------

    def get(self, id: str) -> Record:
        return Record.from_dict(self._request("GET", "/v1/transfers/" + id))

    def list(self, kind: str = "") -> List[Record]:
        if kind and kind not in ("share", "fetch"):
            raise ValueError('kind must be "share", "fetch", or ""')
        data = self._request("GET", "/v1/transfers" + (f"?kind={kind}" if kind else ""))
        return [Record.from_dict(r) for r in (data or {}).get("transfers", [])]

    def shares(self) -> List[Record]:
        data = self._request("GET", "/v1/shares")
        return [Record.from_dict(r) for r in (data or {}).get("shares", [])]

    def history(self) -> List[Record]:
        data = self._request("GET", "/v1/history")
        return [Record.from_dict(r) for r in (data or {}).get("history", [])]

    def status(self) -> Status:
        return Status.from_dict(self._request("GET", "/v1/status"))

    def peers(self) -> List[PeerInfo]:
        data = self._request("GET", "/v1/peers")
        return [PeerInfo.from_dict(p) for p in (data or {}).get("peers", [])]

    def discover(self) -> Dict[str, Any]:
        """Self status plus connected peers (mirrors `lantern discover`)."""
        st = self.status()
        return {"self": st, "peers": self.peers()}

    def trust_list(self) -> List[TrustEntry]:
        data = self._request("GET", "/v1/trust")
        return [TrustEntry.from_dict(e) for e in (data or {}).get("trusted", [])]

    def trust_add(self, peer_id: str, alias: str = "") -> TrustEntry:
        if not peer_id:
            raise ValueError("peer_id must not be empty")
        return TrustEntry.from_dict(
            self._request("POST", "/v1/trust", {"peer_id": peer_id, "alias": alias})
        )

    def trust_remove(self, peer_id: str) -> None:
        if not peer_id:
            raise ValueError("peer_id must not be empty")
        self._request("DELETE", "/v1/trust/" + peer_id)

    def files(self, dir: str = "") -> List[FileEntry]:
        path = "/v1/files" + (f"?dir={urllib.parse.quote(dir)}" if dir else "")
        data = self._request("GET", path)
        return [FileEntry.from_dict(f) for f in (data or {}).get("files", [])]

    def remote_files(self, peer_id: str, dir: str = "") -> List[FileEntry]:
        if not peer_id:
            raise ValueError("peer_id must not be empty")
        path = "/v1/peers/" + peer_id + "/files" + (f"?dir={urllib.parse.quote(dir)}" if dir else "")
        data = self._request("GET", path)
        return [FileEntry.from_dict(f) for f in (data or {}).get("files", [])]

    # -- waiting ----------------------------------------------------------

    def events(self, timeout: Optional[float] = None) -> Iterator[Event]:
        """Yield SSE events for all transfers (filter by `event.id`).

        Reconnects are left to the caller; see wait() for the robust loop.
        """
        req = urllib.request.Request(
            self.base_url + "/v1/events",
            method="GET",
            headers={"Accept": "text/event-stream"},
        )
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        try:
            resp = urllib.request.urlopen(req, timeout=timeout)
        except urllib.error.HTTPError as e:
            if e.code == 401:
                raise AuthError("missing or invalid daemon token", status=401) from None
            raise LanternError(f"events stream failed (status {e.code})", status=e.code) from None
        except OSError as e:
            raise LanternError(f"cannot reach lanternd at {self.base_url}: {e}") from None
        with resp:
            data_lines: List[str] = []

            def flush() -> Iterator[Event]:
                if data_lines:
                    try:
                        yield Event.from_dict(json.loads("\n".join(data_lines)))
                    except ValueError:
                        pass
                    del data_lines[:]

            while True:
                raw = resp.readline()
                if not raw:
                    yield from flush()  # server closed the stream
                    return
                line = raw.decode("utf-8", "replace").rstrip("\n")
                if line == "":
                    yield from flush()
                    continue
                if line.startswith(":"):
                    continue  # ping comment
                if line.startswith("data:"):
                    data_lines.append(line[5:].strip())

    def wait(
        self,
        id: str,
        timeout: Optional[float] = None,
        on_progress: Optional[Callable[[Event], None]] = None,
        poll_interval: float = 0.5,
    ) -> Record:
        """Block until transfer `id` is done; return the final Record.

        Streams SSE, falling back to polling when the stream drops.
        Raises TransferFailed on failed/canceled, TimeoutError on timeout.
        """
        deadline = None if timeout is None else time.monotonic() + timeout
        rec = self.get(id)
        if rec.terminal:
            return self._terminal_or_raise(rec)
        try:
            for event in self.events(
                timeout=None if deadline is None else max(1.0, deadline - time.monotonic())
            ):
                if event.id and event.id != id:
                    continue
                if event.type == "progress":
                    if on_progress:
                        on_progress(event)
                elif event.terminal:
                    # Re-read: the event may win a race with the record
                    # update. Only settle on a terminal record.
                    try:
                        rec = self.get(id)
                    except LanternError:
                        continue
                    if rec.terminal:
                        return self._terminal_or_raise(rec)
                if deadline is not None and time.monotonic() >= deadline:
                    raise TimeoutError(f"timed out waiting for transfer {id}")
        except LanternError:
            pass  # stream dropped: fall through to polling
        while True:
            if deadline is not None and time.monotonic() >= deadline:
                raise TimeoutError(f"timed out waiting for transfer {id}")
            time.sleep(poll_interval)
            try:
                rec = self.get(id)
            except LanternError:
                continue
            if rec.terminal:
                return self._terminal_or_raise(rec)

    @staticmethod
    def _terminal_or_raise(rec: Record) -> Record:
        if rec.state == "done":
            return rec
        raise TransferFailed(rec)

    # -- convenience ------------------------------------------------------

    def fetch_and_wait(
        self,
        code: str,
        out_dir: str = ".",
        timeout: Optional[float] = None,
        on_progress: Optional[Callable[[Event], None]] = None,
    ) -> Record:
        """Fetch a code and block until it completes; returns the Record."""
        rec = self.fetch(code, out_dir)
        return self.wait(rec.id, timeout=timeout, on_progress=on_progress)

    def share_and_wait(
        self,
        path: str,
        ttl_seconds: int = 0,
        timeout: Optional[float] = None,
        on_progress: Optional[Callable[[Event], None]] = None,
    ) -> Record:
        """Share a file and block until a receiver completes it."""
        rec = self.share(path, ttl_seconds=ttl_seconds)
        return self.wait(rec.id, timeout=timeout, on_progress=on_progress)
