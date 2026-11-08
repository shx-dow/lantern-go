## What and why

<!-- What changed, and what was wrong or missing before. One concern per pull
request: a fix and a refactor are two. -->

## Negative control

<!-- For a fix: the test or command that failed before this change and passes
after it. Say what the failure looked like. Delete this section if the change
is not a fix. -->

## Verification

- [ ] `gofmt -l .` prints nothing
- [ ] `go vet ./...` is clean
- [ ] `go test -race ./...` passes
- [ ] `git diff --check` is clean

<!-- Also list anything you did not check. -->

## Impact

<!-- Docs, wire format, daemon API, or release assets. "None" is a fine answer.
Mention if an upgrade order matters. -->
