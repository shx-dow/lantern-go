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
daemon` with no extra flags, which means the daemon reads the per-user
`lanternd.json` written by `lantern setup`. Write access stays a start-time
flag and is deliberately not put in the unit: a service installed today must
not silently grant a write capability that the operator never chose.

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
- The service runs as the user, with the user's identity and shared roots, and
  no new privilege.
- A foreground `lantern daemon` and a service daemon cannot both own the same
  identity directory. Installing while a foreground daemon is running reports
  the service manager's start failure; stop the foreground one and re-run
  `install`.
- Unsupported platforms (systemd-less Linux, and any OS outside the three)
  keep the foreground command. They are not silently given a broken unit.
- No new network surface, account, or relay. The unit carries no secret; the
  daemon token stays in the per-user data directory with its existing
  permissions.
