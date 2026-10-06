# Write-path security audit

Status: internal adversarial review, not an independent audit. Covers the
properties named in issue #17 against `internal/p2p/fs.go` (`fsWrite`,
`resolveWritePath`, `resolveNewParent`), `internal/daemon/daemon.go`
(`PushFile`), `internal/daemon/auth.go`, `internal/daemon/cors.go`,
`internal/daemon/upload.go`, and `internal/daemon/fs.go`.

Regression net: `internal/p2p/audit_write_test.go`,
`internal/p2p/audit_perms_test.go`, `internal/daemon/audit_push_test.go`.

## Summary

The write path is built defensively and most of it holds under attack. Two
findings, both real, neither an arbitrary-file-write:

| # | Finding | Severity | Status |
|---|---------|----------|--------|
| 1 | Absent digest from the receiver is treated as "verified" | **Medium** | Fixed in `384b753` |
| 2 | Directories created by a push are world-accessible inside a private root | **Low** | Fixed in `ab0223b` |

Everything else in scope held: traversal in all spellings, symlinks at the
destination and at ancestors, overwrite consent, the size cap including its
boundary, negative lengths, truncated bodies, temp-file debris, digest
mismatch, token auth, and the trust-everyone write model (by design, see
#16).

## Finding 1 — an absent digest is treated as a verified copy

`internal/daemon/daemon.go:569`:

```go
if res.SHA256 != "" && res.SHA256 != localDigest {
    return PushResult{}, fmt.Errorf("digest mismatch: ...")
}
```

The `res.SHA256 != ""` guard means a receiver that reports **no** digest skips
the comparison entirely and the push is reported as delivered.

Why it matters: the receiver is the party being trusted here. It stores the
bytes and computes the digest, so a receiver that returns an empty digest can
report any content as successfully delivered — while the sender's response
body still carries `sha256: ""` and the caller sees a 200. The README promises
the opposite:

> the response carries both the digest of what was sent and the digest the
> receiver computed, so a corrupted copy is reported as a failure rather than
> a success.

A mismatching digest *is* rejected, so this is not a hole an honest peer walks
into. It needs a receiver that is buggy, truncated mid-response, or hostile.
Confirmed by reproducing the comparison in isolation: an empty remote digest
yields `rejected=false`.

Fix: fail closed. Treat an empty digest as unverifiable rather than as a pass.

```go
if res.SHA256 == "" {
    return PushResult{}, fmt.Errorf("remote reported no digest; cannot verify the copy")
}
if res.SHA256 != localDigest {
    return PushResult{}, fmt.Errorf("digest mismatch: sent %s, remote reported %s", localDigest, res.SHA256)
}
```

**Fixed** in `384b753`. `verifyRemoteDigest` treats an absent digest as
unverifiable rather than as a pass, and compares exactly — no case folding, no
trimming. The tests cover the comparison directly and end to end against a
hand-rolled receiver that consumes the body and reports whatever digest the test
asks for, which is the case the old check skipped. Reverting the fix fails both
the unit and the end-to-end test.

This is a behaviour change for any receiver that omits the digest, including an
older Lantern build. No such build is reachable in practice: `fsWrite` has
always set `SHA256` on success.

## Finding 2 — a push creates world-accessible directories

`internal/p2p/fs.go:399` creates missing parents with `os.MkdirAll(dir, 0755)`,
and `internal/p2p/fs.go:449` chmods the stored file to `0644`. Both ignore the
mode of the root the operator configured.

Reproduced: with a `0700` shared root, pushing to `nested/deeper/file.txt`
creates `nested/` and `nested/deeper/` at `0755`, and the file at `0644`.

Impact is bounded: a `0755` directory inside a `0700` root is still only
reachable through that root, so this is not an exposure by itself. It becomes
one when the operator later relaxes the root (`chmod 755`, a shared group, a
different mount) — every pushed file and every directory a push created becomes
readable at that point, which is wider than what the operator chose for their
own root. Low severity, real widening.

**Fixed** in `ab0223b`. `inheritedPerm` takes the mode from the nearest
existing ancestor, using the same walk `resolveNewParent` uses to prove
containment, and derives the file mode by dropping the execute bits.

Nothing is forced on. If the nearest ancestor is not owner-writable, the push
fails at the create with an ordinary permission error instead of succeeding by
restoring rights the operator deliberately removed; owner-read is the only
floor, so the owner can always read what was stored for them. `MkdirAll` applies
the mode only to directories it actually creates, so an existing parent keeps its
own mode and a permissive root still gets permissive content.

## What held under attack

Each of these is now a regression test.

**Containment.** Five spellings of traversal — `..` segments, `..` after a
valid subdirectory, nested-and-back-out, a root-prefixed relative string — all
refused, with `/etc/passwd` and an out-of-root victim file verified untouched.
`pathWithin` compares root-plus-separator, so `/srv/rooted` cannot match root
`/srv/root`, and the comparison is case-folded on Windows and macOS because
their filesystems are.

**Symlinks.** Refused at the destination, refused when an ancestor is a link
pointing out, and refused even when the link stays *inside* the root (the
refusal is about following links, not escaping). A symlink planted in the
window between the pre-content `Ready` and the rename is not followed: the
re-resolve after `MkdirAll` compares the whole path and refuses on any change.
The rename semantics mean the link would be replaced rather than followed
anyway, so this is defence in depth. A symlinked *root* is the operator's own
choice and resolves to its target, while its siblings stay refused — the
boundary follows the resolved location, not the spelling.

**Overwrite consent.** An existing file survives a non-consenting push byte for
byte, and the refusal arrives *before* content: the pre-content
acknowledgement carries the error and never sets `Ready`, so a sender is never
asked to transmit a payload that was going to be refused.

**Size cap.** Refused before content at any claim above the cap, with no
destination created; exactly at the cap is allowed and one byte over is not, so
the boundary is not off by one. A negative `ContentLen` is refused rather than
wrapping into a huge value that would pass the comparison.

**Failure hygiene.** A sender claiming 4096 bytes and sending 10 leaves neither
a destination nor a `.lantern-push-*` temp file. Directory-target refusals
leave nothing behind either.

**Atomicity.** Twenty overwrites racing a read loop never produced a blended
file, because the write is staged and renamed.

**Fail-closed defaults.** An unknown write mode refuses rather than falling
through. `WriteSharedRoots` with no roots refuses rather than widening to the
read roots — the failure mode that would silently hand over more than intended.
An unpaired peer is stopped by the pairing gate *before* the write policy, and
the refusal does not leak the write configuration.

**HTTP surface.** Six token cases refused at 401 before any filesystem work:
absent, wrong, empty bearer, truncated, trailing space, wrong scheme. The 401
body does not leak the expected token. The daemon never runs with an empty
token, and the token is stable across restarts.

**CORS.** Reflecting any `Origin` is only safe because the token is mandatory,
and it is: a cross-origin `GET /v1/devices` with no token is 401. Credentials
are not allowed. Preflights are answered before auth by design and leak nothing
but headers.

**Uploads.** `../`, `../../`, absolute, and Windows-separator filenames all
land inside `uploads/`, with nothing written above it.

## Out of scope, worth noting

- **Per-pair application keys.** Reads and writes ride the libp2p transport's
  encryption and the peer's identity key. There is no application-layer key
  exchange on this path, unlike the share-code path. Anyone who can impersonate
  a paired peer's identity key has the pairing's capabilities.
- **`WriteAnywhere` is unreachable in production.** Only
  `internal/daemonapp/app.go:202` constructs a policy, and it always uses
  `WriteSharedRoots`. The mode exists in the type and is exercised by tests
  only, so it is dead code with a genuinely dangerous shape. Worth removing, or
  worth refusing to construct outside tests.
- **Hardlinks are not detectable as escapes.** A hardlink to a file outside the
  root is a regular file inside it, so path containment cannot catch it. This
  is a filesystem property rather than a Lantern bug, but it means shared roots
  are not a defence against a peer that already has a local foothold.
  `TestAuditHardlinkIsNotDetectedAsEscape` documents it behind an env var so it
  does not assert a guarantee the platform cannot make.
- **Trust is all-or-nothing for writes.** `--allow-writes` grants every paired
  device the same standing. This is the largest remaining exposure and is issue
  #16.
- **The receiver trusts the sender's framing but not the sender.** Content is
  length-delimited and bounded by the cap, so a sender cannot overrun; but
  there is no per-write rate limit, so a paired peer can fill a disk with
  repeated pushes up to the cap.

## What this does not establish

No independent audit, no formal review of the libp2p or crypto internals, and
no fuzzing. The tests attack the properties the audit scope named; they do not
cover protocol versions this build does not speak, a hostile libp2p
implementation, or side channels. The write half of the product should still be
treated as experimental software.
