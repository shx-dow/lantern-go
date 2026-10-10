# Lantern

Lantern moves files between a person's own devices so that an agent can ask for
one without handling a share code. The language below is the vocabulary the
docs, the CLI, the daemon API and the code are all supposed to use.

## The things involved

**Device**:
One machine running Lantern. It has one persistent identity, one set of
pairings, and one set of directories it offers.
_Avoid_: node, host, box, machine

**Peer ID**:
The permanent public identity of a *remote* device. A device's own peer ID is
fixed for its lifetime and is what a pairing names.
_Avoid_: device ID, address

**Pairing**:
A stored, one-directional record that this device knows a given peer and grants
it a chosen amount of access. A pairing is long-lived and has no natural end
point, so what it confers has to be chosen rather than inferred from the fact
that it exists. Pairing alone confers nothing beyond reading.
_Avoid_: trust relationship, friendship, connection

**Access tier**:
What a pairing actually confers on this device: `none` grants nothing, `read`
allows reading inside the shared roots, and `read-write` additionally allows
writing inside the writable roots. The tiers are ordered, and a tier includes
everything below it.
_Avoid_: permission level, access level, mode

**Connection**:
A live network session with a peer. Lantern makes connections on demand, when
something is asked for, and otherwise holds none.
_Avoid_: link, session, pairing

## The directories a device offers

**Shared root**:
A directory the operator marks as readable by paired devices. A read that
resolves outside every shared root is refused.
_Avoid_: shared dir, read root, export, mount

**Writable root**:
A directory the operator marks as writable by paired devices that are at tier
`read-write`. A write that resolves outside every writable root is refused.
_Avoid_: writable dir, write root, drop box, inbox

**Writable roots are the device's; a pairing may only narrow them.** What a
particular peer may write is that set intersected with whatever the operator
granted that peer. There is no way to widen a device's writable set from a
pairing record.

## The three ways bytes move

**Read path**:
The asking device requests a path and the serving device answers, scoped to its
shared roots. No share code, no staging copy, no transfer session. This is the
path an agent uses for "get me this from the laptop".
_Avoid_: calling it a fetch, download, pull

**Fetch**:
The command and tool name for consuming a share code into a directory. It is
named for what the user does, not for the route: a fetch may end up on the
read path or on a transfer, and which one it took is the serving device's
decision.

**Share code**:
A high-entropy secret that names a share. It is a capability in its own right,
independent of any tier: presenting one is what authorises a transfer, not
being paired. It also serves as a transfer's identifier.
_Avoid_: PIN, password, key, token, link

**Share**:
A file or directory the operator advertised under a share code, together with
the transfer session that moves it. Sharing a directory advertises it as a
single archive; the receiving side does the expanding.
_Avoid_: transfer, upload, job

**Transfer**:
The session that moves a share's bytes, identified by its share code. It can be
inspected, resumed and cancelled while it runs.
_Avoid_: job, task, operation

**Push**:
The sending device dials the receiver and writes to it, so a file or directory
can be placed without the receiver asking. The receiver decides whether it
lands. Pushing a directory places a whole tree, which replaces rather than
merges.
_Avoid_: write, send, upload, copy

## Resolving between them

A share code presented by a paired device is **resolved onto the read path**
where it can be, and falls back to a transfer where it cannot. The serving
device makes that call, because it is the only party that knows which path the
code names and whether that pair may read it. Both routes deliver the same file
and both verify it against a digest the sender computed.

A successful transfer therefore proves nothing about which route ran.

## What a landing write guarantees

Every route that puts bytes on a device holds to the same properties, so a
caller does not have to reason about which one they used:

- the **receiving device** resolves the destination, inside its own roots
- an existing file or directory is never replaced without an explicit request
- nothing is placed until the **digest** of what arrived has been verified
- content is staged and renamed into place, so a failure leaves the destination
  as it was rather than partly written
- permissions are **inherited** from the directory the content lands in, never
  widened and never taken from the sender

A **digest** is the sender's hash of what it sent, compared against the
receiver's hash of what it stored. A mismatch is reported as a failure, never
as a delivery. It is also mandatory: content that arrives without one cannot be
verified, so the receiving device refuses it rather than storing it unchecked.
The comparison happens on the device that received the bytes, before anything
is placed, not afterwards in the sender's process.