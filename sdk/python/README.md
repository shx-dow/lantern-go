# lantern-py

Thin zero-dependency Python client for **lanternd** (see `api/openapi.yaml`).
File bytes never flow through here — the client trades share codes and
streams progress while the p2p data plane moves content.

```python
from lantern_py import Client

ln = Client()  # http://127.0.0.1:43782 + $LANTERN_DAEMON_TOKEN

rec = ln.share("model.bin", ttl_seconds=600)
print("code:", rec.code)

done = ln.wait(rec.id, on_progress=lambda e: print(e.bytes, "/", e.total))
print("sent:", done.file_name)

got = ln.fetch_and_wait(rec.code, out_dir="./inbox")
print("received:", got.file_name)
```

## Install

```sh
pip install -e ./sdk/python   # from the repo root
```

## Auth

Pass `token=` or set `$LANTERN_DAEMON_TOKEN`. Wrong token raises
`AuthError`; unknown IDs raise `NotFoundError`; failed transfers raise
`TransferFailed` (with `.record`).
