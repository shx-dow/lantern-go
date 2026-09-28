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
- **Share-code transfer** (the original path). Still the only way to move a
  file to a device *you* are not currently talking to, and the only way to
  hand a file to an unpaired peer. Directories share as `<name>.zip`.

Reads are one-directional: a paired device can read from another, and
nothing can write to a remote device's filesystem. Writes are not implemented.

Known gaps: connections are not held open between calls, so the first
request after a restart re-discovers the peer; `fetch` is a share-code
transfer rather than being unified onto the read path; sync is unimplemented.
The desktop shell is frozen; headless is the default.

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

The share-code path, for unpaired peers and for moving files you are not
reading:

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
<peer-id>` revokes access immediately. There are no access tiers yet
(supervised / auto / full); every paired device currently has full read
access to every shared root, so declare shared dirs narrowly.

Reads are encrypted and authenticated by the libp2p transport. Unlike the
share-code path there is no separate application-layer key exchange, because
there is no code to derive a key from; a per-pair key is planned.

The share code is a high-entropy transfer secret, not a short human PIN. Do
not paste it into public channels. Requests prove possession of the secret
with a challenge-response exchange before a share is consumed. Encrypted
chunks use AES-GCM with fresh nonces, and completed files are checked against a
full-file SHA-256 hash.

Lantern has not had an independent security audit. Treat it as experimental
software until the protocol, relay configuration, and threat model are stable.
