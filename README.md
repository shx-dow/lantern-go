# Lantern

Local-first, agent-native file transfer. Ask your agent to get a file from
another device, or read a file off it, without handling a share code — no
account, no cloud store. Bytes go peer-to-peer over libp2p (mDNS + DHT,
optional relay); agents coordinate through the CLI, daemon HTTP API, MCP shim,
or Python SDK.

## Status

Under active development, pre-alpha.

Two ways to move bytes, and they are not equivalent:

- **Read from a paired device** (`/lantern/fs/1.0.0`, the agent-native path).
  No share code, no staging copy, no transfer session. The reading device
  asks; the serving device answers, scoped to its `--shared-dirs`. This is
  what an agent uses for "get me this from the laptop" and "read this off
  the nas".
- **Share-code transfer** (the original path). Still the only way to hand a
  file to an unpaired peer, and the only way to move a directory. Folders
  share as `<name>.zip`.

Plus **push**: the sending device dials the receiver and writes directly, so
an agent holding a file can place it on another device without the receiver
having to ask. This is the one case the share-code path used to be the only
answer to.

Push is **off by default**. A device refuses every write unless it is started
with `--allow-writes`, and its writable roots bound where content can land.
Pairing a device is never by itself enough to change anything on it.

Known gaps: connections are not held open between calls, so the first
request after a restart re-discovers the peer; push is single-file, and
directories still need the share-code path; `fetch` is a share-code transfer
rather than being unified onto the read path; sync is unimplemented. The
desktop shell is frozen; headless is the default.

## Run it

Pair two devices, then read from one by name. The alias given at pairing time
is what you address:

```sh
# on both devices
go run ./cmd/lanternd --shared-dirs ~/Share --device-name laptop

# pair them, each side naming the other
lantern --daemon discover
lantern --daemon trust add <peer-id> laptop

# then, from the asking device
curl -s -H "Authorization: Bearer $LANTERN_DAEMON_TOKEN" \
  http://127.0.0.1:43782/v1/devices
curl -s -H "Authorization: Bearer $LANTERN_DAEMON_TOKEN" \
  "http://127.0.0.1:43782/v1/peers/laptop/read?path=$HOME/Share/notes.md"
```

`GET /v1/peers/{id}/read` takes an alias or a peer ID for `{id}`. Content
comes back as text when it is clean UTF-8 and base64 otherwise, with
`encoding` saying which. Reads are capped at 8 MiB per call (256 KiB
default); a partial read returns a `warning` naming the offset to resume from.
`GET /v1/peers/{id}/stat` returns size and mtime without the bytes.

Pushing to a paired device that accepts writes:

```sh
# on the receiving device, opt in
go run ./cmd/lanternd --shared-dirs ~/inbox --writable-dirs ~/inbox --allow-writes

# from the sending device
curl -s -X POST -H "Authorization: Bearer $LANTERN_DAEMON_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"to":"nas","path":"./report.txt"}' \
  http://127.0.0.1:43782/v1/pushes
```

A bare `remote_path` is resolved by the receiving device against its first
writable root, so the example above lands in `~/inbox/report.txt`. Existing
files are never replaced unless you pass `"overwrite": true`, and the response
carries both the digest of what was sent and the digest the receiver computed,
so a corrupted copy is reported as a failure rather than a success. Only
paths and metadata cross this API; the bytes move peer-to-peer.

The share-code path, for unpaired peers and for moving directories:

```sh
go run ./cmd/lantern
go run ./cmd/lantern send ./path/to/file-or-dir
go run ./cmd/lantern receive <share-code> [output-directory]
```

The sender prints a 128-bit share code. The receiver needs that code and must
be able to discover or connect to the sender through mDNS, the DHT, or a
configured libp2p route.

Persistent daemon, also used to serve shared dirs for remote listing:

```sh
lantern --daemon files
lantern --daemon remote-files <peer-id>
```

MCP shim (stdio, proxies the daemon API):

```sh
go run ./cmd/lantern-mcp
```

To run a relay locally:

```sh
go run ./cmd/lantern-relay 4001
```

## Development

```sh
gofmt -w .
go test ./...
go test -race ./...
go vet ./...
```

The two-host transfer tests in `internal/p2p` exercise fresh and resumed
multi-chunk transfers. The `internal/p2p` fs tests cover reads, ranged reads,
the read cap, and refusal of path and symlink escapes. Protocol framing,
crypto, storage, and session tests cover their respective interfaces.

`api/openapi.yaml` is the contract for the daemon API. It is not validated by
CI, so treat changes to it as review-worthy.

## Security notes

**A paired device can read files inside that device's `--shared-dirs`.** That
is the whole point of the read path, and it is a standing capability rather
than a one-shot transfer: it has no natural end point, and it lasts until the
pairing is removed. Shared roots are a hard boundary — paths outside them,
including via symlink, are refused — and `lantern --daemon trust remove
<peer-id>` revokes access immediately.

**A paired device can write into a device's `--writable-dirs` only if that
device was started with `--allow-writes`.** Without it, every write is refused
and the reason is reported. When writes are enabled they are still bounded:
destinations must resolve inside a writable root, an existing file is never
replaced without an explicit overwrite, a write is staged to a temp file and
renamed into place so a reader never sees a half-written file, and the sender
compares digests before calling it delivered. There is no per-peer write
policy yet — enabling writes trusts every paired device equally, and there
are no access tiers (supervised / auto / full) on either path.

Writes are the half of this product that most deserves an audit before
anyone points it at a real machine. Reads are encrypted and authenticated by
the libp2p transport. Unlike the share-code path there is no separate
application-layer key exchange, because there is no code to derive a key
from; a per-pair key is planned.

The share code is a high-entropy transfer secret, not a short human PIN. Do
not paste it into public channels. Requests prove possession of the secret
with a challenge-response exchange before a share is consumed. Encrypted
chunks use AES-GCM with fresh nonces, and completed files are checked against a
full-file SHA-256 hash.

Lantern has not had an independent security audit. Treat it as experimental
software until the protocol, relay configuration, and threat model are stable.
