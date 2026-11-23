# One paste pairs two devices in both directions

**Status:** accepted.

Today pairing takes four commands and two hand-copied peer IDs:

```sh
lantern status            # on the laptop: 12D3KooW...
lantern status            # on the desktop: 12D3KooW...
lantern trust add <desktop-peer-id> desktop   # on the laptop
lantern trust add <laptop-peer-id> laptop     # on the desktop
```

`lantern pair` is meant to replace that with one paste. The laptop prints a
single-use invitation; the desktop redeems it; both devices end up paired at
`read`. Nothing is copied between screens by hand.

## Considered options

**Keep the manual copy.** No protocol change, and it already works. But the two
long base58 IDs are exactly the part people get wrong: a truncated paste names a
different device, and a swap pairs the wrong direction. It also has no expiry —
a peer ID typed once is a permanent read grant with no memory of why it was
granted. It stays as the fallback for devices that cannot dial each other, not
as the first-run path.

**A share code that delivers a pairing file.** Reuses the transfer path, but a
share code authorizes bytes, not pairing. The receiver would still have to make
a second trust decision, so the flow is two steps and still involves reading an
ID off another screen.

**An invitation shown only as a QR code.** Same token, different presentation.
Useful for a phone camera, but the paste is what makes this work over any chat,
ticket, or terminal. QR can be added to the same token later without changing
what the token means.

**Pair through a relay or an account.** Rejected here. It would put a third
party in the pairing path and needs its own ADR; this design asks only that the
two devices can dial each other at the moment of redemption, which is the same
requirement `trust add` already has.

## Decision

`lantern pair` on one device mints an invitation: a single string carrying an
expiry and a random secret. (The first version also carried the inviter's peer
ID and addresses; see the [amendment](#amendment-2026-11-21-the-invitation-names-no-address)
below.) `lantern pair <invitation>` on the other device finds the inviter,
presents the secret, and both devices store a pairing for the other at tier
`read`.

The invitation is the consent on both sides, so it has to be a narrow
capability:

- **One pairing, one use.** Redeeming creates a pairing for exactly one peer,
  the one that presented the secret. The invitation is burned by the first
  redeem attempt that reaches the inviter, whether or not the pairing is
  created, so a leaked invitation cannot be replayed after the intended use.
- **It expires.** Ten minutes by default, `--ttl` to change it. An expired
  invitation is refused with the command that mints a new one.
- **It grants `read`, never more.** New pairings default to `read` (ADR 0002),
  and no field in an invitation can raise a tier or add a writable root. Write
  access remains a start-time decision on the serving device (`--allow-writes`,
  `--writable-dirs`); an invitation cannot touch it.
- **It is not stored.** Outstanding invitations live in daemon memory only, so
  restarting the daemon invalidates them and the secret never reaches disk.
- **An existing pairing is not demoted.** If the peer is already paired,
  redeeming updates the addresses this device knows and leaves tier and roots
  alone.
- **Each side can refuse.** The inviter learns the redeemer's peer ID from the
  connection that presented the secret; the redeemer learns the inviter's peer
  ID from the invitation and confirms it in the response. A device pairs only
  the peer it intended, not whichever device answered.

Aliases default to the other device's reported name. `trust list` shows what
resulted; renaming stays with the existing trust commands and is not part of
this change.

### Shape

- A new libp2p stream protocol, `/lantern/pair/1.0.0`, with one request and one
  response, both length-delimited frames written with the existing `protocol`
  helpers.
- Request: protocol version, the secret, the redeemer's device name. The
  redeemer's peer ID comes from the connection, not from a field it could lie
  about.
- Response: accepted or a reason (`expired`, `used`, `unknown`), then the
  inviter's peer ID, device name, and the tier actually in effect.
- Two daemon routes, `POST /v1/pairing/invitations` and
  `POST /v1/pairing/redeem`, so the CLI, the MCP server, and a future UI share
  one implementation. The CLI holds no pairing logic, as with `trust add`
  today.

## Amendment (2026-11-21): the invitation names no address

The original decision put the inviter's peer ID and dialable multiaddrs in the
invitation. That made the string about 430 characters, exposed LAN and public
addresses in whatever chat window carried it, and went stale when DHCP moved the
inviter between minting and redemption.

The invitation is now:

```
lantern-pair1.<secret>.<expiry>
```

`secret` is 32 random bytes, base64url without padding; `expiry` is Unix
seconds. The string is about 70 characters. Nothing else is in it.

The inviter publishes its addresses in the DHT under a name derived from
`sha256("lantern/pair-lookup/1\x00" + secret)`, using the same
`Provide`/`FindProviders` path as a share code. The redeemer derives the same
name, looks up the provider, dials it, and sends the existing `PairRequest`.
Only a holder of the pasted string can derive the name. The inviter's peer ID is
not a field: the connection authenticates it, and the response is checked
against the peer that was reached. A local advertisement carries the same lookup
between two nodes that share a data directory, which is what the same-process
tests use.

What does not change: the secret is the only thing a redeemer presents; the
invitation is burned by the first attempt that reaches the inviter; the TTL, the
read-only default, and the memory-only store are unchanged. A burned invitation
stays findable until its deadline because the DHT provider record does too, so a
second attempt is refused as used rather than reported as an unknown device.

The cost is reachability. The old string carried an address that worked even
when discovery did not. Now both devices must be able to find each other: on a
LAN through mDNS, on separate networks through the public DHT, or after a
`--peer` connection has put them in each other's routing table. When none of
those is true, `lantern pair` cannot find the inviter, and manual pairing by
peer ID (`lantern trust add`) remains the documented fallback.

## Consequences

- **First run is one paste.** The laptop runs `lantern pair` and sends the
  string; the desktop runs `lantern pair <string>`; both directions are paired
  and either can read the other's shared roots. The device names are exchanged
  automatically, so `lantern get <name>:<path>` works without a rename.
- **The invitation is a secret in transit.** It travels through chat, a
  terminal scrollback, or whatever the operator pastes it into. Its blast radius
  is one read pairing, revocable with `trust remove`, and it dies after one use
  or ten minutes. It is printed once to stdout and never logged.
- **The default stays least-privilege.** Pairing still confers reading only;
  promoting a specific peer to `read-write` is still the explicit
  `lantern trust tier` step, and the device still has to be started with
  `--allow-writes`.
- **Manual pairing remains.** Devices that cannot dial each other — separate
  networks with no relay, or a firewall that blocks the connection — keep
  using `lantern trust add` with a peer ID. `lantern pair` fails with a message
  that says so rather than leaving a half-made pairing.
- **No new infrastructure.** No account, no hosted relay, no long-lived
  invitation. A future hosted introduction would be a separate ADR.

## Not decided here

- QR or clipboard presentation of the same invitation.
- Whether an invitation may name a non-default tier. Rejected for now on
  least-privilege grounds; a `read-write` invitation would turn one paste into
  a write grant, which the current model deliberately keeps separate.
