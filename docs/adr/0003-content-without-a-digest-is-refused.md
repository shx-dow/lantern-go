# Content without a sender digest is refused

The fs protocol is at revision 2. A write carries the sender's digest
(`FSRequest.SHA256`), and every response that serves content reports one
(`FSResponse.SHA256`). A peer that offers content with neither is refused.

The break is one-directional in practice, and only for pushes: see the
consequences below.

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

- **A revision 1 sender can no longer push to a revision 2 device.** The request
  has no digest field to compare, so the push is refused, and the refusal names
  the required revision rather than reporting a generic failure. This is the
  first thing to check when a push *to* a device stops working after an upgrade.
- **Downloads are not affected by version.** Revision 1 already sent a digest on
  both the resolve-share reply and the share-code transfer header, because both
  were computed when the share was registered. A revision 1 sender's downloads
  therefore still verify against a revision 2 receiver, in both directions.
- **A revision 2 sender pushing to a revision 1 receiver silently succeeds, and
  the receiver does not verify.** The old build ignores the added request field
  and hands back the digest it computed over what it stored, which the sender
  then compares — so the sender's end-to-end check still holds. What the old
  receiver cannot do is refuse before placing, which is the guarantee revision 2
  adds.
- **Upgrade senders first.** The order is not symmetric. Bringing a *receiver*
  up to revision 2 while its senders are still on revision 1 is what triggers the
  refusal above, so receivers-first breaks pushes immediately. Bringing the
  senders up first does not, because a revision 2 sender pushing to a revision 1
  receiver still succeeds — pushes keep working while receivers catch up. Only
  once every sender is on revision 2 does upgrading a receiver cost nothing.
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