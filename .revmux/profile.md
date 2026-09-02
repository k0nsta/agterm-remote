# Review profile — agterm-remote (`agr`)

## What this is

One bash script, `agr`, installed on **both** sides of an SSH link. On the Mac it drives
agterm (`open up down install quick doctor ls kill`); on the remote host it is invoked
over SSH and by Claude Code hooks (`attach status relay sessions reap`). It bridges
agent-status events from a remote tmux session back to the Mac's agterm control socket
over a reverse-forwarded Unix socket. Personal-infrastructure tool, single user, hosts
the user fully trusts (README "Security"). No tests, no CI; `shellcheck` is the gate.

## What a real failure looks like here

- **A hook that exits non-zero or blocks.** `agr status` runs on every `PostToolUse`;
  its contract is *always exit 0, never hang* (relay has a 0.3 s timeout). Anything that
  can make it exit non-zero, wait on the network, or print to stdout surfaces as an error
  on every tool call of a remote agent.
- **A remote command string built from unvalidated input.** Names and session ids are
  interpolated into `ssh host "…"`; `valid_token` + `printf %q` are the defence. A new
  path that bypasses either is a real finding.
- **A bridge that cannot be taken down or that spams the tty.** The fallback reconnect
  loop must own its `ssh`, log to a file, and die on `agr down`.
- **Touching a session `agr` did not create.** `ls`/`kill`/`reap` are scoped to sessions
  carrying the `@agr_target` tmux option. Anything that lists or kills beyond that is a
  scope violation, not a feature.
- **Destroying the remote `~/.claude/settings.json`** — `install` merges into the user's
  Claude Code config; it must be atomic, backed up, and refuse to touch a file it cannot parse.
- **Silent version drift** between the Mac and remote copies: the handshake header
  (`agr\t<version>`) and `doctor` exist so drift is *reported*, never auto-fixed.

## Blast radius

The forwarded socket is agterm's **full control API** (it can type into the user's
terminals). A bug on the remote side runs with that reach. The Mac side runs under
`set -euo pipefail` in bash **3.2** — a construct that only exists in bash 4+ is a
crash, not a style issue.

## Reporting bar

- `major`+: violates a contract above, breaks `set -e`/bash 3.2 on the Mac path, kills
  or lists a non-owned session, or a tmux/ssh invocation that cannot work as written
  (e.g. `set-option -t =name` without the trailing colon).
- `minor`: quoting or exit-status slips that are reachable but not on the hook path;
  README/usage drift from behaviour.
- **Do not report**: items already filed under `docs/backlog/` (deliberate omissions);
  the python-per-hook relay cost; absence of a test framework; `printf %q` producing
  bash-specific quoting for exotic characters that `valid_token` already rejects.
- Finding nothing is a valid answer.

## Deliberate conventions (do not flag)

- One script, two sides; Mac verbs are user-facing, remote verbs mechanical, paired
  1:1 (`open→attach`, `ls→sessions`, `kill→reap`).
- tmux option commands use `-t "=name:"` (colon), the others plain `-t "=name"` — both
  verified against tmux 3.7b; the difference is tmux's, not an inconsistency.
- `status` hook resolves its session via `-t "$TMUX_PANE"` and exits 0 when unset.
- `mkdir` directories as locks; `trap` for child cleanup — macOS has no `flock`/`setsid`.
- `$HOME` is passed to the remote as a *literal* inside a single-quoted constant
  (SC2016 is silenced on purpose); it is never passed as a `printf %q` argument.
- `@agr_target` holds the agterm session id or the literal `-`; empty/unset means "not ours".
- Script is sourceable (`if …; then main; fi`) so functions can be checked without a framework.
- Documentation is deliberately terse and example-first.
