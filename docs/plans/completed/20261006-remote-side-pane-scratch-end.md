# Remote-aware side pane (⌘D), scratch (⌘J), and `agr end`

## Overview

A row opened with `agr open <host> a1` holds a remote agent, but agterm's own panes around it
are local: ⌘D splits to a local shell, ⌘J opens a local scratch. Reaching the agent's
directory means typing `agr open` or `ssh … cd …` by hand, and ending the work means
`agr kill <host> a1 a1-2` with every name spelled out.

This plan makes agterm's **standard keys** follow the row's remote host, through agterm event
hooks rather than rebinding, so local rows behave exactly as today:

- **⌘D** (`pane.split` hook → `agr on-split`): a fresh split pane on a bound row opens
  `agr open <host> <name>-2` — a persistent agr session, tagged as a child of `<name>`,
  started in `<name>`'s current remote directory.
- **⌘J** (`pane.scratch` hook → `agr on-scratch`): a fresh scratch on a bound row runs
  `agr shell <host> --cwd <dir>` — a plain ssh/mosh login shell in that directory, no
  multiplexer, so it dies with the pane and needs no cleanup.
- **`ctrl+a>x`** (keymap custom command → `agr end <row>`): after a confirm dialog, kills the
  row's sessions and their children on the remote, drops the bindings, closes the row.
  `agr kill <host> a1` cascades to `a1`'s children the same way.

Closing a row (⌘W) stays non-destructive — it is also how a row is moved to another Mac.

Replaces `docs/backlog/split-pane-follows-the-remote-host.md` (its chord is now a hook; its
blocker, per-pane bindings, shipped in #8). Drops the old `agr quick` idea for good: the
quick terminal is one app-global window with no hook event and no readable foreground, so a
remote shell there would be typed blind and follow no row; ⌘J covers the need per row.

## Context (from discovery)

- Repo layout: thin `cmd/agr` (`run.go` dispatch table, `wire.go` wiring), Mac verbs in
  `internal/cli`, agtermctl adapter `internal/agterm/ctl.go`, remote runner
  `internal/remote/runner.go` (`Data(ctx, host, verb, args…)` checks the `agr\t<version>`
  header), embedded remote script `internal/remotescript/agr.sh` (verbs
  `attach sessions reap status`), bindings `internal/bindings/store.go`
  (`ForRow`, `UnbindRow`, per-pane `PaneID`). Consumer-defined interfaces in
  `internal/cli/dependency.go`, mocks via `go generate`. Commits `type: subject`, plain
  branch names, one change per PR.
- **agterm 0.27 facts (probed 2026-10-06 on this Mac):**
  - hook kinds include `pane.split` and `pane.scratch`; payload `{"status":"shown"|"hidden"}`,
    env `AGT_SESSION_ID`, `AGT_EVENT_STATUS`, `AGT_SOCKET`; the hook runs `/bin/sh -c` with the
    GUI PATH, detached, serialized per line. **`shown` fires on create and on re-show alike** —
    the event cannot tell a fresh pane from a re-shown one.
  - `tree --json` session: `surfaces[]` of `{kind: left|right|scratch, paneID, visible,
    active}`; a hidden scratch stays listed with `visible:false`; a scratch whose command
    exits disappears from the tree.
  - `session restart` is refused outside Live sessions mode (this Mac is not in it), so a
    command is started by **typing** into the pane: `session type --stdin --target <row>
    --pane-id <id>` with a trailing newline executes it (verified on a fresh split pane).
  - `session text --pane-id <id>` reads a pane's screen (used to wait for the prompt).
  - `ask open <title> --message … --button id=Label … --destructive id --target <row>` blocks
    and prints a JSON result; cancel prints `{"result":"cancelled"}` exit 2. The answered
    shape is not yet probed (needs a click) — decode tolerantly, verify in Post-Completion.
  - `session close --target <row>` closes a row.
- tmux: `display-message -p -t "=$n:" '#{pane_current_path}'` gives the active pane's cwd;
  `new-session -c <dir>` sets a new session's start dir — **`-c` is format-expanded**, so `#`
  is doubled (tmux 3.7c: `/d/a#{session_name}` otherwise lands in `$HOME`). zmx: `zmx list`
  exposes the session owner's `pid`; the shell is its first child, whose cwd is
  `/proc/<child>/cwd` on Linux only (empty elsewhere → `$HOME`); a new zmx session starts in
  the caller's cwd, so `cd` before `zmx attach`.
- A hidden split pane and a hidden scratch both stay in `tree` (probed), so pruning the
  handled set to the tree's pane ids never drops a pane that can be re-shown. Pane ids are
  UUIDs and assumed never reused. The prune runs only after a successful tree decode.
- **Typed lines are an injection surface**: a remote directory is any byte string, and
  quoting stops the shell, not the line editor (`^C`, CR, ESC). `Runner.Cwd` returns `""`
  for a control byte (C0, DEL, C1) or invalid UTF-8, and the hook refuses to type any line
  that is not printable.
- `--parent` is written on every attach, so re-attaching `<name>-2` from the side pane
  re-parents it to the current row's session — intended.
- Compatibility: an un-upgraded remote rejects unknown verbs and extra `attach` args
  (`[ "$#" -eq 1 ] || usage 2`). `on-split` therefore only passes `--cwd/--parent` when the
  `cwd` verb answered; otherwise it types the plain `agr open <host> <name>-2`.

## Development Approach

- **testing approach**: Regular (code first, then tests), Go unit tests with gomock + the
  POSIX remote tests in `tests/remote/` (`make check-remote`).
- every task: `make test lint shellcheck check-remote` green before the next.
- one PR per task, stacked: `remote-side-pane` → `remote-scratch` → `agr-end`.
- **CRITICAL: update this plan file when scope changes during implementation**

## Testing Strategy

- **unit**: Go tests for each new verb against mocked agterm/remote/store seams; tree and
  ask decoding against canned JSON.
- **remote**: shell tests for `cwd`, `attach --cwd/--parent`, `reap` cascade on the tmux
  test server and the zmx fake.
- **e2e**: manual on homelab inside agterm (Post-Completion).

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix

## Implementation Steps

### Task 1: Side pane follows the remote host (`cwd`, `attach --cwd/--parent`, `on-split`)

Plan review 2026-10-06 applied: control-byte filter, `#` escaping, `cwd` error vs empty
answer (error → no flags, empty → `--parent` only), stable-prompt wait with `cwd` fetched
concurrently, `AGT_SOCKET` fallback. Kept: typing after the prompt-wait timeout (the pane is
fresh, so its only program is the starting shell; aborting would make slow shells silently
lose the feature).

**Files:**
- Modify: `internal/remotescript/agr.sh`, `internal/remote/runner.go`, `internal/agterm/ctl.go`,
  `internal/cli/open.go`, `internal/cli/dependency.go`, `cmd/agr/run.go`, `cmd/agr/wire.go`,
  `README.md`
- Create: `internal/cli/onsplit.go`, `internal/cli/handled.go`, tests beside each,
  `tests/remote/{tmux,zmx}/cwd_test.sh`
- Delete: `docs/backlog/split-pane-follows-the-remote-host.md`

- [x] remote `cwd <name>`: header, then the session's cwd or an empty line; tmux via
      `display-message`, zmx via the listed pid's `/proc/<pid>/cwd`; never fails for a
      missing session (empty line)
- [x] remote `attach <name> [--cwd <dir>] [--parent <name>]`: dir must be absolute, ignored
      when `cd` fails; tmux `new-session -c` (new sessions only) + `@agr_parent`; zmx `cd`
      before attach + `agr_parent` label; parent through `valid_token`
- [x] runner `Cwd(ctx, host, name) (dir string, err error)`; empty/non-absolute → `""`
- [x] `agr open <host> [name] [--cwd DIR] [--parent NAME]` → `OpenOptions`; passed to the
      remote `attach` argv (ssh quoted, mosh unquoted)
- [x] agterm `Ctl.Session(ctx, row)` decoding surfaces (kind, paneID, visible); `Ctl.Text`
      and `Ctl.Type` by pane id
- [x] handled-pane set at `dirs.Cache/handled-panes.json` (flocked, pruned to pane ids still
      in the tree) so a re-shown pane is never typed into twice
- [x] `agr on-split`: only `shown`; find the split surface; skip if handled (mark it), if the
      primary pane has no binding, or if the split pane is already bound; `Cwd` best effort;
      wait ≤5s for a non-blank screen; type `<abs agr> open <host> <name>-2 --cwd '<dir>'
      --parent <name>` (shell-quoted; flags omitted when `cwd` failed)
- [x] dispatch `on-split` (not in `--help`: a hook entry point), wire deps
- [x] tests: remote cwd/attach (tmux + zmx fake); Go tests for every skip branch, quoting
      (dir with space and `'`), old-remote fallback, handled-set prune
- [x] README: hooks.conf snippet, behaviour, `agr install` needed for `--cwd`
- [x] `make test lint shellcheck check-remote` green

### Task 2: Scratch follows the remote host (`agr shell`, `on-scratch`)

Review: type `exec <agr> shell …`, so leaving the remote shell closes the scratch and the
next ⌘J goes remote again; an unreachable host waits for Enter so the error is readable.

**Files:**
- Create: `internal/cli/shell.go`, `internal/cli/onscratch.go`, tests
- Modify: `cmd/agr/run.go`, `README.md`

- [x] `agr shell <host> [--cwd DIR]`: interactive ssh (mosh when the host has it) running
      `sh -c 'cd "$1" 2>/dev/null; exec "${SHELL:-sh}" -l' sh <dir>`; no binding, no bridge
- [x] `agr on-scratch`: only `shown`; scratch surface; handled-set skip; binding = the row's
      primary-pane binding, else none → skip; `Cwd` best effort; wait for prompt; type
      `<abs agr> shell <host> --cwd '<dir>'`
- [x] tests: skip branches, typed line, shell argv for ssh and mosh
- [x] README: second hooks.conf line
- [x] `make test lint shellcheck check-remote` green

### Task 3: `agr end` and cascading `kill`

Review: a missing session exits **3** on both muxes (zmx checks existence first), mapped to
`remote.ErrNoSession`; `end` treats only that as done, so an old remote keeps the row. The
confirm proceeds only on exit 0 **and** the decoded button equal to the confirm id; the
answered shape is probed with one click before the decoder is written.

**Files:**
- Modify: `internal/remotescript/agr.sh`, `internal/agterm/ctl.go`, `cmd/agr/run.go`,
  `internal/cli/dependency.go`, `README.md`
- Create: `internal/cli/end.go`, tests, `tests/remote/{tmux,zmx}/reap_children_test.sh`

- [x] remote `reap <name>` also reaps owned sessions whose parent is `<name>` (one
      `killed <x>` line each); a missing parent with live children still reaps the children
- [x] agterm `Ctl.Ask(ctx, row, title, message, buttons, destructive) (string, error)` and
      `Ctl.Close(ctx, row)`
- [x] `agr end <row>`: bindings `ForRow`; none → error "not an agr row"; confirm
      ("End a1 on homelab? Kills the agent and its side-pane sessions."); per distinct
      host+name `Reap` ("no session" counts as done); any other failure → stop, keep the row;
      then `UnbindRow`, `session close`
- [x] tests: no binding, cancel, reap failure keeps the row, success order
- [x] README: keymap line (`--error-hud`), lifecycle table (⌘W detaches, `end` kills)
- [x] `make test lint shellcheck check-remote` green

## Post-Completion

- homelab in agterm: ⌘D on a bound row → `a1-2` in a1's dir; hide/show → no retype;
  ⌘J → remote shell in the dir; ⌘J hide/show → same shell; ctrl+a>x → dialog → both
  sessions gone, row closed; a local row → all three keys unchanged.
- confirm the `ask` answered-result shape and tighten the decoder.
- local cleanup (outside the repo): remove the dangling `~/.local/bin/agr` symlink and the
  commented `agr quick` keymap line; add the two hook lines and the `ctrl+a>x` keymap line.
