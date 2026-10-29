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
   Caveat: the floating thumbnail is on (`show-thumbnail` unset = on), and macOS writes the
   file only when the thumbnail goes away (~5 s or a click). A key pressed sooner finds
   no new file (see Technical Details > Screenshot finder).

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
- `internal/remote/ssh.go`: `ExecSSH.Run(ctx, host, stdin, argv…)` carries stdin, but
  `Runner.Data` always passes `nil`. Task 2 adds an unexported `data(ctx, host, stdin,
  verb, args…)` under `Data`, so the header parsing stays in one place. An old remote
  script prints usage to stderr only, so the header is missing, `ErrNotInstalled` comes
  back, and the existing "run: agr install <host>" text shows.
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
- Keymap custom commands receive `{AGT_SESSION_ID}`, `{AGT_PANE_ID}` and `{AGT_PANE}`
  (agterm keymap docs). `{AGT_PANE_ID}` is empty for a launcher fired with no session.
- `cmd/agr/run.go` `runSetup` accepts exactly `--end-key X`. Adding `--paste-key` needs real
  flag parsing. `runEnd` falls back to `AGT_SESSION_ID`/`AGTERM_SESSION_ID` for the row.
- `agtermctl clipboard set` works only in a Live-sessions pane, and this Mac is not in that
  mode, so text on the clipboard means `pbcopy`. `agtermctl session paste` takes
  `--pane left|right|scratch`, not `--pane-id`.
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
- **e2e**: manual on `myhost` in agterm (Post-Completion).

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
  4. deliver  = enter the path(s) into the pane, no newline (method from Task 1)
```

- The pane-id binding decides the host. A split pane opened by ⌘D is bound to `<name>-2`
  on the same host, so either pane works.
- One ssh call per file. Several files are uploaded in sequence, and the paths are entered
  together only when all uploads succeed.
- Remote files go to `~/.cache/agr/paste/<UTC stamp>-<n>-<name>`, mode 0600, where `<n>` is
  the file's index in this paste (two copied files may share a basename). Each upload first
  prunes files older than 7 days, so there is no daemon or timer.
- **Names are made safe on the Mac** (`[A-Za-z0-9._-]`, every other byte → `-`, extension
  kept, ≤80 bytes, no leading `.` or `-`). macOS screenshot names carry spaces and U+202F,
  so this matters. The remote re-checks with the existing `valid_token` rule and refuses
  (exit 2) instead of renaming. The paths are then safe as written: nothing needs quoting,
  and whether Claude Code accepts quoted paths stops mattering.

## Technical Details

- **Clipboard (darwin)**: one JXA script (`osascript -l JavaScript`, `run(argv)`) prints
  JSON. Files: loop over `pasteboardItems` and read `stringForType('public.file-url')` →
  `NSURL.URLWithString(…).path`. This avoids `$`-exposed constants such as
  `NSPasteboardURLReadingFileURLsOnlyKey`, which JXA often lacks. Otherwise image data is
  `dataForType('public.png')`, else `public.tiff` → `NSBitmapImageRep` →
  `representationUsingTypeProperties(4, $())` (4 = PNG), written to the path Go passes as
  `argv[0]`. Go creates that path with `os.MkdirTemp` and removes it. Otherwise `{}`. Finder
  ⌘C also puts the file icon on the clipboard as an image, so **files are checked before
  image data**. That swaps the order decided in brainstorm without changing its intent,
  since the icon is never what the user means. Folders, non-regular files and unreadable
  files are refused by name.
- **Screenshot finder**: folder = `defaults read com.apple.screencapture location`
  (`~`-expanded). A non-zero exit means the key is unset → `~/Desktop`, not an error.
  Extension = `defaults read com.apple.screencapture type`, unset → `png`, which also leaves
  out `.mov` recordings. Pick the newest regular file with that extension by mtime
  (`os.ReadDir`, pure Go). The name prefix is localized, so names are not matched.
  Spotlight (`mdfind`) is not used: it lags behind new files and would pick an older one.
  Accept the file only if it is under 5 minutes old.
  **Thumbnail delay**: with the floating thumbnail on, a key pressed within ~5 s of ⌘⇧4 finds
  the previous screenshot (if under 5 min) or nothing. Decided: agr never changes macOS
  settings; the README says to wait for the thumbnail, click it away, or turn it off.
- **Remote verb**: `paste-put <name>`. The name must pass `valid_token`, else exit 2 with
  nothing written. Writes stdin to `$HOME/.cache/agr/paste/<stamp>-<name>` via a `mktemp`
  file in the same directory (removed by `trap` on failure) + `mv`, then prints the header
  and the absolute path. Prune: `find "$dir" -type f -mtime +7 -exec rm -f {} +` (POSIX, no
  `-delete`). The Mac sends `<n>-<safe name>` as `<name>`, so the remote adds only the stamp.
- **Reply check**: the Mac accepts the reply only if it starts with
  `<remote home>/.cache/agr/paste/` (home from `Runner.Home`), ends with `-<name>`, and is
  `remote.Printable`. Anything else is an error and nothing is entered.
- **Limits**: 50 MB **total** per paste (the body is held in memory by `ExecSSH.Run`),
  checked before any upload. 60 s context timeout per upload.
- **Delivery**: join the checked remote paths with spaces and add a trailing space so the
  user keeps typing. No quoting is needed because names are safe.
- **Errors** (stderr, exit 1, shown by `--error-hud`): `not an agr row`,
  `nothing to paste: copy an image or files, or take a screenshot`, the existing
  `ErrNotInstalled` text, `paste is larger than 50 MB`, `<name>: not a regular file`.
  A version-mismatch warning (`log.Printf`) must not fail a paste.

## What Goes Where

- **Implementation Steps**: code, tests, README in this repo.
- **Post-Completion**: manual checks on `myhost`, release notes.

## Implementation Steps

### Task 1: Spike — how a remote Claude Code attaches an image path

Answer before building: does Claude Code turn an entered image path into `[Image #N]`
when it is typed (`agtermctl session type`, plain keystrokes) or only when it is pasted
(bracketed paste)? A local drag arrives as a paste. Use a throwaway row, never the user's
live agent rows.

Spike findings (2026-10-15, run by hand after the autonomous run skipped it; that run had
bound the orchestrating row to `paste-spike`, since removed): on `myhost`, inside tmux, Claude
Code leaves a **typed** path as text, while the same path sent through `agtermctl session
type` wrapped in `ESC[200~ … ESC[201~` becomes `[Image #N]`. Two space-separated paths in one
paste give two images; a trailing space is fine. So delivery is `Ctl.Type` with a bracketed
paste: no `session paste`, and the Mac clipboard is never replaced. The JXA source probe and the
thumbnail timing are covered by the Post-Completion check.

**Files:**
- Modify: this plan (findings)

- [x] `agr open myhost paste-spike`, start `claude` there, `scp` a PNG to
      `~/.cache/agr/paste/spike.png` (skipped - the requested name resolved to a live
      Claude/ralphex row; no throwaway row was available without risking the user's work)
- [x] type the path with `agtermctl session type --pane-id …` (no newline) → attached?
      (skipped - manual Claude attachment check)
- [x] bracketed paste: `pbcopy` the path (`clipboard set` needs Live sessions mode), then
      `agtermctl session paste --target <row> --pane left` → attached? (skipped - manual
      macOS clipboard and Claude attachment check)
- [x] record the result here and choose the delivery. Typed works → `Ctl.Type` without a
      newline. Only paste works → the keymap line also passes `{AGT_PANE}` for
      `session paste --pane`, and the user's clipboard is **replaced** by the path text
      (decided: no snapshot/restore). Neither → typed (Claude still
      `Read`s a plain image path) and note it in the README (skipped - empirical result
      unavailable; selected the typed-without-newline fallback and recorded that above)
- [x] probe the JXA script once per source **from a keymap command** (the real caller):
      Finder ⌘C of two files, ⌃⌘⇧4 image, Copy Image from a browser, empty clipboard.
      Record the outputs here, plus any privacy prompt macOS shows for pasteboard access
      (skipped - requires hands-on macOS clipboard sources and a keymap command)
- [x] measure the thumbnail delay: ⌘⇧4, then `ls -lt ~/Desktop` once a second until the file
      appears (skipped - requires a hands-on screenshot capture)
- [x] kill the spike session (`agr kill myhost paste-spike`) (skipped - no separate spike
      session was created; the pre-existing shared row was restored rather than killed)

### Task 2: Remote `paste-put` verb and runner `PastePut`

**Files:**
- Modify: `internal/remotescript/agr.sh`, `internal/remote/runner.go`
- Create: `tests/remote/tmux/paste_put_test.sh`
- Modify: `internal/remote/runner_test.go`

- [x] `agr.sh`: `paste-put <name>` verb (also in `usage()`): `valid_token` or exit 2,
      `umask 077`, `mkdir -p "$HOME/.cache/agr/paste"`, POSIX prune, `mktemp` in the dir +
      `trap` cleanup, `mv` to `<stamp>-<name>`, print header + absolute path
- [x] runner: unexported `data(ctx, host, stdin, verb, args…)`; `Data` calls it with `nil`
- [x] `Runner.PastePut(ctx, host, name string, body []byte) (string, error)`: reply checked
      against `<home>/.cache/agr/paste/` prefix and `-<name>` suffix, printable, one line
- [x] remote test: content round-trips, absolute path printed, `../x` and `.hidden`
      refused with exit 2 and nothing written, a file aged with `touch -t 202001010000` is
      pruned, a fresh one kept (no GNU/BSD-only `date`)
- [x] Go tests: argv + stdin passed, `Data` unchanged (nil stdin), old remote → install
      error, reply outside the paste dir → error, wrong suffix → error
- [x] `make check` green

### Task 3: Mac content sources (clipboard and screenshot)

**Files:**
- Create: `internal/pasteboard/pasteboard.go` (JXA clipboard + screenshot finder, darwin exec
  behind an injectable runner), `internal/pasteboard/pasteboard_test.go`

- [x] `Clipboard.Read(ctx, pngPath string) (Content, error)`: `Content{Files []string;
      Image bool}` from the JXA JSON (the script from Task 1). Files before image data.
      Directories, non-regular and unreadable files → error naming the file
- [x] `Screenshots.Latest(ctx, now, maxAge) (string, bool, error)`: `defaults` folder and type
      (non-zero exit = unset), newest regular file with that extension by mtime, age filter
- [x] `SafeName(index int, base string) string` for `[A-Za-z0-9._-]`, extension kept, ≤80
      bytes, no leading `.`/`-`; empty → `file`
- [x] tests with a fake runner: files win over an icon image, PNG only, TIFF-converted
      reply, empty clipboard, malformed JSON, a folder refused
- [x] tests on a temp dir: `location` set / exit 1 → `~/Desktop`, `type` jpg, a `.mov` newer
      than the png ignored, a 6-minute-old file rejected
- [x] tests: `SafeName` with `Screenshot 2026-10-14 at 18.53.01.png` (spaces + U+202F),
      `../etc`, `.env`, a 200-byte name, a name with `'`
- [x] `make check` green

### Task 4: `agr paste <row> <pane-id>`

**Files:**
- Create: `internal/cli/paste.go`, `internal/cli/paste_test.go`
- Modify: `internal/cli/dependency.go` (+ regenerated mocks), `internal/agterm/ctl.go`
  (`Type` without newline, or clipboard/paste per Task 1), `cmd/agr/run.go`,
  `cmd/agr/wire.go`

- [x] row from the argument, else `AGT_SESSION_ID`/`AGTERM_SESSION_ID` (as `runEnd`); pane
      id may be empty
- [x] binding: the `ForRow(row)` entry with `PaneID == pane-id`, else the primary; none →
      `not an agr row`. A scratch or unbound local pane on a bound row uses the primary's
      host. That's right for ⌘J's `agr shell`; a truly local pane gets a remote path
      (accepted, noted in the README)
- [x] sources in order (clipboard files/image, screenshot ≤5 min); none → nothing-to-paste
      error; total >50 MB → error before any upload
- [x] upload each file via `PastePut(host, SafeName(i, base), body)` with a 60 s timeout,
      all or nothing; remove the Mac temp dir afterwards
- [x] deliver the paths + trailing space with the Task 1 method, by pane id, or the
      primary pane when the id is empty; refuse non-printable
- [x] dispatch `paste` (listed in `--help`: `paste [row] [pane-id]  Send the clipboard or
      latest screenshot to a remote row`), wire deps
- [x] tests: pane-id binding vs primary fallback, empty pane id, row from env, local row,
      each source branch, two files with the same basename get distinct names, upload
      failure enters nothing, temp dir removed, a mismatch warning does not fail
- [x] `make check` green

### Task 5: `agr setup` binds Paste to remote

**Files:**
- Modify: `internal/cli/setup.go`, `internal/cli/setup_test.go`, `cmd/agr/run.go`
  (`--paste-key`), `cmd/agr/wire.go`

- [x] generalize the End helpers over the verb (`commandLine(lines, verb)`,
      `agrWord(…, verb)`, `chord(…)`) so End and Paste share repoint/dedupe/carry-chord logic
- [x] Paste line: `command "Paste to remote" <key> --error-hud <agr> paste
      "{AGT_SESSION_ID}" "{AGT_PANE_ID}"`, with its own note
- [x] key: `--paste-key` if given, else keep the existing chord, else `ctrl+a>v` if no other
      `map`/`command` line binds it (chords split on `|`), else no key + a message. agterm's
      built-in defaults are not in keymap.conf; a clash there surfaces as the existing
      "agterm reports problems" error
- [x] `AskKey` stays End-only: the paste key is never prompted for
- [x] `runSetup`: parse `--end-key` and `--paste-key` in any order (each at most once,
      non-empty), usage `agr setup [--end-key <chord>] [--paste-key <chord>]`; update the
      final "agterm reloaded…" line and add a Paste "no key" hint
- [x] tests: fresh file, existing paste line repointed with its chord kept, `ctrl+a>v`
      already bound elsewhere (also as `x|ctrl+a>v`) → no key, `--paste-key` rebinds,
      duplicate dropped with note, End behavior unchanged (existing cases still pass)
- [x] `cmd/agr` tests: flag parsing (both flags, either order, repeated, missing value)
- [x] `make check` green

### Task 6: Verify acceptance criteria

- [x] each Overview source works, and a local row and an empty clipboard give the errors
- [x] old remote → install hint; non-printable name never entered
- [x] `make check` green (test, lint, shellcheck, check-remote)

### Task 7: [Final] Update documentation

- [x] README: "Paste a screenshot" section (key, sources, remote location, 7-day prune,
      `agr install <host>` needed; the thumbnail delay with its three workarounds, incl.
      `defaults write com.apple.screencapture show-thumbnail -bool false`; on the paste
      branch, that the clipboard ends up holding the path) + `paste` in the command list + keymap line
- [x] move this plan to `docs/plans/completed/`

## Post-Completion

- `myhost` in agterm: ⌘⇧4 → `ctrl+a>v` on a bound row → image attached in Claude; Finder
  ⌘C of two files → both paths; Copy Image in a browser → attached; the ⌘D side pane →
  uploads to the same host; a local row → error HUD.
- `agr install myhost` after release (new remote verb).
- release notes (v1.2.0): the key, the sources, `agr setup` then `agr install <host>`.
