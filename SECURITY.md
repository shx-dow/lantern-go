# Security policy

## Status

Lantern is pre-alpha. Releases are alpha only, with no supported versions and
no compatibility promise, so treat it as experimental software.

## Reporting a vulnerability

Use **GitHub's private vulnerability reporting**: go to
[Security → Report a vulnerability](https://github.com/shx-dow/lantern-go/security/advisories/new)
on this repository. That opens a private advisory only the maintainer can read.

Do not open a public issue for a suspected vulnerability. A public issue is
visible to everyone before a fix exists, and a proof of concept is often the
part that helps someone else.

Private reporting is enabled on this repository. If that link does not load, the
repository settings have changed since this file was written.

### What to include

- What an attacker can do, and what they need in order to do it
- The version or commit you tested, and the platform
- Steps to reproduce, or a proof of concept
- Whether a paired device is needed, and at which access tier

The pairing model matters when writing a report. A paired device holding
`read-write` can already write inside the writable roots, so that is not a
vulnerability. A paired device that can write **outside** them, or that can read
outside the shared roots, is.

### What to expect

A maintainer reads the advisory and replies. There is no guaranteed response
time and no coordinated disclosure deadline, because this is a single-maintainer
project. If a report is accepted, a fix and an advisory come first, and the
reporter is credited unless they ask not to be.

If you need a deadline enforced, say so in the advisory and treat silence as a
reason to publish.

## What counts as a vulnerability

These are bugs in Lantern. Each one lets a peer do something the operator did
not grant:

- reading a file outside a shared root, including through a symbolic link or a
  path that resolves after traversal
- writing a file outside a writable root
- replacing an existing file or directory without an explicit overwrite request
- placing content whose digest was never verified, or whose digest did not match
- widening a pairing's writable roots past what the operator granted
- accepting content through an operation that needs a pairing, from a peer that
  holds none, or none at the tier the operation needs. A valid **share code**
  authorises a transfer on its own, so a share-code transfer from an unpaired
  device is the design working, not this
- placing partial content after a failure, so a reader sees a half-written file
- making the permissions of placed content wider than the destination directory

## What does not count

- A **paired** device reading inside the shared roots. That is the read path
  working as designed, and it lasts until the pairing is removed.
- A **paired** device writing inside the writable roots while the daemon runs
  with `--allow-writes` and the pairing is at tier `read-write`. Both gates
  passing is the grant.
- A share code being a bearer secret. Whoever holds a share code can consume that
  share. That is what a share code is.
- DoS against your own daemon, or a peer you chose to pair.

The vocabulary for all of these is in [`CONTEXT.md`](CONTEXT.md).

## Known limitations

**No independent audit.** Nobody outside this project has reviewed the write
path. An internal adversarial pass is recorded in
[`docs/write-path-audit.md`](docs/write-path-audit.md); both findings it made
are fixed, and it is still not an independent audit.

**Writes are the newer half.** Reads are encrypted and authenticated by the
libp2p transport. A push has no application-layer key exchange of its own,
because there is no share code to derive a key from. A per-pairing key is
planned and does not exist yet.

**Pairings outlive their purpose.** A pairing is long-lived and grants nothing
beyond `read` by default, but it has no natural end point. Revoke one you no
longer want with `lantern trust remove <peer-id>`.

## Version compatibility

Releases are alpha only. No version is supported, so there is no version table
to maintain and nothing carries a compatibility promise. `main` is the only
thing that exists.

One compatibility fact matters to a reporter, because it changes what a failure
means. A **sender** older than the digest contract cannot push to a current
device: its content carries no digest, and unverifiable content is refused. A
push failing that way is the digest check working, not a defect. The refusal
names the protocol revision. See
[ADR 0003](docs/adr/0003-content-without-a-digest-is-refused.md).

For upgrade order and the rest of the model's guarantees, see the
[README](README.md#security-notes).