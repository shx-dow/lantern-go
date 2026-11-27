# The daemon runs as a per-user service

**Status:** accepted.

Today the first-run instructions end with `lantern daemon` in a terminal and a
warning not to close it. Closing the terminal, logging out, or rebooting stops
the daemon. A paired device then fails with "no daemon answering", which looks
like a network problem even though the cause is a closed window.

An always-on daemon needs an operating-system mechanism that starts it at
login and restarts it after a crash. Every supported platform has one, and all
three can run it as the user who owns the identity and the shared directories,
so no step needs administrator rights.

## Considered options

**Tell people to leave a terminal open.** The current state. It works until
the window is closed, and it is the reason a working pairing can appear to
break overnight.

**A system-wide service (root/SYSTEM).** It would start before login and run
for every user, but it needs elevation to install, runs with more privilege
than the daemon needs, and would read the wrong home directory unless the unit
hard-codes one user. Lantern's state is per-user: the identity key, trust
store, token, and shared roots all belong to the person who ran `lantern
setup`. A system service is the wrong shape for that state.

**A process supervisor the user already runs** (supervisord, s6, a container
entrypoint). Reasonable in a server deployment, but it is not present on a
laptop and would make the first-run path depend on a third-party tool.

**A hosted relay or an account system.** Rejected by the project's constraints
and irrelevant to process lifetime. The daemon still has to be running
somewhere.

## Decision

`lantern service install`, `lantern service uninstall`, and `lantern service
status` manage one per-user registration. There is no `start`, `stop`, or
`restart` subcommand: after installation the platform's service manager owns
the process lifetime, and the CLI does not duplicate it.

`install` writes the platform file, registers it, and starts the daemon now
(except on Windows, where the entry takes effect at the next sign-in). It is
idempotent: running it again replaces the file and leaves the service running.
`uninstall` stops the daemon and removes the registration, and does nothing if
nothing is installed. `status` prints whether the registration exists and
whether the daemon is running, and exits non-zero when it is not running.

The command uses the executable that is running it, resolved through symlinks,
so the service starts the same binary the user invoked. It runs `lantern
daemon` with the flags the operator passed to `service install`:
`--allow-writes`, `--writable-dirs`, `--p2p-port`, `--peer`, `--data-dir`,
`--addr`, `--config`, and `--allow-remote`. A flag that is not given is left
out of the registration, so the daemon reads `lanternd.json` and its own
defaults for it; an install with no flags behaves exactly like `lantern setup`
then `lantern daemon`.

Write access still has to be chosen explicitly. `--allow-writes` is written
into the registration only when the operator passes it, and a reinstall
without it takes it away again: a service installed today must not silently
grant a write capability that nobody asked for. `--config` and `--data-dir`
are accepted for the same reason — a daemon started with a non-default state
directory should come back after a reboot still using it.

`--token` is refused. The unit, plist, and registry value are readable by
every local user, and the token is what keeps the loopback API from other
accounts on the same machine; it stays in the data directory.

The registration is the only stored copy of these choices. Changing one means
running `install` again, which rewrites the file and restarts the service.

### Linux: systemd user unit

The unit is `$XDG_CONFIG_HOME/systemd/user/lanternd.service`, or
`~/.config/systemd/user/lanternd.service` when `XDG_CONFIG_HOME` is unset. It
runs:

```sh
systemctl --user daemon-reload
systemctl --user enable --now lanternd.service
```

`Restart=on-failure` restarts a crashed daemon after five seconds; a clean
exit from `systemctl --user stop` is not a failure and stays stopped. Logs go
to the user journal (`journalctl --user -u lanternd`).

A systemd **user** manager normally stops when the last session for that user
ends. Without lingering, the service therefore stops at logout even though it
is enabled. `lantern doctor` warns when lingering is off, and the fix is:

```sh
loginctl enable-linger "$USER"
```

Lingering is not enabled by `install`: it changes a machine-wide policy and
should be the operator's choice, not a side effect of installing a file
transfer daemon. Distributions without systemd are not supported by this
mechanism; `install` says so and leaves `lantern daemon` as the fallback.

### macOS: launchd user agent

The agent is `~/Library/LaunchAgents/com.lantern.lanternd.plist`, loaded with:

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.lantern.lanternd.plist
```

`RunAtLoad` starts it at login, and `KeepAlive` with `SuccessfulExit=false`
restarts a crash without fighting an explicit `launchctl bootout`. Logs go to
`~/Library/Logs/lantern/lanternd.log`. `uninstall` boots the agent out before
removing the plist, so the running process stops rather than being orphaned.

### Windows: per-user startup entry

The entry is the `Lantern` value under
`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, with the data
`"<path-to-lantern.exe>" daemon`. It starts at the next sign-in for that user
and needs no elevation.

A real Windows service is deferred. It needs administrator rights, an SCM
integration that reports status and handles stop requests, and a decision
about which user's state it reads. Per-user startup meets the requirement that
matters here: the daemon comes back after a reboot without a terminal. The
cost is that a console window appears at sign-in and the daemon's output goes
to that window rather than a log file. `uninstall` removes the value; it does
not kill a daemon the user may have started by hand.

### One registration at a time

Before writing anything, `install` looks for another registration that runs
`lantern daemon`: another `*.service` in the systemd user unit directories,
another launch agent in `~/Library/LaunchAgents` or `/Library/LaunchAgents`,
or another value under `HKCU\...\Run` besides its own. Finding one is an error
that names it and the command that removes it.

The case this exists for is the hand-written unit people already have from
following older instructions. Adding `lanternd.service` beside it would leave
both enabled, so the next login would start two daemons, the second would fail
on the identity directory, and the user would see only "not running".
Refusing is reversible in one step and keeps the failure where it can be
explained.

`install` also reports the manager's last output when the daemon exits right
after a successful start. `systemctl --user enable --now` returns once the job
is accepted, not once the process has stayed up, so a locked identity
directory or a busy port is otherwise indistinguishable from a service that
merely needs starting.

### `lantern doctor`

Doctor gains a `service` check. It runs only when doctor is pointed at a
loopback daemon, because a service on this machine says nothing about a daemon
on another one. The check:

- **passes** when the registration exists and the daemon is running;
- **warns** when no registration exists, with `lantern service install` as the
  fix, because a foreground daemon works but stops with its terminal;
- **fails** when a registration exists but the daemon is not running, with the
  platform's start command as the fix;
- **warns** on Linux when lingering is off, naming `loginctl enable-linger`;
- **skips** on a platform with no supported mechanism.

Doctor stays read-only. Reading the state means `systemctl --user is-active`,
`launchctl print`, or `reg query`, none of which change anything.

## Consequences

- The first-run path no longer asks anyone to keep a terminal open.
- A device that should accept pushes installs the service with
  `--allow-writes` (and `--writable-dirs`), so the always-on daemon and the
  write path are not mutually exclusive.
- A machine with an older hand-written unit is told to remove it rather than
  left with two daemons that fight after the next login.
- The service runs as the user, with the user's identity and shared roots, and
  no new privilege.
- A foreground `lantern daemon` and a service daemon cannot both own the same
  identity directory. Installing while a foreground daemon is running prints
  the manager's last output, which is where the daemon says the directory is
  locked; stop the foreground one and re-run `install`.
- Unsupported platforms (systemd-less Linux, and any OS outside the three)
  keep the foreground command. They are not silently given a broken unit.
- No new network surface, account, or relay. The unit carries no secret; the
  daemon token stays in the per-user data directory with its existing
  permissions.
