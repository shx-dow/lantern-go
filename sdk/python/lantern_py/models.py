"""Data models mirroring api/openapi.yaml schemas."""

from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional


@dataclass
class Record:
    """One transfer. ID always equals the share code."""

    id: str
    kind: str
    code: str
    state: str
    file_name: str = ""
    file_size: int = 0
    bytes: int = 0
    total: int = 0
    error: str = ""
    peer_id: str = ""
    started_at: str = ""
    updated_at: str = ""
    expires_at: Optional[str] = None

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "Record":
        return cls(
            id=d.get("id", ""),
            kind=d.get("kind", ""),
            code=d.get("code", ""),
            state=d.get("state", ""),
            file_name=d.get("file_name", "") or "",
            file_size=d.get("file_size", 0) or 0,
            bytes=d.get("bytes", 0) or 0,
            total=d.get("total", 0) or 0,
            error=d.get("error", "") or "",
            peer_id=d.get("peer_id", "") or "",
            started_at=d.get("started_at", "") or "",
            updated_at=d.get("updated_at", "") or "",
            expires_at=d.get("expires_at"),
        )

    @property
    def terminal(self) -> bool:
        return self.state in ("done", "failed", "canceled")

    @property
    def ok(self) -> bool:
        return self.state == "done"


@dataclass
class Event:
    """One SSE event from GET /v1/events."""

    type: str
    id: str
    file_name: str = ""
    bytes: int = 0
    total: int = 0
    error: str = ""

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "Event":
        return cls(
            type=d.get("type", ""),
            id=d.get("id", ""),
            file_name=d.get("file_name", ""),
            bytes=d.get("bytes", 0) or 0,
            total=d.get("total", 0) or 0,
            error=d.get("error", ""),
        )

    @property
    def terminal(self) -> bool:
        return self.type in ("done", "error", "cancelled")


@dataclass
class Status:
    peer_id: str = ""
    addrs: List[str] = field(default_factory=list)
    lan_only: bool = False

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "Status":
        return cls(
            peer_id=d.get("peer_id", ""),
            addrs=list(d.get("addrs", []) or []),
            lan_only=bool(d.get("lan_only", False)),
        )


@dataclass
class PeerInfo:
    id: str = ""
    addrs: List[str] = field(default_factory=list)
    connected: bool = False

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "PeerInfo":
        return cls(
            id=d.get("id", ""),
            addrs=list(d.get("addrs", []) or []),
            connected=bool(d.get("connected", False)),
        )
