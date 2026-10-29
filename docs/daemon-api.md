# The daemon API

Every request needs the daemon's bearer token. It lives in the data directory
that `lantern daemon` prints on startup:

```sh
export LANTERN_DAEMON_TOKEN="$(cat ~/.local/share/lantern/.lanternd-token)"
export LANTERND_URL="http://127.0.0.1:43782"
```

The full contract is [`api/openapi.yaml`](../api/openapi.yaml). CI parses it
and fails on a duplicate key, an unresolved local `$ref`, an operation with no
`responses` map, or a method and path the daemon's mux does not serve (see
[`api/openapi_test.go`](../api/openapi_test.go)).

Every request body is a small document of paths and flags, capped at 64 KiB. The
cap exists to refuse a mistake, not to be generous.

## List paired devices

```sh
curl -s -H "Authorization: Bearer $LANTERN_DAEMON_TOKEN" \
  http://127.0.0.1:43782/v1/devices
```

Each device has an alias and an `online` flag. `online` is point-in-time: it
reports a live connection, so `false` means not currently connected rather than
down. Asking for something dials on demand, which is how a reachable but idle
device flips to `true`.

`?probe=1` dials every device and adds `reachable` plus a reason for any failure.
`lantern doctor --probe-peers` and the MCP `devices` tool's `probe` argument do
the same thing.

## Read a file

```sh
curl -s -H "Authorization: Bearer $LANTERN_DAEMON_TOKEN" \
  "http://127.0.0.1:43782/v1/peers/laptop/read?path=$HOME/Share/notes.md"
```

`{id}` is an alias or a peer ID. An alias is resolved through the trust store,
so it only works for a paired device. Content comes back as text when it is
clean UTF-8 and base64 otherwise, with `encoding` saying which.

Reads are capped at 8 MiB per call, with 256 KiB as the default. A partial read
returns a `warning` naming the offset to resume from. To get size and mtime
without the bytes, use `GET /v1/peers/{id}/stat`.

## Push a file

The receiving device must be started with `--allow-writes`, and the sending
device must be paired at tier `read-write`:

```sh
# on the receiving device
lantern daemon --shared-dirs ~/inbox --writable-dirs ~/inbox --allow-writes
```

```sh
# from the sending device
curl -s -X POST -H "Authorization: Bearer $LANTERN_DAEMON_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"to":"nas","path":"./report.txt"}' \
  http://127.0.0.1:43782/v1/pushes
```

A bare `remote_path` is resolved by the receiving device against its first
writable root, so the call above lands in `~/inbox/report.txt`.

An existing file is never replaced unless you pass `"overwrite": true`. The
response carries both the digest of what was sent and the digest the receiver
computed, so a corrupted copy is reported as a failure rather than a success.

Only paths and metadata cross this API. The bytes move peer-to-peer.

## MCP

`lantern mcp` is an MCP server over stdio. It proxies the daemon API and holds no
transfer logic of its own.

```sh
lantern mcp
```

Wire it into an MCP client as one command. Use the absolute binary path: a bare
`"lantern"` fails in clients that do not inherit your shell's `$PATH`.

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

Codex uses TOML instead:

```toml
[mcp_servers.lantern]
command = "/home/you/lantern-go/lantern"
args = ["mcp"]

[mcp_servers.lantern.env]
LANTERND_URL = "http://127.0.0.1:43782"
LANTERN_DAEMON_TOKEN = "<the token in your data dir>"
```

Set `LANTERND_URL` to the daemon's real `--addr`. A daemon on `:43792`, as in
the [static peers](static-peers.md) guide, is not the default port.

Restart the client after editing, because MCP servers load at startup. Then ask
it for something real: `devices` first, then a `read`, then a `push`.

The version the agent sees in `serverInfo` is the one `lantern version` prints.