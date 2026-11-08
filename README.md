# Lantern

Move a file between your own devices. Ask your agent for it by name. No account,
no cloud store, and the agent never handles a share code.

Bytes go peer to peer over libp2p. Agents drive it through the CLI, the daemon
HTTP API, or an MCP server.

> **Pre-alpha.** Alpha builds only, with no supported versions and no
> compatibility promise. The write path has had no independent audit. Report a
> suspected vulnerability through [private advisory](SECURITY.md), not a public
> issue.

## The three ways bytes move

- **Read from a paired device.** The agent asks, the serving device answers
  within its shared roots. No share code and no transfer. This is the path an
  agent uses.
- **Share code.** The original path, and the only way to hand a file to an
  unpaired peer. A code from a paired device resolves onto the read path instead.
- **Push.** The sending device dials the receiver and writes, so an agent can
  place a file without the receiver asking. Takes a whole directory as one write.

Connections are made on demand and held only while something is in flight. A
device that only announces itself opens nothing.

## Build

```sh
go build ./cmd/lantern
```

```sh
lantern version      # version, commit, build date, toolchain, platform
lantern doctor       # check the setup, and print a fix per problem
lantern help daemon  # every daemon flag
```

The daemon keeps its identity, pairings, and token in a per-user directory that
survives a reboot. It prints the path on startup.

## Install

Releases are alpha builds. There is no installer, package-manager entry, SDK, or
UI artifact. A release publishes six archives and one checksum file.

Download the archive for your platform from the
[releases page](https://github.com/shx-dow/lantern-go/releases). Each archive
name carries the release tag, shown here as `<version>`:

| platform | archive |
| --- | --- |
| Linux amd64 | `lantern_<version>_linux-amd64.tar.gz` |
| Linux arm64 | `lantern_<version>_linux-arm64.tar.gz` |
| macOS amd64 | `lantern_<version>_darwin-amd64.tar.gz` |
| macOS arm64 | `lantern_<version>_darwin-arm64.tar.gz` |
| Windows amd64 | `lantern_<version>_windows-amd64.zip` |
| Windows arm64 | `lantern_<version>_windows-arm64.zip` |

`SHA256SUMS` carries one line per archive. Download it beside the archive you
want, then check that one line and unpack. Set `VERSION` to the tag you
downloaded, for example `v0.1.0-alpha.1`.

Linux amd64:

```sh
VERSION=v0.1.0-alpha.1
grep 'linux-amd64\.tar\.gz' SHA256SUMS | sha256sum -c -
tar -xzf "lantern_${VERSION}_linux-amd64.tar.gz"
cd linux-amd64
./lantern version
```

macOS arm64:

```sh
VERSION=v0.1.0-alpha.1
grep 'darwin-arm64\.tar\.gz' SHA256SUMS | shasum -a 256 -c -
tar -xzf "lantern_${VERSION}_darwin-arm64.tar.gz"
cd darwin-arm64
./lantern version
```

Windows amd64, in PowerShell:

```powershell
$version = "v0.1.0-alpha.1"
Get-FileHash ".\lantern_${version}_windows-amd64.zip" -Algorithm SHA256
# Compare the hash with the matching line in SHA256SUMS.
Expand-Archive ".\lantern_${version}_windows-amd64.zip" .
.\windows-amd64\lantern.exe version
```

An archive contains the binary and the documentation it links to: `README.md`,
`LICENSE`, `SECURITY.md`, `CONTEXT.md`, and `docs/`. `lantern version` prints the
release version, commit, and build date.

## First run

The binaries are unsigned. macOS and Windows warn the first time one runs. The
warning is about the missing signature, not a damaged file. The checksum above
confirms the archive arrived intact.

macOS, after unpacking:

```sh
# from the directory containing the archive
cd darwin-arm64
xattr -dr com.apple.quarantine .
./lantern version
```

Use `darwin-amd64` on Intel. If macOS still refuses, open System Settings,
Privacy & Security, and allow `lantern` there.

Windows, in PowerShell:

```powershell
Unblock-File .\windows-amd64\lantern.exe
.\windows-amd64\lantern.exe version
```

If SmartScreen appears, choose More info, then Run anyway. Linux needs no extra
step.

Start the daemon in its own terminal and leave it running:

```sh
lantern daemon --shared-dirs ~/Share --device-name laptop
```

Then run `lantern doctor` in another terminal. The CLI and the MCP server talk
to the daemon over the local API. Closing the daemon's terminal stops it, and
commands fail until it is started again.

## Pair, then read

```sh
# on both devices
lantern daemon --shared-dirs ~/Share --device-name laptop

# pair them, each side naming the other
lantern discover
lantern trust add <peer-id> laptop

# from the asking device
lantern remote-files laptop
```

The alias given at pairing time is the name you address. See
[the daemon API](docs/daemon-api.md) for reading a file by path and for
pushing.

Unpaired peers and whole directories use the share code:

```sh
lantern send ./path/to/file-or-dir
lantern receive <share-code> [output-directory]
```

## What holds, on every route

- The receiving device resolves the destination, inside its own roots
- Nothing is placed until the sender's digest matches the bytes that arrived
- An existing file or directory is never replaced without an explicit request
- Content is staged and renamed, so a failure leaves the destination as it was
- Permissions come from the directory the content lands in, never widened and
  never taken from the sender

A pairing grants `read` by default. `read-write` is a separate, deliberate
grant, and writes are off entirely unless the daemon runs with `--allow-writes`.
Both gates must pass.

Full model, and what counts as a defect:
[`CONTEXT.md`](CONTEXT.md) and [the README's security notes](#security-notes).

## When something is not working

Run `lantern doctor` first. Every check is read-only, and the failures it exists
for are silent ones that look identical from the outside.

```sh
lantern doctor                   # or: lantern --daemon-url http://127.0.0.1:43792 doctor
lantern doctor --probe-peers     # also dial each paired device
lantern doctor --json            # for agents
```

It reports on the daemon and its token, whether an advertised address is
actually dialable from another machine, whether the p2p port is pinned, whether
multicast leaves the host, and whether each paired device has a known address.
Add `--probe-peers` to dial each paired device and report reachability. It exits
non-zero when a check fails, so it works as a CI gate.

Two results are warnings rather than failures: an mDNS check that hears
nothing, and an idle paired device. Neither means the setup is broken.

### Static peers

If discovery cannot introduce two devices, for example a WSL2 NAT, a Docker
bridge, or Wi-Fi that blocks multicast, name the address directly:

```sh
lantern daemon --peer /ip4/192.168.1.20/tcp/41002/p2p/<peer-id>
```

Each value is a full multiaddr ending in `/p2p/<peer-id>`. The address is kept
for the running process and dialed on demand. A paired device's address is also
recorded in the trust store, so it survives a restart. Pin the p2p port with
`--p2p-port` on both sides, or the address goes stale on every restart.

In WSL2, use the Windows host's vEthernet address on the WSL side and the WSL
address on the Windows side. Run `lantern doctor --probe-peers` on both after
starting them.

## Development

```sh
gofmt -w .
go mod tidy
go vet ./...
go test ./...
go test -race ./...
```

CI runs `staticcheck` on Linux. The test suite runs on Linux, Windows, and
macOS, each with `go test -race`.

What a pull request needs, and how to report a problem:
[`CONTRIBUTING.md`](CONTRIBUTING.md).

The framing and the AEAD have fuzz targets, because both trust a length or a
nonce that arrived over a connection. They run their seed corpus as part of
`go test`, and search nightly:

```sh
go test -run XXX -fuzz FuzzReadMetadata -fuzztime 30s ./internal/protocol
```

A nightly run is a workflow you can start by hand from the Actions tab. It
discovers targets with `go test -list`, so a new one is covered the day it is
written rather than the day someone remembers to add it.

Design decisions that the code does not explain, and why they went that way, are
in [`docs/adr/`](docs/adr/). Two to read first:

- [0002](docs/adr/0002-new-pairings-default-to-read.md) if a paired device
  stopped being able to push after an upgrade
- [0003](docs/adr/0003-content-without-a-digest-is-refused.md) if a push **to**
  this device stopped working after an upgrade

## Security notes

To report a suspected vulnerability, use
[private advisory](SECURITY.md). What follows is the model, not a report
channel.

A paired device can read inside that device's shared roots. That is the read
path working as designed, and it lasts until the pairing is removed. Shared roots
are a hard boundary: paths outside them, including through a symbolic link, are
refused.

Pairing is not capability. What a paired device may do is chosen per device and
survives a reboot:

```sh
lantern trust add <peer-id> [alias]                # paired, reads only
lantern trust add <peer-id> nas --tier read-write  # and it may write here
lantern trust tier <alias> read                    # revoke its writes
lantern trust tier <alias> none                    # paired, nothing works
lantern trust roots <alias> ~/inbox                # it may write only there
lantern trust list                                 # each device's tier
lantern trust remove <peer-id>                     # revoke entirely
```

`trust roots` may only narrow a device's writable set. A root outside
`--writable-dirs` is rejected, so an edited pairing record cannot name its way
past the operator's bound. The policy is enforced in the libp2p stream handler,
not only at the HTTP layer, so a peer cannot bypass it by dialing directly.

A share code is a bearer secret, not a short human PIN. Whoever holds one can
consume that share. Requests prove possession with a challenge response before
a share is consumed, chunks use AES-GCM with fresh nonces, and a completed
transfer is checked against a full-file SHA-256.

Reads are encrypted and authenticated by the libp2p transport. A push has no
application-layer key exchange of its own, because there is no share code to
derive a key from. A per-pairing key is planned and does not exist yet.

**Upgrade senders before receivers.** A new sender can still push to an
un-upgraded receiver, because the old build ignores the added field. The reverse
order breaks pushes at once.

## Licence

MIT. See [`LICENSE`](LICENSE).