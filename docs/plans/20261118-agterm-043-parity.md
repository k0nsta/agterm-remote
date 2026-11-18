# Remote agents get agterm 0.39–0.43 features

## Overview

agterm added several features in 0.39–0.43 that a Claude in a remote row cannot use through agr yet, and its own
Claude hook got two status rules that agr's hooks lack. This plan closes both gaps, in two PRs:

- **PR A, hook parity (Tasks 1–2), after PR #34:** a remote Claude row stays active while background subagents,
  workflows or `run_in_background` shells run, and a subagent's tool activity no longer overwrites a blocked prompt.
- **PR B, proxy (Tasks 3–7):** a host may call `session info`, `session tint`, `session new --parent` (a companion
  of a row it owns), and `session overlay open --markdown|--text|--image FILE` with a file on the host.

Scope follows agr's role next to agterm-server (decided 2026-11-18): agr is the complement, the bridge for your own
tmux on any ssh host, not a full alternative. It takes only the agterm features a remote tmux agent uses. The pane
mailbox (`msg`) is left out: agents on one host already talk through the host.

Example: in a remote row, Claude runs `agtermctl session overlay open --markdown notes.md --target "$AGTERM_SESSION_ID"
--pane "$AGTERM_PANE"`. The host wrapper ships `notes.md` as `@0.md`, the daemon writes it to a temp file, and agterm
shows the rendered page. Today the call is refused.

## Context (from discovery)

- agterm versions: the Mac runs 0.42.3 (`defaults read /Applications/agterm.app/Contents/Info.plist
  CFBundleShortVersionString`). Release notes: `gh release view v0.39.0…v0.43.0 -R umputun/agterm`.
- Allowlist: `internal/proxy/policy.go:130`. Scopes at `policy.go:29-40`. Refused leaves with reasons at
  `policy.go:193-232`. None of `session info`, `session tint`, `msg` is listed (`grep -n 'words("' policy.go`).
- Authorization helpers: `mapRow` (full row id or the self marker; prefixes and `active` refused,
  `policy.go:834-848`), `callerScope` (`policy.go:930`), `checkOwned` (the pane is bound **and** attached, not a
  local shell after a detach, `policy.go:956`, `attached` at `policy.go:510-528`), `wholeRowRefusal`
  (`policy.go:971`). `leaf.check` sees only `parsedArgs`, no tree (`policy.go:707-710`).
- `session new` is `scopeNone`: `run` allows it after `normalise`, before `load` reads the tree
  (`policy.go:674-676`). A `roleRow` value goes only into `a.rows` and is rendered as `--target=`
  (`policy.go:718-719,426`). The leaf allows `name`, `cwd`, `workspace`, `no-select`, `wait`, `command`
  (`policy.go:173-175`); `rewriteNew` turns it into a row running `agr open <host> <name>` (`internal/proxy/rewrite.go`).
- `session overlay open` takes only `--html` (body file, `roleFile`) and `--url` (`policy.go:160-166`);
  `checkOverlayOpen` at `policy.go:276`; `--navigation` is in `refusedOptions` (`internal/proxy/argv.go:147-151`).
  `sizePage` adds the default `--size-percent` to a whole-row page with files (`rewrite.go:128-149`).
- Host files: the host wrapper's `FILE_OPTIONS = ("--html",)` (`internal/remotescript/agr.sh`, the `ctl` python,
  line 797 on master) replaces the path with `@<i>` and ships the bytes. `fileIndex` accepts only `@<i>`
  (`policy.go:773`). The daemon writes every file as `<i>.html` (`rewrite.go:188-207`) and sends any call with
  files to `rewriteHTML`, the page shim (`rewrite.go:104-110`). Limits: 8 files, 8 MiB (`internal/proxy/wire.go:18-20`).
- Temp dirs: a non-page call's `Prepared.Dir` is not removed after the call; `sweepTemp` removes dirs older than
  24 h and `clearTemp` all of them at daemon start (`internal/daemon/proxy.go:32,102-123`). The shim already
  watches an open page's slot through the tree (`internal/daemon/pageshim.go`, `wait`, `current`).
- agterm pages (release notes): a markdown page "loads nothing from the network, runs no script from the file, and
  turns every link other than http(s) and `#anchor` into plain text"; pictures come from the file's own folder
  (v0.40.0 #866). Text pages are read-only (v0.41.0 #874). An image panel fits the picture without
  `--size-percent` (`agtermctl session overlay open -h`).
- agtermctl 0.42.3 help: `session info [TEXT] [--clear] [--markdown] [--hide-after S]`, 512 bytes;
  `session tint VALUE [--on P]`; `session new --parent SID --tint C --tint-on P`;

- Hooks: agr installs `agr status active --blink` (UserPromptSubmit, PostToolUse), `blocked` (Notification),
  `completed --auto-reset` (Stop), `agr agent start|end` (`internal/remote/hooks.go:54-60`). The host relays only
  `--blink` and `--auto-reset` (`agr.sh`, `relay`); the receiver keeps only those (`internal/receiver/event.go:54-55`,
  `listener.go:322-332`); `agterm.StatusArgs` has `blink`, `autoReset` (`internal/agterm/protocol.go:18-24`), with an
  agtermctl fallback (`internal/agterm/client.go:120-150`). Resync replays the host state, skipping a `completed`
  already seen (`internal/daemon/resync.go:120-126`).
- agterm's hook rules: `docs/plans/completed/20261110-subagent-status-owner.md` in umputun/agterm (0.42.0 #959):
  a subagent event (carries `agent_id`) posting `active` gets `--if-idle` (`ifIdle` in JSON; ok no-op on a row that
  is not idle; an older app drops the field); a `Stop` posting `completed` posts `active --blink` while
  `background_tasks` lists a running `subagent` or `workflow`. Every background completion re-invokes the main
  thread with `UserPromptSubmit` then `Stop`, so the final `completed` comes from that last `Stop`. Commit 2b66c171
  (0.43.0) adds type `shell`; the 0.42.3 script on this Mac still lists only `subagent | workflow`.
- PR #34 (open, branch `fix-background-claude-status`) adds `read_hook_input`/`hook_field` and rewrites
  `tmux_status`/`zmx_status` in `agr.sh`; PR A builds on it. `python3` is a host requirement (README "Requirements").

## Solution Overview

- **PR A:** the host applies agterm's two rules before it sets and relays a state, reading `agent_id` and
  `background_tasks` with `python3`; the Mac carries `--if-idle` through as `ifIdle`.
- **PR B:** each new verb gets an explicit authorization path that loads the tree and checks ownership with the
  existing helpers:
  - `session info`, `session tint`: `scopeRow` (every pane the host's).
  - `session new --parent ROW`: a new check after `load`: `mapRow(ROW)` and `wholeRowRefusal == ""`, rendered as
    `--parent=<full row id>`.
  - `--markdown|--text|--image`: body files without the page shim. Per agterm's notes these pages run no script, and
    a markdown page's non-web links are plain text; its pictures are absent, since only the one file is shipped.
- **Body file extension:** the host sends `@<i>.<ext>` for the new options; `--html` keeps `@<i>`, so a new host
  still works with an older daemon.
- Rejected: running agterm's `agterm-claude-status.sh` on the host. It routes by the row's inherited
  `AGTERM_SESSION_ID`, which a background Claude carries wrongly (PR #34), and it needs agterm's scripts on the host.

## Technical Details

- **`--if-idle` (PR A):** host `relay` passes `"--if-idle"`; `receiver.Event.IfIdle`;
  `agterm.StatusArgs.IfIdle *bool json:"ifIdle,omitempty"`. The agtermctl fallback appends `--if-idle` and, when that
  call fails, retries once without it (an older agtermctl rejects the flag).
- **Host state under `--if-idle`:** written only when the host state is empty or `idle@…`. Over `completed@…`,
  `active@…` or `blocked@…` the host state stays, so resync never replays an `active` agterm skipped. Trade-off: a
  subagent that wakes an idle-reset row on the Mac leaves the host at `completed`, so a reconnect shows the row
  finished until the next main-thread event.
- **Stop rewrite:** `hook_event_name` `Stop`, state `completed`, and a running `subagent|workflow|shell` → state
  `active`, args `--blink` only. `shell` follows 0.43.0 (2b66c171); on 0.42.3 it only keeps the row active longer.
- **python3:** started only when the hook JSON contains `"agent_id"` or `"background_tasks"`; it prints `subagent`
  and/or `running`. No python3, or any error → argv as today.
- **File refs:** the daemon's `fileIndex` accepts `@<i>` or `@<i>.<ext>` with ext matching `^[a-z0-9]{1,12}$`;
  anything else is refused before any path is built. Allowed ext per option: `--html` any (always written
  `<i>.html`, as today), `--markdown` `md|markdown`, `--image` `png|jpg|jpeg|gif|webp`, `--text` any (the host sends
  none for a name without one; the daemon then writes `.txt`). `--markdown /path` from an older host fails
  `fileIndex` with "run `agr install <host>`".
- **Temp lifetime:** a non-html page's dir is kept while the overlay it opened is in its slot, watched like a shim
  page (tree poll), and removed when it closes or is replaced; the 24 h sweep stays as the backstop. Per host, at
  most 8 such dirs live; opening a ninth removes the oldest. A swept page keeps what agterm already rendered; a
  rebuild after that shows agterm's missing-file state.
- **Overlay check:** exactly one of a command, `--html`, `--url`, `--image`, `--markdown`, `--text`; `--line` and
  `--tail` only with `--text`; `--follow-file` dropped with a warning (the page shows a copy); `--navigation` stays
  refused; `--js` only with `--html` (as today). `sizePage` skips `--image`, so agterm fits the picture.

## Development Approach

- **testing approach**: Regular (code first, then tests), Go unit tests and the remote shell tests.
- every task: `make test lint` green before the next. PR A after PR #34 merges, PR B independent.
- test helpers take `*testing.T` first and call `t.Helper()`.
- the repo is public: fixtures use `myhost`, `<host>`, `/home/me`.
- **CRITICAL: update this plan file when scope changes during implementation**

## Testing Strategy

- **unit**: `policy_test.go`, `argv_test.go`, `rewrite_test.go`, `internal/daemon/proxy_test.go`,
  `internal/daemon/resync_test.go`, `event_test.go`, `listener_test.go`, `client_test.go`.
- **remote shell**: `tests/remote/{tmux,zmx}/status_test.sh`, `tests/remote/tmux/proxy_test.sh`, run by `make check`.
- **live**: `tests/live/proxy_live.sh` on a real host (Post-Completion).

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix

## Implementation Steps

### Task 1 (PR A): `--if-idle` from host to agterm

**Files:**
- Modify: `internal/receiver/event.go`, `internal/receiver/listener.go`, `internal/agterm/protocol.go`,
  `internal/agterm/client.go`, tests beside each

- [ ] `Event.IfIdle` from `--if-idle`; `statusArgs` sets `StatusArgs.IfIdle`
- [ ] fallback appends `--if-idle`, retries once without it on failure
- [ ] tests: decode with and without the flag, request JSON carries `ifIdle`, fallback argv, fallback retry
- [ ] `make test lint` green

### Task 2 (PR A, after #34): Claude hook rules on the host

**Files:**
- Modify: `internal/remotescript/agr.sh`, `tests/remote/tmux/status_test.sh`, `tests/remote/zmx/status_test.sh`,
  `internal/daemon/resync_test.go`

- [ ] ⚠️ blocked until PR #34 merges; branch from master after it
- [ ] `hook_kind`: python3 over `hook_input`, only when it holds `"agent_id"` or `"background_tasks"`
- [ ] subagent `active` → relay with `--if-idle`; host state written only when empty or `idle@…`
- [ ] `Stop` + `completed` + a running `subagent|workflow|shell` → state `active`, args `--blink`
- [ ] `relay` passes `--if-idle`
- [ ] tests: subagent event over `blocked`, `completed`, `active` and empty; Stop with a running shell, with none;
      no python3 on PATH → argv as today; resync after a skipped subagent event does not replay `active`
- [ ] `make test lint` green

### Task 3 (PR B): `session info`, `session tint`

**Files:**
- Modify: `internal/proxy/policy.go`, `internal/proxy/policy_test.go`

- [ ] `session info` (`clear`, `markdown`, `hide-after`, `--target`), `session tint` (`on`, `--target`); `scopeRow`,
      write
- [ ] tests: own row allowed, a local row refused, a split row with one local pane refused, positional limits
- [ ] `make test lint` green

### Task 4 (PR B): companions

**Files:**
- Modify: `internal/proxy/policy.go`, `internal/proxy/argv.go`, `internal/proxy/rewrite.go`, tests beside each

- [ ] `session new`: `--parent` (new role, not `roleRow`), `--tint`, `--tint-on`
- [ ] after `load`: `mapRow(parent)`, `wholeRowRefusal == ""`; the call stays `scopeNone` otherwise
- [ ] `rewriteNew` emits `--parent=<full row id>`
- [ ] tests: own row and another owned row allowed, a local row refused, a prefix refused, rendered `--parent=`
- [ ] `make test lint` green

### Task 5 (PR B): body file extensions

**Files:**
- Modify: `internal/remotescript/agr.sh`, `internal/proxy/policy.go`, `internal/proxy/rewrite.go`, tests beside each

- [ ] `fileIndex` parses an optional ext with the daemon's own check; `FileRef.Ext`
- [ ] `writeFiles` names `<i>.<ext>` (html always `<i>.html`, text without ext `<i>.txt`)
- [ ] host `ctl`: `@<i>.<ext>` for the new options only
- [ ] tests: `@0./x`, `@0.A`, a 13-char ext refused; `--html foo.htm` and `--html` without ext still go to the shim
- [ ] `make test lint` green

### Task 6 (PR B): markdown, text and image pages

**Files:**
- Modify: `internal/remotescript/agr.sh`, `internal/proxy/policy.go`, `internal/proxy/rewrite.go`,
  `internal/daemon/proxy.go`, `tests/remote/tmux/proxy_test.sh`, Go tests beside each

- [ ] host `FILE_OPTIONS` += `--markdown`, `--text`, `--image`
- [ ] leaf options and `checkOverlayOpen` as in Technical Details; per-option ext check
- [ ] non-html file calls bypass `rewriteHTML` and pass the temp path; `sizePage` skips `--image`
- [ ] the daemon keeps the dir while the overlay is in its slot, removes it after; per-host cap of 8
- [ ] tests: each kind written with its ext; a `.exe` image refused; two sources refused; `--line` without `--text`
      refused; dir removed on close and at the cap; the shell test ships `@0.md`
- [ ] `make test lint` green

### Task 7 (PR B): docs

**Files:**
- Modify: `README.md`, `internal/skill/SKILL.md`

- [ ] README "Remote agterm control": the new verbs and their limits (own rows only, one file per page)
- [ ] skill: `session info` for progress, `session tint`, companions, `--markdown`/`--text`/`--image`
- [ ] review README, the skill and `CLAUDE.md` (untracked in the main checkout) for stale statements
- [ ] `make check` green

## Acceptance Criteria

- Run: `make check`
- Run: `go test ./internal/proxy/ ./internal/receiver/ ./internal/agterm/ ./internal/daemon/`
- `grep -n 'words("session info")' internal/proxy/policy.go` finds the leaf.
- `grep -n 'if-idle' internal/remotescript/agr.sh` finds the relay flag (PR A).

## Post-Completion

- on a real host (`tests/live/proxy_live.sh`, and by hand in a remote row): `session info` shows under the row;
  `--markdown`, `--text --line 10` and `--image` open; a companion nests under its row; a Claude Stop with a background subagent keeps the row active until it ends.
- release notes: hosts need `agr install <host>` for the new file options and hook rules; `--if-idle` needs agterm
  0.42 on the Mac (older writes unconditionally); `shell` tasks need agterm 0.43's semantics to match.
