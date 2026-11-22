# Working in this repository

Lantern moves bytes between paired devices. Each device runs a daemon; the CLI
and the MCP server are clients to its loopback HTTP API. `CONTEXT.md` defines
the domain terms (Device, Pairing, Connection, Transfer, Read path, Shared
root, Access tier). Use those words in code, docs, and commit messages.

## Layout

- `cmd/lantern` — CLI entry point and dispatch. Keep it thin; behavior belongs
  in a package that a test can call.
- `internal/daemonapp` — daemon flags, startup, shutdown, and wiring.
- `internal/daemon` — HTTP handlers, trust store, read and push orchestration.
- `internal/p2p` — libp2p host, pairing, share codes, transfers, fs streams.
- `internal/protocol`, `internal/crypto`, `internal/storage` — wire messages,
  keys and signatures, on-disk state.
- `internal/lanterntest` — shared network and process fixtures.
- `pkg/lantern` — embeddable session API.
- `pkg/lanternclient` — the single Go client for the daemon API.
- `internal/mcpserver` — MCP stdio server; proxies the daemon, holds no
  transfer logic.
- `api/openapi.yaml` — daemon API contract. `api/openapi_test.go` fails when
  the spec and the mux disagree.

## Commands

Run `lantern help <command>` for the full text. The main paths:

- **First run.** `lantern setup` writes the per-user `lanternd.json` with a
  device name and shared roots; `lantern daemon` reads it. `lantern ui` opens
  the daemon's page at `<daemon-url>/ui`.
- **Pairing.** `lantern pair` mints a one-use invitation; `lantern pair
  <invitation>` redeems it and pairs both directions at `read`. `lantern trust
  add` is the manual fallback when discovery cannot reach the other device.
- **Paired reads.** `lantern get <device>:<path>` copies a file and verifies
  the digest the serving device reports. `lantern files` lists this device's
  shared roots; `lantern remote-files <device> [dir]` lists a peer's.
- **Share codes.** `lantern send` and `lantern receive` are the unpaired path;
  neither needs a pairing.
- **Trust and diagnostics.** `lantern trust list|add|tier|roots|remove`,
  `lantern status`, `lantern discover`, and `lantern doctor`.

## Invariants

- A pairing is not a capability. Access tier and writable roots are checked on
  the serving device at the stream handler, not in the CLI or MCP layer.
- Nothing is placed until the digest of what arrived has been verified. The fs
  protocol is at revision 3: a write carries `FSRequest.SHA256`, a read response
  carries `FSResponse.SHA256` over exactly the bytes it sent, and content
  without a sender digest is refused, not skipped. See ADR 0003 and ADR 0005.
- Peer-supplied paths are confined to shared roots for reads and writable
  roots for writes. Use the existing containment helpers; do not join a path
  by hand.
- The daemon binds loopback and requires a bearer token by default. Do not
  weaken either without an explicit decision.
- No sender-side hard timeout on a transfer. Idle reads have deadlines; a slow
  but moving transfer must keep working.

## Checks

Fast, before every commit:

```sh
gofmt -l .          # must print nothing
go vet ./...
go test ./...
```

Before a pull request:

```sh
go test -race ./...
staticcheck ./...
git diff --check
```

CI runs lint once and the test matrix on Linux, macOS, and Windows. Fuzz
targets run their seeds in normal tests and search nightly.

A fix needs a test that fails before it and passes after; put the observed
failure and the result of each check in the PR description. A behavior change
that is not obvious needs agreement before implementation.

## Change rules

- One concern per PR. Fixes, refactors, dependency bumps, and docs are separate.
- Do not push to `main`. Branch, open a PR, wait for checks.
- Do not merge without the maintainer's approval.
- Decisions that outlive the diff go in `docs/adr/`. Do not turn an ADR into a
  copy of the implementation.
- Do not add a sender-side hard timeout, a hosted relay, or an account system
  without an agreed ADR.
- Keep the docs set small: `README.md`, `CONTEXT.md`, `CONTRIBUTING.md`,
  `SECURITY.md`, `docs/adr/`, `docs/daemon-api.md`, and this file.

## Easy to get wrong

- There is no `lantern read` command. Use `lantern get <device>:<path>`, which
  goes through `GET /v1/peers/{id}/read?path=...` with optional `offset` and
  `length` (8 MiB cap per call, 256 KiB default). Every read response carries
  `FSResponse.SHA256` over exactly the bytes it sent, and a response without a
  digest is refused. See ADR 0005.
- `lantern pair` is the first-run path and grants `read` only. An invitation
  carries a secret and an expiry, is found through the DHT, and is burned by
  the first redemption that reaches the inviter. `trust add` remains the
  fallback when the two devices cannot discover each other.
- `lantern setup` writes `lanternd.json` and keeps the daemon read-only.
  `--allow-writes` and `--writable-dirs` are start-time flags and are
  deliberately not stored in that file.
- `lantern ui` opens `<daemon-url>/ui`. The page is served without auth; every
  `/v1/` request it makes still needs the bearer token from browser storage.
- The bearer token lives at `<data-dir>/.lanternd-token`. Clients read it only
  when the daemon URL is loopback.
- A peer cannot write unless the daemon runs with `--allow-writes` and the
  destination is inside `--writable-dirs`.
- The default daemon URL is `http://127.0.0.1:43782`.
- `lantern send` and `lantern receive` are the unpaired, share-code path.
  Paired devices use the daemon API and the CLI's paired commands.
- `lantern ls` and `lantern devices` are aliases for `lantern files` and
  `lantern trust list`.
