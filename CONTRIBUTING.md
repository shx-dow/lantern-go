# Contributing

Lantern is an alpha project with one maintainer. A small, focused change is
easier to review, test, and undo than a broad one.

## Before you build

- **Suspected vulnerability**: report it privately through
  [SECURITY.md](SECURITY.md). Do not open an issue or a pull request for it.
- **A question about running Lantern**: ask in
  [Discussions](https://github.com/shx-dow/lantern-go/discussions). The issue
  tracker is for defects and work that has been agreed first.
- **Non-obvious bug or behaviour change**: open an issue and agree on the
  approach first. A pull request that solves the wrong problem costs more than
  the discussion would have.
- **A design decision the code cannot explain**: add a record in
  [`docs/adr/`](docs/adr/) with the next free number. Say what was decided and
  why the alternatives lost.

## Build and check

Fast loop, while iterating:

```sh
gofmt -l .          # must print nothing
go vet ./...
go test -short ./...
```

`-short` skips tests that measure a deadline, wait for a TTL, or move
megabytes over a real stream. It is a feedback loop, not a release gate. Use it
to find a red test quickly, then run the full suite.

Before a pull request:

```sh
go test -race ./...
staticcheck ./...
```

CI runs the full suite, without `-short`, on Linux, macOS, and Windows, plus
Staticcheck on Linux. A change that passes only on the machine it was written
on is not passing.

Fuzz targets run their seed corpus as part of `go test` and search nightly. A
new parser or framing layer should come with one; see the
[fuzz workflow](.github/workflows/fuzz.yml) for how targets are discovered.

## What a pull request needs

- **One concern.** A fix and a refactor are two pull requests.
- **A test that can fail.** For a fix, the test must fail before the change and
  pass after. Say in the pull request what the failure looked like; a test that
  cannot fail is not evidence that anything works.
- **No unrelated edits.** Do not reformat files you did not otherwise touch, and
  do not fold a dependency bump into a change that does something else.
- **Green CI**, including the platform matrix.
- **An honest handoff.** State what was checked and what was not. An unchecked
  claim is worse than a missing one.

## Commits

Prefix the subject with the area and use the imperative mood:

```
fix(p2p): time out idle fs streams, not slow transfers
feat(daemon): expose transfer history over the API
docs: use the release tag instead of a pinned archive name
test: cover identity refusal on a corrupt key
chore(deps): bump go-libp2p
```

The body explains why the change exists. The diff already shows what changed.

## Dependencies

Dependabot opens grouped minor and patch updates weekly. Major versions arrive
as separate pull requests, because a breaking change needs its own review and
its own CI run.

## Security

Read [SECURITY.md](SECURITY.md) before writing about a weakness. Do not paste
share codes, peer IDs, identity keys, or private paths into an issue, a pull
request, or a screenshot. Redact them the same way you would redact a password.

## Licence

By contributing, you agree that your contribution is licensed under the MIT
licence in [LICENSE](LICENSE).
