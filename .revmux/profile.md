# Review profile — agterm-remote (`agr`)

## What this is

A Go binary (`cmd/agr`, `internal/*`, `CGO_ENABLED=0`, darwin-only release) that drives
agterm on the Mac (`open ls kill up down install daemon doctor shell end setup paste`)
plus a POSIX `sh` script (`internal/remotescript/agr.sh`, embedded and installed by
`agr install`) that runs on the remote host over SSH and from Claude Code hooks. A Mac
daemon keeps one reverse-forwarded Unix socket per host so remote hooks can push
agent status into agterm. Single user, hosts fully trusted (README "Security").
Gate: `make check` = `go test -race`, `golangci-lint`, `shellcheck -s sh`,
`tests/remote/run.sh` (sh tests against the remote script); CI runs it on ubuntu+macos.

## What a real failure looks like here

- **A hook that exits non-zero, blocks, or prints to stdout.** Remote `status`/`relay`
  run on every Claude Code tool call; contract: always exit 0, never hang.
- **A remote command built from unvalidated input.** Names/ids reach `ssh host …`;
  `valid_token` in `agr.sh` and the Go-side checks are the defence. A path that bypasses
  them, or a remote reply typed into a pane without validation, is a real finding.
- **Typing into the wrong pane or session.** The forwarded socket is agterm's full
  control API; `agtermctl session type/paste` into a pane agr does not own is serious.
- **Touching a remote session agr did not create** (ownership marker scoping).
- **Destroying user config**: remote `~/.claude/settings.json` (install merges), Mac
  `~/.config/agterm/{hooks,keymap}.conf` (`agr setup` edits). Must be atomic, keep
  unrelated lines, refuse what it cannot parse, be idempotent on re-run.
- **Silent Mac/remote version drift**: the `agr\t<version>` header is checked and
  reported, never auto-fixed; a new remote verb on an old remote must give the install hint.
- **Mac-side leaks**: temp files with clipboard/screenshot content left behind, or
  uploads unbounded in size/time.

## Reporting bar

- `major`+: a contract above broken; a POSIX-sh incompatibility in `agr.sh` (it runs
  under dash/busybox, not bash); a mocked test that passes while the real exec path
  (osascript, ssh, agtermctl argv) cannot work as written.
- `minor`: reachable error-message/exit-code slips off the hook path; README/usage drift.
- **Do not report**: items in `docs/backlog/`; parts that can only be verified by hand on
  a Mac (JXA against a real pasteboard) unless the code is demonstrably wrong; style.
- Finding nothing is a valid answer.

## Deliberate conventions (do not flag)

- Consumers define interfaces in `internal/cli/dependency.go`; mocks via
  `go.uber.org/mock` in `internal/cli/mocks`. Producers return concrete types.
- `cmd/agr/wire.go` wires real implementations; `internal/cli` holds the logic.
- Remote verbs are mechanical and paired with Mac verbs; the remote script stays POSIX
  `sh` with `valid_token`, `mktemp`+`mv` for atomic writes, `trap` cleanup.
- macOS has no `flock`/`setsid`: `mkdir` locks and `trap` are intentional.
- Commit subjects `type: subject`; docs terse and example-first.
