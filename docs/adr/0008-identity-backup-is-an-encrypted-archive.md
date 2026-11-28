# An identity backup is an encrypted archive

**Status:** accepted.

A device's identity is one private key. The peer ID other devices pair with is
derived from it, so losing the key is not a local loss: every device paired with
this one holds an ID that no longer exists, and each of them has to be paired
again by hand. Until now the only backup was to copy the data directory, which
puts a plaintext private key wherever the copy goes — a USB stick, a synced
folder, an email attachment.

## Considered options

**Document copying the data directory.** No code, but the file it copies is the
device's private key in the clear. A backup that is dangerous to store is not
one people take.

**A plaintext archive.** One command, no passphrase to forget, and the same
exposure as copying the directory under a friendlier name.

**Encrypt with the daemon's bearer token.** No new secret, but the token lives
on the same disk as the key, and a restore onto a new machine has no token yet.

**Encrypt with a passphrase (chosen).** The archive is useless without a secret
the operator chooses and can keep somewhere else. The cost is that a forgotten
passphrase is unrecoverable, which the command's help says plainly.

**No backup; pair again.** Honest, and the status quo, but it turns a lost disk
into an afternoon of re-pairing and re-granting tiers on every other device.

## Decision

`lantern identity export` writes one file containing the identity key and
`trusted_peers.json`, encrypted with a passphrase: scrypt (N=2^15, r=8, p=1)
derives the key, and AES-256-GCM seals it. The unencrypted header carries the
format version, peer ID, device name, creation time, pairing count, and KDF
parameters, and is authenticated as the AEAD's additional data. A reader bounds
every KDF parameter before deriving, so a hostile archive cannot exhaust memory.

`lantern identity import` restores the two files. It takes the state
directory's lock, which the daemon holds while running, so an import cannot race
the key the daemon loaded; the refusal names the daemon. An existing identity is
left alone unless `--force` is given, and then it is moved to
`<name>.pre-restore-<stamp>` rather than deleted.

The archive deliberately excludes:

- **The bearer token.** It is per-machine and minted on start; restoring one
  would carry a secret that has nothing to do with identity.
- **`shared_dirs` and `--writable-dirs`.** Those are settings of this machine.
  Restoring paths that do not exist would produce a daemon that starts and
  serves nothing, which looks like a broken pairing.
- **Per-peer writable roots are restored as recorded.** They are already
  bounded by `--writable-dirs` on the machine, so a path that does not exist
  simply cannot be written to.

## Consequences

A backup is only as good as its passphrase, and there is no recovery path. The
format is versioned; a newer archive is refused with its version rather than
half-read. There is no key rotation: changing the passphrase means exporting
again, and changing the identity means pairing again. The archive is a backup,
not a sync mechanism: importing it on two machines at once gives them the same
peer ID, and only the directory lock on each machine stops two daemons there
from fighting over it.
