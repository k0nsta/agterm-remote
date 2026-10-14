# Remote HTML overlay (`agr overlay`)

## Overview

A program on a remote host (Claude Code, a script) cannot show agterm UI today: the host has
no `agtermctl`, and agr's bridge only carries agent status from the host to the Mac. This adds a
remote verb that shows an HTML page as an agterm overlay on the row the program runs in and,
with `--wait`, returns what the page submitted, like `agtermctl session overlay open --html F
--js --block` does locally.

```sh
# on the host, inside a row opened with `agr open`
agr overlay /tmp/panel.html --js --wait
{"outcome":"submitted","value":"{\"answers\":{…}}"}
echo $?    # 0 submitted · 2 dismissed · 1 error
```

Decided by the user (not open):

- **Interactive**: the submitted value comes back, so `decide`-style panels work from remote rows.
- **Interface**: a remote `agr overlay` verb, not an `agtermctl` shim on the host.
- **Transport**: in-band over the existing reverse-forwarded bridge socket, both directions on one
  connection. No extra ssh.

## Context (from discovery)

- `internal/remotescript/agr.sh`: POSIX `sh`. `relay()` sends a status line to `$AGR_SOCK`
  (default `~/.cache/agr/bridge.sock`) with `nc`, `python3` or `socat` (`AGR_RELAY`, picked in
  that order). `tmux_status`/`zmx_status` find the session name from `$TMUX_PANE` / `$ZMX_SESSION`
  and check agr owns it (`tmux_owned`, `zmx get … agr`).
- `internal/receiver/listener.go`: `serveConn` reads newline-terminated lines (cap
  `maxEventLine` 64 KB) with one `bufio.Reader`, decodes each with `Decode` (only
  `cmd: session-status`), resolves `session` to a binding (`Resolver.ByHostName`, which gives
  `Row`, `Pane`, `PaneID`) and pushes status. It never writes back on the connection.
- `internal/daemon`: builds one `receiver.Listener` per host (`daemon.go:560`, the daemon itself
  is the `StatusSink`). `dependency.go` has `UI` (`HudOpen`/`HudClose`) for non-status row UI.
- `internal/agterm/ctl.go`: `Ctl` runs `agtermctl` through `Outputter`. `Confirm` shows the
  pattern for a blocking command whose exit 2 means "no", not failure.
- `agtermctl session overlay open --html F [--js] --block` waits for the page to submit or
  close, prints its outcome JSON, and exits 0 (submitted), 2 (dismissed) or 1 (error).
  `--pane` scopes it to one split pane and cannot be combined with `--size-percent`.
  Without `--cwd` the page has no file access, so a page must be one self-contained file.
- Remote tests: `tests/remote/<mux>/*_test.sh` with `tests/remote/lib.sh` (`run_agr`, a temp
  `HOME`, `AGR_SOCK` under the test dir).
- homelab uses `AGR_RELAY=python3`. The Mac daemon running now is older than 1.2.0.

## Development Approach

- **testing approach**: Regular (code first, then tests)
- complete each task fully before moving to the next; small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task,
  success and error paths
- **CRITICAL: all tests must pass before starting next task** (`make check`)
- **CRITICAL: update this plan file when scope changes during implementation**
- status events keep working exactly as before; an old remote or an old Mac daemon fails with a
  clear message, never a hang

## Testing Strategy

- **unit**: wire codec (header validation, body length, reply format), listener dispatch
  (status vs overlay on the same socket), daemon overlay handling with mocked `agterm`
  (binding lookup, wait/no-wait, dismissed, error, disconnect closes the overlay, temp file
  removed), `Ctl` argv.
- **remote**: `tests/remote/tmux/overlay_test.sh` drives `agr overlay` against a fake bridge
  socket (a small python3 server in the test) for submitted, dismissed, error, oversized
  file, not-in-a-session, and an old daemon that closes without replying.
- Every test-only helper takes `*testing.T` first and calls `t.Helper()`.
- No UI e2e; the real overlay is a Post-Completion manual check.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- keep the plan in sync with the code

## Solution Overview

```
host: agr overlay page.html --js --wait
  └─ python3 client ── header line + page bytes ──► bridge.sock (reverse forward)
                                                     │
Mac:  receiver.Listener.serveConn  (cmd == "overlay")
        └─ binding = ByHostName(host, session) → row, pane
        └─ OverlaySink.Overlay(ctx, request)   (the daemon)
              └─ temp file (0600) → Ctl.Overlay: agtermctl … --html --block [--js] [--pane|--size-percent]
              └─ reply "<exit>\n<agtermctl stdout or error text>" ──► same connection
host: prints the payload, exits with <exit>
```

- One connection per overlay. The listener reads the header line, sees `cmd: overlay`, reads
  exactly `bytes` more from the same `bufio.Reader`, and hands the request to a new
  `OverlaySink`. Status lines are untouched.
- The daemon always runs `agtermctl` with `--block` (so it knows when to delete the temp file).
  With `wait: false` it replies `0` with `{"outcome":"opened"}` as soon as the overlay is
  up and keeps waiting in the background; with `wait: true` it replies with agtermctl's own
  outcome.
- If the remote side disconnects while waiting (Ctrl-C), the daemon closes the overlay
  (`session overlay close`) and deletes the file.
- The client is python3 only, independent of `AGR_RELAY`: it must send, half-close and read a
  reply, and parsing that in plain `sh` around `nc`/`socat` variants is fragile. A host without
  python3 gets `agr overlay: needs python3 on this host`.

## Technical Details

- **Request header** (one line, ≤ 64 KB, then exactly `bytes` raw bytes):
  `{"cmd":"overlay","session":"<name>","js":true,"wait":true,"size_percent":0,"bytes":1234}`
  - `session` must pass `token.Valid`, the same as status events.
  - `bytes` must be 1 … 2 MiB (`maxOverlayBytes`), and `size_percent` must be 0 (full) or 1 … 100.
  - Unknown fields are ignored.
- **Reply**: first line is the exit code (`0`, `1` or `2`), then the payload until EOF.
  - For 0 and 2, the payload is agtermctl's stdout, unchanged.
  - For 1, it's an error text: for example `no agr row for session "x" on homelab`,
    `page is larger than 2 MB`, or the agtermctl error.
- **Remote verb** `agr overlay <file> [--js] [--wait] [--size-percent N]`:
  - Finds the session like `tmux_status`/`zmx_status`; outside an agr-owned session it fails with
    `agr overlay: run it inside a row opened with agr open` (exit 1).
  - The file must be a readable regular file of 1 … 2 MiB.
  - Connection failures:
    - Socket missing or connect refused: `agr overlay: the bridge to the Mac is down (run agr up
      <host> on the Mac)`.
    - EOF before any reply line (an old daemon): `agr overlay: the Mac's agr daemon is too old;
      upgrade agr there and restart agr daemon`.
  - With exit 0 or 2, the payload goes to stdout; with exit 1, to stderr.
- **Pane choice** (settled by the Task 1 spike):
  - `--size-percent` given: a floating panel over the session, without `--pane`.
  - Otherwise, the binding's pane: `--pane right` for a ⌘D split session; for the main pane,
    `--pane left` only if that works on a row without a split, else no `--pane`.
- **Temp files**: `os.CreateTemp` in the daemon's cache dir under `overlay/`, mode 0600,
  removed when agtermctl returns. On daemon start, files left over from a crash are removed.
- **Liveness**: a valid overlay header counts as a received event (`MarkAlive`), like status.

## What Goes Where

- **Implementation Steps**: code, tests, README in this repo.
- **Post-Completion**: `decide.py` in machine-setup, manual check on `homelab`, release.

## Implementation Steps

### Task 1: Spike — agtermctl overlay behaviour the design relies on

Answer in a throwaway local row (never the user's live rows); record results here.

**Files:**
- Modify: this plan (findings)

- [ ] `--html F --block --pane left` on a row **without** a split: works, or error? Same for
      `--pane right` with a hidden split
- [ ] killing the `agtermctl … --block` process: does the overlay stay up? Does
      `agtermctl session overlay close --target <row> [--pane <p>]` close a `--block` one, and
      what does the killed/blocked call print and exit?
- [ ] non-submitting page closed with ⌘W: confirm exit 2 and the stdout shape; a submitting
      page: confirm `{"outcome":"submitted","value":…}` on exit 0
- [ ] decide the pane rule from the results and update Technical Details > Pane choice

### Task 2: `agterm.Ctl` overlay open/close

**Files:**
- Modify: `internal/agterm/ctl.go`, `internal/agterm/ctl_test.go`

- [ ] `Overlay(ctx, row string, opts OverlayOpts) (exit int, out []byte, err error)`.
      `OverlayOpts{File, Pane string; JS bool; SizePercent int}`. It runs
      `session overlay open --html <file> --block [--js] [--pane p | --size-percent n]
      --target row --socket …`. Exit 0 and 2 are returned with stdout and a nil error, like
      `Confirm`.
- [ ] `OverlayClose(ctx, row, pane string) error` → `session overlay close --target row [--pane p]`
- [ ] tests: argv for each option combination; exit 2 is not an error; exit 1 is an error that
      carries stderr; `Pane` and `SizePercent` together are refused before running anything

### Task 3: Receiver — overlay requests on the bridge socket

**Files:**
- Create: `internal/receiver/overlay.go`, `internal/receiver/overlay_test.go`
- Modify: `internal/receiver/listener.go`, `internal/receiver/dependency.go`,
  `internal/receiver/listener_test.go`, `internal/receiver/mocks/dependency_mock.go` (generated)

- [ ] `OverlayRequest{Host, Session, Row, Pane, PaneID string; JS, Wait bool; SizePercent int;
      Page []byte}`, plus `decodeOverlayHeader(line)` with the limits in Technical Details
- [ ] `OverlaySink` interface (`Overlay(ctx, OverlayRequest, reply func(exit int, payload []byte))
      `) in `dependency.go`; `NewListener` takes it (nil = overlays refused with exit 1)
- [ ] `serveConn`:
  - Peek `cmd`. For `overlay`, read `bytes` from the same reader, resolve the binding (a miss
    replies exit 1), and call the sink.
  - Watch the connection for EOF and cancel the request ctx when the client goes away.
  - Return after the reply.
  - `session-status` lines behave exactly as today.
- [ ] write the reply (`<exit>\n<payload>`) once, then close; a write error is only logged
- [ ] tests:
  - status and overlay on separate connections of one listener
  - oversized `bytes` and a short body: exit 1, no sink call
  - unknown session: exit 1, no sink call
  - client disconnect cancels the ctx
  - `MarkAlive` on a valid header
  - nil sink: exit 1

### Task 4: Daemon — show the page and reply

**Files:**
- Create: `internal/daemon/overlay.go`, `internal/daemon/overlay_test.go`
- Modify: `internal/daemon/daemon.go`, `internal/daemon/dependency.go`,
  `internal/daemon/mocks/dependency_mock.go` (generated), `cmd/agr/wire.go`

- [ ] `Overlays` interface (`Overlay`, `OverlayClose`) in `dependency.go`, wired to `*agterm.Ctl`
      in `cmd/agr/wire.go`
- [ ] the daemon implements `receiver.OverlaySink`:
  - Write the page to `<cache>/overlay/<random>.html` (0600).
  - Pick the pane per Technical Details and run `Overlay` with `--block` semantics.
  - `wait: false`: reply `0 {"outcome":"opened"}` once agtermctl has started (the reply must not
    wait for the user), and keep the call running in the background.
  - `wait: true`: reply with agtermctl's exit and stdout.
  - ctx cancelled (client gone) before return: call `OverlayClose`.
  - Always remove the temp file.
- [ ] remove leftover `<cache>/overlay/*` on daemon start
- [ ] pass the sink to `receiver.NewListener` in `daemon.go`
- [ ] tests (mocked `Overlays`):
  - submitted, dismissed and error replies
  - wait=false replies before the mock returns
  - cancel triggers `OverlayClose`
  - the temp file is gone in every case, and its content and mode are right
  - `--size-percent` drops `--pane`
  - leftover cleanup

### Task 5: Remote verb `agr overlay`

**Files:**
- Modify: `internal/remotescript/agr.sh`
- Create: `tests/remote/tmux/overlay_test.sh`

- [ ] parse `overlay <file> [--js] [--wait] [--size-percent N]` (N 1-100); unknown option → usage
      exit 2; add it to the remote usage text
- [ ] session name via the same lookups as `tmux_status`/`zmx_status`, with the ownership check;
      none → exit 1 with the message from Technical Details
- [ ] file checks (regular, readable, 1 … 2 MiB via `wc -c`); `have python3` or exit 1
- [ ] python3 client (`python3 -c '…' "$AGR_SOCK" "$header" "$file"`):
  - Connect with a 2 s timeout and send the header plus the file.
  - `shutdown(SHUT_WR)`, then read the reply with no timeout (a human is answering).
  - Map connect failures and EOF-before-reply to the messages in Technical Details.
  - Exit with the reply's code; the payload goes to stdout for 0/2 and to stderr for 1.
  - Build the header in python3 from argv, never by sh string pasting.
- [ ] `shellcheck -s sh` clean
- [ ] remote tests against a python3 fake socket server in the test:
  - submitted → stdout and exit 0; dismissed → exit 2; error → stderr and exit 1
  - the header carries session, js, wait and size_percent
  - outside a session → exit 1; oversized file → exit 1 with no connection made
  - no socket → the bridge-down message
  - a server that closes without replying → the old-daemon message

### Task 6: Verify acceptance criteria

- [ ] every Overview behaviour holds; status events unchanged (existing tests pass)
- [ ] old daemon and missing bridge give the documented messages, never a hang
- [ ] `make check` green (test, lint, shellcheck, check-remote)

### Task 7: [Final] Update documentation

- [ ] README: "Show a page from the remote" section (command, exit codes, `--wait`, one
      self-contained file ≤ 2 MB, python3 on the host, Mac daemon must be ≥ this release,
      `--js` runs the page's scripts on your Mac: hosts are trusted per "Security"); add `overlay`
      to the remote verbs list
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

- `decide.py` (machine-setup, `~/.claude/skills/decide/scripts/decide.py`): when
  `AGTERM_ENABLED` is unset and `agr` is on PATH, render the page and run
  `agr overlay <page> --js --wait` instead of opening a browser; map exit 2 to "dismissed".
- Manual on `homelab`:
  - From a throwaway row, show a static page (`agr overlay page.html`) and a submitting page with
    `--wait`, in the main pane and in the ⌘D side pane.
  - Close the page with ⌘W → exit 2; press Ctrl-C on the host while it waits → the overlay closes.
  - Run a `decide` panel from a remote Claude session.
- Restart the Mac `agr daemon` after upgrading (the running one predates this).
- Release v1.3.0 notes: `brew upgrade agr`, restart the daemon, `agr install <host>`.
