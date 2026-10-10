# Lantern

Local-first, agent-native file transfer. Ask your agent to get a file from
another device, or read a file off it, without handling a share code — no
account, no cloud store. Bytes go peer-to-peer over libp2p (mDNS + DHT,
optional relay); agents coordinate through the CLI, daemon HTTP API, MCP shim,
or Python SDK.

## Status

Under active development, pre-alpha.

Three ways to move bytes. They are not equivalent:

- **Read from a paired device** (the read path, the agent-native path). No share
  code, no staging copy, no transfer session. The reading device asks; the
  serving device answers, scoped to its `--shared-dirs`. This is what an agent
  uses for "get me this from the laptop" and "read this off the nas".
- **Share-code transfer** (the original path). Still the only way to hand a
  file to an unpaired peer. A code from a *paired* device is fetched over the
  read path instead, so this path is what remains after the read path takes what
  it can.
- **Push**. The sending device dials the receiver and writes directly, so an
  agent holding a file can place it on another device without the receiver
  having to ask. This is the one case the share-code path used to be the only
  answer to.

Push takes a directory as well as a file. The sender archives it and sends one
write; the receiver verifies the digest, then expands the archive into a
directory it creates itself, inside its writable roots. That keeps every
safety property a file push has — the destination is resolved by the receiver,
an existing tree is not replaced without `overwrite`, nothing is created until
the digest verifies, and the tree is staged and renamed so a failure leaves the
destination untouched. Symbolic links in an archive are refused, and every
entry is checked to resolve inside the destination. Replacing a directory
replaces the whole tree; two pushes never merge.

Push is **off by default**. A device refuses every write unless it is started
with `--allow-writes`, and its writable roots bound where content can land.
Pairing a device is never by itself enough to change anything on it.

**Connections are made on demand.** A device records where it hears other
devices, but does not connect to them just for announcing themselves.
Connections happen when the agent asks for something, and are otherwise
absent. This keeps a device light on memory and CPU, and means a stranger
running Lantern on the same Wi-Fi is never connected to.

The desktop shell has been removed, so the tree is pure Go and cross-compiles
for every target we ship.

## Run it

There is one binary. `lantern daemon` runs the background service, `lantern
mcp` is the agent shim, and the rest are commands.

```sh
go build ./cmd/lantern          # or: CGO_ENABLED=0 go build -ldflags "-s -w" -o lantern ./cmd/lantern
```

Check what you built, and that it works:

```sh
lantern version                 # version, commit, build date, toolchain, platform
lantern doctor                  # diagnose the setup; prints a fix per problem
lantern help daemon             # every daemon flag
```

The daemon keeps its identity, its pairings, and its token in a per-user
directory that survives a reboot — `~/.local/share/lantern` on Linux,
`~/Library/Application Support/lantern` on macOS, `%LOCALAPPDATA%\lantern`
on Windows. It prints where on startup:

```
lantern daemon listening on http://127.0.0.1:43782/ui (lan_only=true)
  identity and pairings: /home/you/.local/share/lantern
  daemon token:          /home/you/.local/share/lantern/.lanternd-token
```

Pair two devices, then read from one by name. The alias given at pairing time
is what you address:

```sh
# on both devices
lantern daemon --shared-dirs ~/Share --device-name laptop

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

`GET /v1/devices` is the starting point: paired devices with aliases and an
`online` flag. `online` is point-in-time — it reports a live connection, so
`false` means not currently connected, not necessarily down. Asking for
something dials on demand, which is how an idle-but-reachable peer flips to
`true`. Add `?probe=1` to dial each device and get `reachable` plus the reason
for any failure; `lantern doctor --probe-peers` and the MCP `devices` tool's
`probe` argument both do this.

When discovery cannot introduce two devices — across a WSL2 NAT, a Docker
bridge, or Wi-Fi that blocks multicast — point each daemon at the other with
`--peer` (full multiaddrs ending in `/p2p/<peer-id>`, comma-separated, or
`peer_addrs` in the config file). The address is kept permanently and dialed
on demand, with a retry every 20s until the other side appears, and a working
address is saved into the trust store so restarts keep working.

Pin `--p2p-port` on both sides: the default is a random port, which makes a
configured `--peer` address go stale on every restart. One direction
configured is enough (identify teaches both sides the return path), but
configure both so either side can start first:

```sh
# on the wsl box (substitute the windows peer ID from its status output,
# and the host's vEthernet address, reachable from inside WSL)
lantern daemon --addr 127.0.0.1:43792 --p2p-port 41001 --shared-dirs ~/Share --device-name wsl --no-lan \
  --peer /ip4/172.27.224.1/tcp/41002/p2p/<windows-peer-id>

# on the windows box, mirrored (substitute the wsl peer ID and WSL IP)
lantern.exe daemon --addr 127.0.0.1:43782 --p2p-port 41002 --shared-dirs $HOME\Share --device-name windows --no-lan `
  --peer /ip4/172.27.236.57/tcp/41001/p2p/<wsl-peer-id>
```

Pushing to a paired device that accepts writes:

```sh
# on the receiving device, opt in
lantern daemon --shared-dirs ~/inbox --writable-dirs ~/inbox --allow-writes

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
lantern send ./path/to/file-or-dir
lantern receive <share-code> [output-directory]
```

The sender prints a 128-bit share code. The receiver needs that code and must
be able to discover or connect to the sender through mDNS, the DHT, or a
configured libp2p route.

**A code is an address, not a second protocol.** When you receive a code from a
device you are paired with, the file is fetched over the read path: the sender
is asked which path the code names, and if that path is inside its shared dirs
the bytes come from `/lantern/fs/1.0.0` with no transfer session. The sender
decides — it is the only party that knows the path, and the only one that can
say whether the pair may read it. Anything else falls back to the transfer
protocol, so codes still work for unpaired peers and for files outside a shared
dir. Both paths verify the file against a hash the sender computed, and both
resume a partial download.

## When something is not working

`lantern doctor` checks a setup and prints the command that fixes each problem.
Every check is read-only. Run it before anything else — the failures it exists
for are silent ones, and they look identical from the outside:

```sh
lantern doctor                    # or: lantern --daemon-url http://127.0.0.1:43792 doctor
lantern doctor --probe-peers      # also dial each paired device
lantern doctor --json             # for agents
```

It reports on the daemon and its token (a 401 and a refused connection are
different problems with different fixes), whether any advertised address is
actually dialable from another machine, whether the libp2p port is pinned,
whether multicast leaves this host, and every paired device's reachability. It
exits non-zero when a check fails, so it works as a CI or script gate.

Two results are deliberately warnings rather than failures: an mDNS check that
hears nothing, and an idle paired device. Neither means the setup is broken —
Lantern dials on demand, and `--peer` or the DHT route around a blocked
multicast path.

Persistent daemon, also used to serve shared dirs for remote listing:

```sh
lantern --daemon files
lantern --daemon remote-files <peer-id>
```

MCP shim for agents, over stdio. It proxies the daemon API and holds no
transfer logic of its own:

```sh
lantern mcp
```

Wire it into an MCP client as one command. Use the absolute binary path —
bare `"lantern"` fails in clients that don't inherit your shell's `$PATH`
(this bit us with Codex). Point `LANTERND_URL` at the local daemon's actual
`--addr` (the WSL daemon in the example above is `:43792`, not the default):

```json
{
  "mcpServers": {
    "lantern": {
      "command": "/home/you/lantern-go/lantern",
      "args": ["mcp"],
      "env": {
        "LANTERND_URL": "http://127.0.0.1:43782",
        "LANTERN_DAEMON_TOKEN": "<the token in your data dir>"
      }
    }
  }
}
```

Codex (`~/.codex/config.toml`) uses TOML instead of JSON:

```toml
[mcp_servers.lantern]
command = "/home/you/lantern-go/lantern"
args = ["mcp"]

[mcp_servers.lantern.env]
LANTERND_URL = "http://127.0.0.1:43792"
LANTERN_DAEMON_TOKEN = "<the token in your data dir>"
```

Restart the client after editing — MCP servers load at startup. Then ask it
something real: `devices` first, then a `read`, then a `push`.

The version the agent sees in `serverInfo` is the same one `lantern version`
prints.

## Development

```sh
gofmt -w .
go mod tidy
go test ./...
go test -race ./...
go vet ./...
```

The two-host transfer tests in `internal/p2p` exercise fresh and resumed
multi-chunk transfers. The `internal/p2p` fs tests cover reads, ranged reads,
the read cap, and refusal of path and symlink escapes; the write tests cover
the push safety properties. Protocol framing, crypto, storage, and session
tests cover their respective interfaces.

`api/openapi.yaml` is the contract for the daemon API. It is not validated by
CI, so treat changes to it as review-worthy.

JSON request bodies are capped at 64 KiB. Every request in the API is a small
document of paths and flags, so this only ever refuses a mistake.

The project's vocabulary — what a device, a pairing, a tier, a shared root, a
share code, a fetch and a push each mean — is in [`CONTEXT.md`](CONTEXT.md).
Prose and code should use those words.

Design decisions that are not obvious from the code, and why they went that way:
[`docs/adr/`](docs/adr/). Start with
[0002](docs/adr/0002-new-pairings-default-to-read.md) if paired peers stopped
being able to push after an upgrade, or
[0003](docs/adr/0003-content-without-a-digest-is-refused.md) if a push **to**
this device stopped working after an upgrade.

Write-path security review: [`docs/write-path-audit.md`](docs/write-path-audit.md)
records an internal adversarial pass over the push path, its two open findings,
and the properties that held. Not an independent audit.

## Security notes

**A paired device can read files inside that device's `--shared-dirs`.** That
is the whole point of the read path, and it is a standing capability rather
than a one-shot transfer: it has no natural end point, and it lasts until the
pairing is removed. Shared roots are a hard boundary — paths outside them,
including via symlink, are refused — and `lantern --daemon trust remove
<peer-id>` revokes access immediately.

**Pairing is not capability.** What a paired device may do is chosen
deliberately, per device, and survives a reboot:

```sh
lantern trust add <peer-id> [alias]              # paired at tier read
lantern trust add <peer-id> nas --tier read-write # and it may write here
lantern trust tier <alias> read                   # revoke its writes
lantern trust tier <alias> none                  # paired, but nothing works
lantern trust roots <alias> ~/inbox               # it may write only there
lantern trust list                               # shows each device's tier
```

Three tiers, ordered: `none` (paired, grants nothing), `read` (reads inside
`--shared-dirs`), and `read-write` (also writes inside `--writable-dirs`). A
newly paired device gets `read`, so pairing a device never hands it the ability
to change this one. A pairing written before tiers existed loads as `read`
rather than silently gaining write access.

`trust roots` may only **narrow** a device's writable set: a root outside
`--writable-dirs` is rejected, so a pairing record cannot name its way past the
operator's bound even if edited by hand. Per-peer policy is enforced in the
libp2p stream handler, not just at the HTTP layer, so a peer cannot bypass it by
dialling directly.

**A paired device can write only if this device was started with
`--allow-writes` *and* the device is at tier `read-write`.** Both gates must
pass. When writes are enabled they are still bounded: destinations must resolve
inside a writable root, an existing file is never replaced without an explicit
overwrite, a write is staged to a temp file and renamed into place so a reader
never sees a half-written file, permissions are inherited from the directory the
file lands in rather than widened, and the receiving device compares the sender's
digest against the bytes that arrived before anything is renamed or expanded
into place. The same check guards the read path and share-code fetches: content
with no digest from the sender is refused rather than placed unverified.

**Upgrading:** devices paired before tiers existed read as `read`. If you relied
on a paired device pushing files here, raise it with
`lantern trust tier <alias> read-write`.

**Upgrading:** pushes **from an older device to this one** fail until the
sender is upgraded. A push from an older build carries no sender digest, and
content that cannot be verified is refused rather than landed; see
[0003](docs/adr/0003-content-without-a-digest-is-refused.md). The refusal names
the required protocol revision, so the error says which side needs upgrading.

Downloads are unaffected by version. Previous builds already sent digests for
read-path fetches and share-code transfers, so those keep working in both
directions.

**Upgrade senders before receivers.** A new sender can still push to an
un-upgraded receiver — the old build ignores the added field — so pushes keep
working while receivers catch up. Going the other way breaks them at once.

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
