# A read response carries a digest of the range it served

The fs protocol is at revision 3. An `OpRead` response carries the serving
device's SHA-256 of exactly the bytes that follow the response frame, in
`FSResponse.SHA256`. This extends the rule ADR 0003 set for writes and
share-code replies to the plain read path.

## The problem

Revision 2 required a sender digest on every write and on every share-code
reply. A read response had none, and the comment said so: a read of a share code
is verified against the digest the resolve step returned, so the digest was not
repeated per chunk.

That reasoning only holds when a resolve step exists. A generic read — ask a
paired device for a path, get the bytes — has no resolve and therefore no
digest anywhere. The caller could check the size the same peer reported and
nothing else. `CONTEXT.md` says a download is a landing route like any other and
that nothing is placed until the digest of what arrived has been verified, so a
command that writes a read response to disk would have broken that guarantee the
moment it existed.

## Considered options

**A digest trailer after the content.** Send the header as today, stream the
bytes, then send one more frame carrying the digest. This avoids buffering and
keeps the sender's memory bounded. It changes the framing: every reader of a
read response must know to read a trailer, and a reader that stops after
`Bytes` leaves the stream in a state the sender cannot distinguish from a slow
one. One extra frame for a field that exists on every other content response is
not worth a second framing rule.

**Hash the whole file before the first read and verify once at the end.** This
is the shape a share code uses, and it lets a download verify a complete file
rather than chunk by chunk. It needs a resolve step the generic read does not
have, a second round trip before any content moves, and it is defeated by a file
that changes between the hash and the read. The write path has the same
exposure and accepts it; matching that is simpler and says the same thing.

**Leave reads unverified and let the caller decide.** This is what shipping
`lantern get` on revision 2 would have done, and it is exactly the failure ADR
0003 rejected for pushes: verification would be conditional on what the peer
chose to send, and a verified and an unverified download would be
indistinguishable from the outside.

## Decision

Revision 3 read responses carry the digest of the returned range. Because the
digest is in the header, the sender has to produce the content before the
header, so a read buffers at most one response — capped at `MaxReadLength`
(8 MiB). A range read reports the digest of that range, not of the whole file.

Stat and listing responses serve no content and carry no digest; an empty field
is the honest answer there. A caller that places content treats a missing digest
as a refusal and names the required revision, the same way a revision 2 write
does.

## Consequences

- **A read is now verifiable on its own.** A download can compare each chunk
  before writing it, and a truncated or altered response fails as a chunk
  mismatch rather than a successful copy of different bytes.
- **A revision 1 or 2 peer's read cannot be verified**, so a caller that places
  content refuses it. Reading to display text, as the MCP `read` tool does,
  keeps working: it places nothing.
- **The sender buffers up to 8 MiB per in-flight read.** That is the response cap
  that already applied; the trailer option was rejected to keep one framing.
- **Writes and share-code transfers are unchanged.** Neither payload shape nor
  version gate moved, so an existing push keeps working with only the version
  number on the response changing.
