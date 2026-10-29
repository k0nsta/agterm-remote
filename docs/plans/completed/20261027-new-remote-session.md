# New remote session command (`agr new`, `ctrl+a>n`)

## Overview

`agr setup` wires two agterm keymap commands, End remote session (`ctrl+a>x`, `agr end`) and
Paste to remote (`ctrl+a>v`, `agr paste`). Opening remote work still means a new row, then
typing `agr open <host>` by hand. This adds the third command:

- **`ctrl+a>n`** (keymap custom command → `agr new "{AGT_SESSION_ID}"`): pick a host, pick or
  name a session on it, pick the workspace (or name a new one), and get a new row there
  running `agr open <host> <name>`.

Example: on row `a1` (myhost, workspace `myhost - tools`) press `ctrl+a>n` → host picker
(`myhost` first) → Enter → session picker on myhost → type `a2` → workspace picker
(`myhost - tools` first, then `local`) → Enter → new row right after `a1` named `a2`, its
shell runs `'/opt/homebrew/bin/agr' open myhost a2`, which binds the row, starts the bridge
and attaches. Typing `staging` at the workspace picker creates (or reuses) a workspace of that
name and puts the row there.

## Context (from discovery)

- A keymap command runs detached with the GUI PATH and no TTY, while `agr open` needs a TTY
  (ssh/mosh interactive) and adopts `$AGTERM_SESSION_ID`. So `agr new` must create the row and
  run `agr open` inside it.
- Precedent for running a command in a fresh pane: `runInNewPane`/`waitForPrompt`
  (`internal/cli/onsplit.go`) wait for a stable prompt, then `TypeLine` the quoted, printable
  argv. Typing into a login shell keeps the user's PATH (Homebrew mosh) and leaves a readable
  local shell when the attach fails or detaches — the same as a hand-typed `agr open`.
  `session new --command` would run with the GUI PATH (no mosh) and close the row on error.
- agterm probes 2026-10-27:
  - `session new --name N --after ROW --json` prints `{"ok":true,"result":{"id":"…"}}`,
    selects the new row, and its primary surface (`kind:left`, `paneID`) is in `tree --json`
    at once. `--workspace ID` and `--workspace-name N --create-workspace` place it elsewhere;
    `--after` and the workspace flags are mutually exclusive.
  - `tree --json` workspaces carry `id`, `name`, `active`, `sessions[]`. Names are not unique,
    so picker items use the id.
  - the picker runs Return on the first supplied item when the query is empty, so "current
    first" needs no `--select`.
  - `ctrl+a>n` is bound neither in the live keymap nor among agterm's built-ins
    (`agtermctl keymap list`).
- All three pickers run before the row is created, so a cancel at any leaves nothing behind.
  The session picker is `chooseOpenName` (`internal/cli/open.go`), reused as is; its
  `Remote.Sessions` ssh call has no deadline (an offline host hangs ~75 s), so `New` bounds
  that call alone, not the pickers.
- Known hosts: `dirs.Cache/hosts/<FileKey(host)>.json` (written by install and the agr-path
  probe) and the bindings' `host` field. `FileKey` is lossy (`user@h` → `user_h-<hash>`), so
  `SaveHostInfo` starts recording `host` in the file; an older file yields its host only when
  `FileKey(prefix)` round-trips. The picker's custom entry covers anything else.
- Setup's chord choice for Paste (`pasteKey`: flag → existing chord → default when free and
  no command yet) is the shape for New, with `ctrl+a>n` as default. `pasteKey` only checks
  `occupied[0]`; it must check every chord requested this run.
- A failure after the row exists (no primary pane, typing failed) closes that row, so no
  empty row is left behind; the error then reaches the `--error-hud` of the original row.

## Development Approach

- **testing approach**: Regular (code first, then tests), Go unit tests with gomock.
- every task: `make test lint` green before the next; one PR for all three.
- test helpers take `*testing.T` first and call `t.Helper()`.
- **CRITICAL: update this plan file when scope changes during implementation**

## Testing Strategy

- **unit**: `New` against mocked picker/remote/store/rows/panes; `CachedHosts` on a temp cache
  dir; `Ctl.NewRow`/`Ctl.Workspaces` against the fake runner; setup keymap wiring and
  `--new-key` parsing.
- **e2e**: manual in agterm (Post-Completion).

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix

## Implementation Steps

### Task 1: Host and workspace sources, row creation

**Files:**
- Modify: `internal/remote/hostinfo.go`, `internal/agterm/ctl.go`, tests beside each

- [x] `HostInfo.Host` (`json:"host,omitempty"`), set by `SaveHostInfo`
- [x] `remote.CachedHosts(dirs) ([]string, error)`: `host` from each file, else the file name's
      prefix when it round-trips through `FileKey`; missing dir → none (`hostinfo_test.go`)
- [x] `Ctl.Workspaces(ctx) ([]Workspace, error)`: id, name, session ids from `tree --json`
- [x] `Ctl.NewRow(ctx, NewRow{Name, After, Workspace, WorkspaceName})`: `session new --name …`
      plus `--after`, `--workspace` or `--workspace-name --create-workspace`; decode
      `result.id`; selects the row (no `--no-select`) (`ctl_test.go`)
- [x] `make test lint` green

### Task 2: `agr new`

**Files:**
- Create: `internal/cli/new.go`, `internal/cli/new_test.go`
- Modify: `internal/cli/dependency.go`, `internal/cli/onsplit.go`, `cmd/agr/run.go`,
  `cmd/agr/wire.go`, `cmd/agr/run_test.go`

- [x] `Workspaces` and `RowOpener` interfaces in `dependency.go`, mocks regenerated
- [x] ➕ `waitForPrompt` kept as is: `New` passes it a `PaneHookDependencies` with its panes and
      timing (narrowing it would break the hook tests that set `PromptWait` by name)
- [x] `New(ctx, row, deps)`: hosts = cached ∪ bound, sorted, the row's own host first; host
      picker (custom allowed, `ValidHost`); `chooseOpenName` with `Sessions` bounded by a
      timeout; workspace picker, the row's workspace first (custom = new workspace name,
      printable, non-blank); `NewRow` after the row when its workspace is kept, else in the
      chosen one; primary pane from `Surfaces`; `waitForPrompt`; type
      `<abs agr> open <host> <name>` (quoted, printable); after-create failure → `CloseRow`
- [x] ➕ codex review: pin the flow to the window it fired in (`Ctl.InWindow`, `--window
      $AGT_WINDOW_ID` on tree, pick, `session new/type/text/close`), so switching windows during
      the ssh lookup cannot move the pickers or the row
- [x] `RunNew`: a cancel exits 0 (no error HUD), like `RunEnd`
- [x] run.go `new [row]`: row from the argument or `AGT_SESSION_ID`/`AGTERM_SESSION_ID`,
      optional; help line; wire.go deps
- [x] tests: host order and dedupe, cancel at each picker creates no row, kept vs other vs new
      workspace flags, typed line quoting, invalid host/name/workspace, no primary pane and
      type failure close the row, offline host times out
- [x] `make test lint` green

### Task 3: Setup wiring and docs

**Files:**
- Modify: `internal/cli/setup.go`, `internal/cli/setup_test.go`, `cmd/agr/run.go`,
  `cmd/agr/wire.go`, `cmd/agr/run_test.go`, `README.md`

- [x] `defaultedKey` shared by Paste and New, checking every chord requested this run (End,
      Paste, New) as occupied
- [x] `NewKey` / `--new-key`: `command "New remote session" ctrl+a>n --error-hud <agr> new
      "{AGT_SESSION_ID}"`, same repoint/dedupe as End/Paste; no-key hint; reload message
- [x] tests: default added, default taken by another chord, `--new-key` equal to Paste's
      default, existing chord kept, repeated flag rejected
- [x] README: `agr new`, keymap line, `--new-key`
- [x] `make test lint` green

## Post-Completion

- in agterm: `agr setup` adds the line; `ctrl+a>n` → host → session → workspace → new row
  attaches; cancel at each picker → no row; a new workspace name → created; an unreachable
  host → error HUD after the timeout, no row.
- a keymap kept outside this repo needs the same line beside End/Paste:
  `command "New remote session" ctrl+a>n --error-hud '/opt/homebrew/bin/agr' new "{AGT_SESSION_ID}"`.
