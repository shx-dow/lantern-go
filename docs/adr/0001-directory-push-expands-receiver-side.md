# Directory push sends an archive the receiver expands

A pushed directory travels as a single zip archive over the ordinary write
operation, and the receiving device expands it into a directory it creates
itself. We chose this over pushing the tree file-by-file, and over adding
archive framing to the filesystem protocol.

## Considered options

**Enumerate, then push each file over the existing write operation.** This was
the closest alternative and it reuses code even more directly. It loses on two
counts. It costs one round trip per file, so a large tree is slow for a reason
that has nothing to do with the network. More seriously it cannot be atomic:
there is no point at which the tree is complete, so a transfer that dies half
way leaves a partial directory that looks like the whole thing. That is the
failure mode a staged write exists to prevent, and per-file pushes reintroduce
it at the directory level.

**Archive framing on the filesystem protocol, expanded by the sender.** This
sends the same bytes but pushes the work onto the sender, which then writes
entries individually and inherits the same non-atomicity, just with more
network traffic.

## Why the receiver expands

Reusing one verified write is the whole point. Because the archive crosses the
wire as a single content stream, everything already proven about a file push
carries over unchanged: the receiver resolves the destination inside its own
roots, refuses to replace without an explicit request, verifies the digest
before acting on the content, and stages then renames so nothing is visible
half-written. A directory push is not a second write path with its own rules; it
is the existing one, with an expansion step at the end.

## Consequences

- **Replacing a directory replaces the whole tree.** Merging cannot be made
  atomic, and this way a repeat push cannot quietly change what a peer holds.
  Deliberately unsupported.
- **Symbolic links in an archive are refused.** An archive must not be able to
  plant a link that redirects a later write.
- **Two caps, for two different sizes.** The per-device byte cap applies to the
  archive, which is what crosses the wire. Separate caps bound the expanded tree
  and its entry count, because a zip declares its uncompressed sizes and a
  small archive can otherwise claim to fill a disk.
- **The verified digest is the archive's**, not the expanded tree's. The
  archive is the thing that moved between two devices.
- **Permissions are inherited** from the destination's parent directory, never
  taken from the archive.