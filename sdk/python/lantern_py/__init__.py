"""Thin client for lanternd, the Lantern transfer daemon.

Zero dependencies (stdlib only). File bytes never flow through here;
the client exchanges share codes and polls/streams progress while the
p2p data plane moves the actual content.

    from lantern_py import Client

    ln = Client()  # http://127.0.0.1:43782 + $LANTERN_DAEMON_TOKEN
    rec = ln.share("/home/ava/photo.jpg", ttl_seconds=600)
    print(rec.code)
    final = ln.wait(rec.id)  # blocks until done/failed/canceled
"""

from .client import Client
from .models import Event, PeerInfo, Record, Status

__all__ = ["Client", "Event", "PeerInfo", "Record", "Status"]
