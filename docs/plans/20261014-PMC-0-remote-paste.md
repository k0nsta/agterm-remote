# Paste a Mac screenshot or file into a remote row (`agr paste`)

## Overview

On a local agterm row a screenshot reaches the agent in two ways: dragging it types its Mac
path, and Claude Code's ctrl+v reads the Mac clipboard. On a row opened with `agr open` the
agent runs on the remote host, so the Mac path does not exist there and ctrl+v reads the
remote's empty clipboard. agterm has no drop or paste hook, so agr cannot see a drag.

This plan adds a **paste-to-remote key**. `ctrl+a>v`, bound by `agr setup`, runs
`agr paste <row> <pane-id>`. agr takes the content, uploads it to the row's host over ssh,
and enters the remote path into the pane the key fired from. Claude Code on the remote then
attaches the image, as it does with a local drag (verified in Task 1).

Content, first match wins:

1. image data on the Mac clipboard (⌃⌘⇧4, "Copy Image"), written as PNG;
2. files copied in Finder (⌘C), possibly several, keeping their basenames;
3. the newest screenshot in the macOS screenshot folder, if it is under 5 minutes old.
   This Mac saves screenshots as files (`com.apple.screencapture target-screenshot = file`,
   default folder `~/Desktop`), so ⌘⇧4 → `ctrl+a>v` works without a settings change.

Nothing found, or a local row: an error that agterm shows through `--error-hud`.

Decided in brainstorm 2026-10-14. Rejected: a remote `xclip`/`wl-paste` shim, because the
remote could read the Mac clipboard at will, the bridge would need a request protocol, and
it depends on Claude Code's undocumented Linux paste. Also rejected: waiting for an agterm
drop hook, which has no timeline.

## Context (from discovery)

- `cmd/agr/run.go` dispatch, `cmd/agr/wire.go` wiring. Mac verbs live in `internal/cli`,
  consumer interfaces in `internal/cli/dependency.go`, mocks via `go generate`.
- `internal/bindings/store.go`: `Binding{Row, PaneID, Pane, Host, Name, Mux}`, `ForRow(row)`.
  A row can hold two bindings (primary and split), so the pane id picks the binding and
  falls back to the row's primary.
- `internal/remote/ssh.go`: `ExecSSH.Run(ctx, host, stdin, argv…)` carries stdin, so the
  upload is a runner verb with a body. `Runner.Data` checks the `agr\t<version>` header, so
  an old remote script gives the existing "run agr install" error.
- `internal/remotescript/agr.sh`: POSIX sh, `shellcheck -s sh`, verbs dispatched by name,
  `$HOME/.cache/agr` already used (`bridge.sock`). Remote tests in `tests/remote/{tmux,zmx}`.
- `internal/agterm/ctl.go`: `Ctl.TypeLine(row, paneID, text)` appends `\n`, which would
  submit the prompt. paste needs a variant without the newline.
- `internal/cli/setup.go`: `wireKeymap` / `endCommandLine` / `agrWord` / `endChord` /
  `dropNote` handle the End command idempotently (repoint path, keep chord, drop repeats).
  The paste command follows the same rules, so generalize these over the verb (`end`,
  `paste`) instead of copying them.
- Builds are `CGO_ENABLED=0`, darwin only. The clipboard is read through `osascript -l
  JavaScript` (NSPasteboard). CI also runs on ubuntu, so every darwin exec sits behind a
  consumer interface and the unit tests use mocks.
- Keymap custom commands receive `{AGT_SESSION_ID}` and `{AGT_PANE_ID}` (agterm keymap docs).
- `ctrl+a>v` is free in this Mac's keymap (`a b c d e g m p r s t x` are bound).

## Development Approach

- **testing approach**: Regular (code first, then tests), as in previous plans. Go unit
  tests with gomock, POSIX remote tests via `make check-remote`.
- every task: `make check` green before the next.
- one PR per task: `paste-spike` (no PR, findings recorded here) → `remote-paste-put` →
  `agr-paste` → `setup-paste-key`.
- **CRITICAL: update this plan file when scope changes during implementation**

## Testing Strategy

- **unit**: source selection order and fallbacks (mocked clipboard and screenshot finder),
  binding resolution (pane id, primary fallback, local row), upload argv and stdin, the
  entered text (quoting, several files, no newline), error texts; setup keymap rewriting
  table cases.
- **remote**: `paste-put` writes stdin, prints the absolute path, rejects bad names and
  prunes old files (tmux and zmx test dirs share the verb; one test file is enough).
- **e2e**: manual on `homelab` in agterm (Post-Completion).

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix

## Solution Overview

```text
ctrl+a>v ─ agterm keymap ─▶ agr paste <row> <pane-id>        (Mac)
  1. binding  = pane-id's binding, else the row's primary; none → "not an agr row"
  2. content  = clipboard image | Finder files | screenshot < 5 min; none → error
  3. upload   = ssh <host> agr paste-put <name>   (stdin = bytes) → remote abs path
  4. deliver  = enter the quoted path(s) into the pane, no newline (method from Task 1)
```

- The pane-id binding decides the host. A split pane opened by ⌘D is bound to `<name>-2`
  on the same host, so either pane works.
- One ssh call per file. Several files are uploaded in sequence, and the paths are entered
  together only when all uploads succeed.
- Remote files go to `~/.cache/agr/paste/<UTC stamp>-<basename>`, mode 0600. Each upload
  first prunes files older than 7 days, so there is no daemon or timer.

## Technical Details

- **Clipboard (darwin)**: one JXA script prints a small JSON header. Files come from
  `readObjectsForClasses([NSURL], {NSPasteboardURLReadingFileURLsOnlyKey: true})` → paths.
  Otherwise image data is `dataForType(public.png)`, else TIFF converted with
  `NSBitmapImageRep` → PNG, written to a Mac temp file → path. Otherwise `{}`. Finder ⌘C
  also puts the file icon on the clipboard as an image, so **files are checked before image
  data**. That swaps the order decided in brainstorm without changing its intent, since the
  icon is never what the user means.
- **Screenshot finder**: folder = `defaults read com.apple.screencapture location`
  (`~`-expanded), else `~/Desktop`. Candidates come from
  `mdfind -onlyin <dir> 'kMDItemIsScreenCapture == 1'`, which works in any locale (the file
  name prefix is localized). If Spotlight is off (empty result), fall back to the newest
  `*.png` by mtime. Accept a file only if its mtime is under 5 minutes old.
- **Remote verb**: `paste-put <basename>`. The name passes a strict check (no `/`, no
  leading `.`, printable, ≤128 bytes; anything else → `image.png`). Writes stdin to
  `$HOME/.cache/agr/paste/<stamp>-<name>` via a temp file + `mv`, prints the header then
  the absolute path. Size cap 50 MB, checked on the Mac before upload.
- **Delivery**: shell-quote each remote path (`remote.QuoteRemoteCommand`), join with
  spaces, add a trailing space so the user keeps typing. Only printable text is ever entered
  (`remote.Printable`).
- **Errors** (stderr, exit 1, shown by `--error-hud`): `not an agr row`,
  `nothing to paste: copy an image or files, or take a screenshot`,
  `<host>: run agr install <host> (remote agr is <v>)`, `<file>: larger than 50 MB`.

## What Goes Where

- **Implementation Steps**: code, tests, README in this repo.
- **Post-Completion**: manual checks on `homelab`, release notes.

## Implementation Steps

### Task 1: Spike — how a remote Claude Code attaches an image path

Answer before building: does Claude Code turn an entered image path into `[Image #N]`
when it is typed (`agtermctl session type`, plain keystrokes) or only when it is pasted
(bracketed paste)? A local drag arrives as a paste. Use a throwaway row, never the user's
live agent rows.

**Files:**
- Modify: this plan (findings)

- [ ] `agr open homelab paste-spike`, start `claude` there, `scp` a PNG to `/tmp/spike.png`
- [ ] type the path with `agtermctl session type --pane-id …` (no newline) → attached?
- [ ] bracketed paste: `agtermctl clipboard set '/tmp/spike.png'` (or `pbcopy`), then
      `agtermctl session paste --target <row> --pane left` → attached? restore the clipboard
- [ ] record the result here and choose the delivery. Typed works → `Ctl.Type` without a
      newline. Only paste works → save the clipboard, set the path text, `session paste
      --pane <left|right from the binding>`, restore the clipboard. Neither → typed (Claude
      still `Read`s a plain image path) and note it in the README
- [ ] kill the spike session (`agr kill homelab paste-spike`)

### Task 2: Remote `paste-put` verb and runner `PastePut`

**Files:**
- Modify: `internal/remotescript/agr.sh`, `internal/remote/runner.go`
- Create: `tests/remote/tmux/paste_put_test.sh`
- Modify: `internal/remote/runner_test.go`

- [ ] `agr.sh`: `paste-put <name>` verb: name check (fallback `image.png`), `umask 077`,
      `mkdir -p "$HOME/.cache/agr/paste"`, prune `-mtime +7`, write stdin via temp + `mv`,
      print header + absolute path
- [ ] `Runner.PastePut(ctx, host, name string, body []byte) (string, error)` through the
      `Data` header check; reply must be one absolute printable line
- [ ] remote test: content round-trips, absolute path printed, a `../x` name becomes
      `image.png`, a 9-day-old file is pruned, a fresh one kept
- [ ] Go tests: argv + stdin passed, old remote → install error, bad reply → error
- [ ] `make check` green

### Task 3: Mac content sources (clipboard and screenshot)

**Files:**
- Create: `internal/pasteboard/pasteboard.go` (JXA clipboard + screenshot finder, darwin exec
  behind an injectable runner), `internal/pasteboard/pasteboard_test.go`

- [ ] `Clipboard.Read(ctx) (Content, error)`: `Content{Files []string; Image string}`
      from the JXA JSON. Files before image data. The temp PNG goes under
      `os.TempDir()/agr-paste`, removed by the caller after upload
- [ ] `Screenshots.Latest(ctx, maxAge) (string, bool, error)`: folder from `defaults`
      (`~` expanded), `mdfind` candidates, mtime fallback, age filter
- [ ] tests with a fake runner: files win over an icon image, PNG only, TIFF-converted
      reply, empty clipboard, malformed JSON
- [ ] tests: `location` set/unset, Spotlight empty → mtime fallback, a 6-minute-old file
      rejected
- [ ] `make check` green

### Task 4: `agr paste <row> <pane-id>`

**Files:**
- Create: `internal/cli/paste.go`, `internal/cli/paste_test.go`
- Modify: `internal/cli/dependency.go` (+ regenerated mocks), `internal/agterm/ctl.go`
  (`Type` without newline, or clipboard/paste per Task 1), `cmd/agr/run.go`,
  `cmd/agr/wire.go`

- [ ] binding: the `ForRow(row)` entry with `PaneID == pane-id`, else the primary; none →
      `not an agr row`
- [ ] sources in order (clipboard files/image, screenshot ≤5 min); none → nothing-to-paste
      error; >50 MB → error before any upload
- [ ] upload each file via `PastePut`, all or nothing; delete the Mac temp PNG afterwards
- [ ] deliver the quoted paths + trailing space with the Task 1 method; refuse non-printable
- [ ] dispatch `paste` (listed in `--help`: `paste [row] [pane-id]  Send the clipboard or
      latest screenshot to a remote row`), wire deps
- [ ] tests: pane-id binding vs primary fallback, local row, each source branch, multi-file
      quoting (space and `'` in names), upload failure enters nothing, temp file removed
- [ ] `make check` green

### Task 5: `agr setup` binds Paste to remote

**Files:**
- Modify: `internal/cli/setup.go`, `internal/cli/setup_test.go`, `cmd/agr/run.go`
  (`--paste-key`), `cmd/agr/wire.go`

- [ ] generalize the End helpers over the verb (`commandLine(lines, verb)`,
      `agrWord(…, verb)`, `chord(…)`) so End and Paste share repoint/dedupe/carry-chord logic
- [ ] Paste line: `command "Paste to remote" <key> --error-hud <agr> paste
      "{AGT_SESSION_ID}" "{AGT_PANE_ID}"`, with its own note
- [ ] key: `--paste-key` if given, else keep the existing chord, else `ctrl+a>v` if no other
      `map`/`command` line binds it, else no key + a message
- [ ] tests: fresh file, existing paste line repointed with its chord kept, `ctrl+a>v`
      already bound elsewhere → no key, `--paste-key` rebinds, duplicate dropped with note,
      End behavior unchanged (existing cases still pass)
- [ ] `make check` green

### Task 6: Verify acceptance criteria

- [ ] each Overview source works, and a local row and an empty clipboard give the errors
- [ ] old remote → install hint; non-printable name never entered
- [ ] `make check` green (test, lint, shellcheck, check-remote)

### Task 7: [Final] Update documentation

- [ ] README: "Paste a screenshot" section (key, sources, remote location, 7-day prune,
      `agr install <host>` needed) + `paste` in the command list + keymap line
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

- `homelab` in agterm: ⌘⇧4 → `ctrl+a>v` on a bound row → image attached in Claude; Finder
  ⌘C of two files → both paths; Copy Image in a browser → attached; the ⌘D side pane →
  uploads to the same host; a local row → error HUD.
- `agr install homelab` after release (new remote verb).
- release notes (v1.2.0): the key, the sources, `agr setup` then `agr install <host>`.
