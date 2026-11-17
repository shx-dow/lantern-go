# A peer's relative path resolves inside the shared roots

When a paired device asks to read or list a path that is not absolute, the
serving device resolves it inside its shared roots. It never resolves it
against the daemon's working directory.

## The problem

The read and listing handlers passed the requested path straight to
`filepath.Abs`. A relative name therefore meant "relative to wherever the daemon
happened to be started", which is a directory the operator never shared and
cannot see. A read of `notes.md` failed unless the daemon was started beside
that file, and the names a listing printed could only be used by pasting the
whole absolute path back. The device that asked has no way to know the daemon's
working directory, so the failure was unexplainable from the asking side and the
feature looked broken rather than misused.

Write already had the right rule: `resolveWritePath` interprets a relative
destination on the receiving device, inside its first writable root, because
that is what a caller passing `report.txt` means. Reads had no equivalent.

## Decision

- An absolute path is unchanged: it must resolve inside a shared root.
- A relative path is joined to each shared root, and the result must resolve
  inside that root. `.`, `..`, and paths starting with `../` are refused
  outright; a request cannot climb out of a root even before containment is
  checked.
- A bare name that equals a shared root's own directory name addresses that
  root, so the names in a root listing can be typed back in.
- If more than one root yields the same path, the request is refused as
  ambiguous and both candidates are named. Picking one silently would read a
  different file than the caller asked for and would differ between devices.

Containment is still decided by `resolveWithinRoots`, and symlinks are still
evaluated before the comparison. This adds a name-resolution step; it does not
add a way out of the roots.

## Consequences

- A listing's output is directly usable as input to a read, which is what makes
  browsing practical over this protocol.
- A name present in two roots needs a fuller path. That is a refusal, not a
  silent choice, so the caller learns which roots collided.
- Paths already stored by the daemon (share codes) and absolute paths from
  existing callers keep their current meaning; only wire requests that were
  relative change behaviour, and they were previously broken.
