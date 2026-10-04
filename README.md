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

**Connections are made on demand.** A device records where it hears other
devices, but does not connect to them just for announcing themselves.
Connections happen when the agent asks for something, and are otherwise
absent. This keeps a device light on memory and CPU, and means a stranger
running Lantern on the same Wi-Fi is never connected to.

Known gaps: directory push is not implemented, so folders still need the
share-code path; `fetch` is a share-code transfer rather than being unified
onto the read path; sync is unimplemented; there are no access modes. The
desktop shell has been removed, so the tree is pure Go and cross-compiles for
every target we ship.

## Run it

There is one binary. `lantern daemon` runs the background service, `lantern
mcp` is the agent shim, and the rest are commands.

```sh
go build ./cmd/lantern          # or: CGO_ENABLED=0 go build -ldflags "-s -w" -o lantern ./cmd/lantern
```

Check what you built:

```sh
lantern version                 # version, commit, build date, toolchain, platform
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
`true`.

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
