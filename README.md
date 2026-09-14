# Lantern

Local-first, agent-native file transfer. Ask your agent to find, fetch, or
send files across your trusted devices — no account, no cloud store. Bytes
go peer-to-peer over libp2p (mDNS + DHT, optional relay); agents coordinate
through the CLI, daemon HTTP API, MCP shim, or Python SDK.

## Status

Under active development. The transfer path has authenticated requests,
encrypted chunked streams, full-file hashing, and resumable partial files.
Peer IDs are stable across restarts, devices pair via a trust store, and
shared dirs support local + remote (paired-only) listing. Directories share
as `<name>.zip`. The desktop shell is frozen; headless is the default.

## Run it

```sh
go run ./cmd/lantern
go run ./cmd/lantern send ./path/to/file-or-dir
go run ./cmd/lantern receive <share-code> [output-directory]
```

The sender prints a 128-bit share code. The receiver needs that code and must
be able to discover or connect to the sender through mDNS, the DHT, or a
configured libp2p route.

Persistent daemon (pairs devices, serves shared dirs, powers CLI/MCP/SDK):

```sh
go run ./cmd/lanternd --shared-dirs ~/Share --device-name laptop
lantern --daemon discover
lantern --daemon trust add <peer-id> laptop
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
multi-chunk transfers. Protocol framing, crypto, storage, and session tests
cover their respective interfaces.

## Security notes

The share code is a high-entropy transfer secret, not a short human PIN. Do
not paste it into public channels. Requests prove possession of the secret
with a challenge-response exchange before a share is consumed. Encrypted
chunks use AES-GCM with fresh nonces, and completed files are checked against a
full-file SHA-256 hash.

Lantern has not had an independent security audit. Treat it as experimental
software until the protocol, relay configuration, and threat model are stable.
