# Content without a sender digest is refused

The fs protocol is at revision 2. A write carries the sender's digest
(`FSRequest.SHA256`), and every response that serves content reports one
(`FSResponse.SHA256`). A peer that offers content with neither is refused.

## Considered options

**Keep treating an absent digest as "skip the check".** This is what revision 1
did, and it is the behaviour in the field today. It costs nothing and it breaks
nobody on upgrade.

We chose not to. "Skip the check" made the guarantee conditional on something
the sender chose, so the sender decided whether the receiver verified anything.
A peer that omitted the digest landed arbitrary bytes at the destination as a
completed transfer, and the receiver reported success — the same shape as a
verified one. The property CONTEXT.md states unconditionally, "nothing is placed
until the digest of what arrived has been verified", was true only for senders
that opted in, and nothing in the protocol distinguished the two cases. Reading
the code comment next to the archive expansion, which said the digest "has now
been verified against the bytes that arrived", was enough to believe the check
happened; it did not, and the sender compared the digests in its own process
after the tree had already landed.

**Version gate instead: accept revision 1 for one release, then drop it.** Add a
`ProtocolVersion` field, keep revision 1 working as unverified, and remove it in
the following release. This gives a deprecation window and costs one field.

We chose not to, for two reasons. The field is not the hard part — the window is
what keeps unverified content landing for a full release cycle, which is the
whole problem being fixed. And the unverified path has no correct future: there
is no configuration in which a receiver should accept bytes it cannot vouch for,
so a window here is a delay, not a migration.

## Consequences

- **A mixed-version pair stops transferring files.** A revision 2 device refuses
  a revision 1 peer's writes, reads, and fetches; a revision 1 device has no
  digest field to compare. This is deliberate, and it is the first thing to check
  when a transfer stops working after an upgrade: upgrade both ends of the pair.
  The refusal names the required revision rather than reporting a generic
  failure, so the cause is visible in the error rather than something to guess
  at.
- **A mismatch now leaves nothing behind.** Both routes stage first and place
  last. A refusal removes the staged file, its resume checkpoint, and the output
  directory the archive would have expanded into, so a failed attempt does not
  become the seed of the next one.
- **The push route verifies for the first time.** It previously computed a
  digest and handed it back to the sender, which compared it elsewhere; the
  receiver never compared anything. Verification now happens on the device that
  received the bytes, before anything is renamed or expanded into place.
- **The revision is reported on every response**, including successes. Nothing
  branches on it yet — the break is detected by the digest being absent, and the
  refusal names the required revision — but a peer can see what it is talking to,
  and a future build can detect the mismatch up front rather than at the first
  refusal.