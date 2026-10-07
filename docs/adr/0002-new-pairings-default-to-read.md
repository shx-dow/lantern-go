# A newly paired device is granted read, not read-write

A pairing created today starts at tier `read`. It does not start at
`read-write`, even though that is the tier every pairing had before access tiers
existed.

## Considered options

**Default to `read-write`, and treat that as backwards compatibility.** Tiers
were introduced on top of a model where pairing conferred everything, so an
existing pairing record means its owner had granted push. Defaulting to
`read-write` would preserve that behaviour exactly and break nobody.

We chose not to. A pairing is a long-lived, one-directional grant with no natural
end point — it lasts until it is removed, and it can be forgotten. The old
behaviour meant the safe outcome required the operator to *remember* to narrow a
pairing they had just created, at the exact moment they were least likely to be
thinking about it. Choosing `read` means the action a user takes without thinking
is the one that grants the least.

## Consequences

- **Upgrading silently stops paired peers from pushing to this device.** A
  pairing written before tiers existed has no tier recorded, and loads as `read`
  rather than being rejected or gaining write access. Anyone relying on a paired
  device pushing files here has to run
  `lantern trust tier <alias> read-write`. This is called out in the README and
  is the first thing to check when pushes stop working after an upgrade.
- **The default is the safe direction to fail.** A pairing that was created by
  mistake, or by an agent acting on a vague instruction, grants reading inside
  the shared roots rather than the ability to change the disk.
- **Writes still need a second gate.** Tier `read-write` is necessary but not
  sufficient; the device must also have been started with writes enabled. The
  default is one of two independent checks, not the whole of the answer.