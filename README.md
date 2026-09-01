# Lantern

Lantern is a peer-to-peer command-line file transfer tool written in Go. It
uses libp2p for connectivity, local mDNS and DHT discovery, and an optional
relay binary for networks where direct connections are unavailable.

## Status

Lantern is under active development. The transfer path has authenticated
requests, encrypted chunked streams, full-file hashing, and resumable partial
files. The command-line and terminal UI are usable for development, but the
protocol and configuration interfaces may still change.

## Run it

```sh
go run ./cmd/lantern
go run ./cmd/lantern send ./path/to/file
go run ./cmd/lantern receive <share-code> [output-directory]
```

The sender prints a 128-bit share code. The receiver needs that code and must
be able to discover or connect to the sender through mDNS, the DHT, or a
configured libp2p route.

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
