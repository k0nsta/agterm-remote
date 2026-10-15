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
                     ◄── "ack 1\n" (header and body accepted)
Mac:  receiver.Listener.serveConn  (cmd == "overlay")
        └─ binding = ByHostName(host, session) → row, pane
        └─ OverlaySink.Overlay(reqCtx, request) (exit, payload)   (the daemon)
              └─ temp file (0600) → Ctl.Overlay: agtermctl … --html --block [--js] [--pane|--size-percent]
        └─ writes "<exit>\n<payload>", closes
host: prints the payload, exits with <exit>
```

- **One connection per overlay, never half-closed.** The `bytes` header field marks where the
  body ends. The client never calls `shutdown(SHUT_WR)`, so the daemon reads EOF on that
  connection only when the client has really gone (Ctrl-C, ssh dropped). Whether an
  ssh-forwarded unix socket passes a half-close through no longer matters.
- **Ack before the wait.** As soon as a valid header and the full body have arrived, the daemon
  writes `ack 1\n` (`1` is the protocol version). The client waits up to 5 s for it:
  - no ack: an old or absent daemon;
  - EOF after the ack, with no reply: the bridge dropped while waiting.
  Each case gets its own message. Status lines are untouched.
- **Synchronous sink.** `OverlaySink.Overlay(ctx, req) (exit int, payload []byte)`. The
  listener writes whatever it returns, once, and closes the connection.
- **Request ctx**: `context.WithCancel(serveCtx)`, cancelled when the client's EOF arrives
  (read loop after the ack) or on shutdown, so `agr down` and daemon exit never wait on a human.
- **`wait: true`**: the daemon runs `agtermctl … --block` on the request ctx and returns its
  result. If the ctx is cancelled first, cleanup runs on
  `context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)`: `OverlayClose`, then
  remove the temp file.
- **`wait: false`**: the daemon starts the `--block` call in a goroutine on the **host** ctx,
  not the request ctx, so the client closing after `opened` doesn't close the overlay. It
  tracks the goroutine the same way host goroutines are tracked today (`runtime.done` /
  `d.children`), so `stopHost` and `Close` wait for it. It waits up to 500 ms:
  - if agtermctl exits 1 in that time (bad pane, agterm down), that error is the reply;
  - otherwise it replies `0 {"outcome":"opened"}`.
  The goroutine removes the temp file when agtermctl returns. When the host ctx ends it closes
  the overlay with the same 2 s cleanup ctx.
- **One agr overlay per row and pane.** The daemon keeps a set of `(row, pane)` pairs with an agr
  overlay open; a second request for a busy pair gets exit 1 `an agr overlay is already open on
  this pane`. So a cancel can only ever close agr's own overlay. The Task 1 spike also
  determines what happens when the user's own local overlay is up there.
- **Client**: python3 only, independent of `AGR_RELAY`. It must send, wait for an ack, and
  read a reply; doing that in plain `sh` around `nc`/`socat` variants is fragile. A host without
  python3 gets `agr overlay: needs python3 on this host`.

## Technical Details

- **Request header** (one line, ≤ 64 KB, then exactly `bytes` raw bytes):
  `{"cmd":"overlay","session":"<name>","js":true,"wait":true,"size_percent":0,"bytes":1234}`
  - `session` must pass `token.Valid` and is required. `session_id` is **rejected**: status
    events accept it as a direct row target with no binding check (`event.go:63-67`), and an
    overlay must never be able to aim at a row the host has no binding for.
  - `bytes` must be 1 … 2 MiB (`maxOverlayBytes`), and `size_percent` must be 0 (full) or 1 … 100.
  - Unknown fields are ignored.
  - The header and body are read under `conn.SetReadDeadline(30 s)`, which is cleared after
    the ack. The body is read with `io.ReadFull` from the **same** `bufio.Reader` that read the
    header, since part of the body may already be buffered. Bytes after the body are ignored.
- **Daemon → client**:
  - First `ack 1\n`.
  - Then the reply: the exit code line (`0`, `1` or `2`) followed by the payload until EOF.
    - For 0 and 2, the payload is agtermctl's stdout, unchanged.
    - For 1, it's a short message, e.g. `no agr row for session "x" on this Mac`,
      `page is larger than 2 MB`, `an agr overlay is already open on this pane`,
      `agterm could not show the page`. The full agtermctl error, which may name Mac paths, goes
      to the daemon log only.
  - Any agtermctl exit other than 0 or 2, including `-1` for a killed process or missing binary,
    becomes 1.
- **Remote verb** `agr overlay <file> [--js] [--wait] [--size-percent N]`:
  - The session comes from new side-effect-free helpers `tmux_current_session` /
    `zmx_current_session`. They return the name after the ownership check and do not write
    `@agr_state`. `tmux_status`/`zmx_status` are refactored onto them and keep their behaviour.
    With no agr-owned session: `agr overlay: run it inside a row opened with agr open` (exit 1).
  - The file must be a readable regular file of 1 … 2 MiB (`wc -c`). It is checked before any
    connection is made.
  - The python3 client gets the socket path, file, session, js, wait and size as **separate argv
    entries** and builds the header JSON itself; sh never pastes JSON.
  - Messages:
    - socket missing or connect refused: `agr overlay: the bridge to the Mac is down (run agr up
      <host> on the Mac)`
    - no ack within 5 s: `agr overlay: the Mac's agr daemon is too old or not running; upgrade agr
      there and restart agr daemon`
    - EOF after the ack, before a reply: `agr overlay: the bridge to the Mac dropped while waiting`
  - A reply code outside {0, 1, 2}, or a first line that is not a number, becomes 1. For exit 0
    or 2, the payload goes to stdout; for exit 1, to stderr. Ctrl-C exits 130 quietly, with no
    traceback.
- **Pane choice**:
  - `--size-percent` given: a floating panel over the session, without `--pane`.
  - Otherwise `--pane <binding.Pane>`, after checking it is one of `left|top|primary|right|bottom|split`.
  - An empty `binding.Pane` (older agterm): no `--pane`.
  - The spike checks a pane value left stale after a promote, and a `--pane` on a row without a
    split.
- **Temp files**: `os.CreateTemp` in `<cache>/overlay/` (mode 0600). Each is removed when its
  agtermctl call returns. Leftovers from a crash are removed once, at daemon start, before any
  host listener is started.
- **Liveness**: a valid overlay header counts as a received event (`MarkAlive`), like status.

## What Goes Where

- **Implementation Steps**: code, tests, README in this repo.
- **Post-Completion**: `decide.py` in machine-setup, manual check on `homelab`, release.

## Implementation Steps

### Task 1: Spike — agtermctl overlay behaviour the design relies on

Answer in a throwaway local row (never the user's live rows); record results here.

**Files:**
- Modify: this plan (findings)

- [ ] `--html F --block --pane left` on a row **without** a split: does it work or fail? Then
      `--pane right` with a hidden split, and a pane value left stale after promoting the split
      to primary
- [ ] kill the `agtermctl … --block` process: does the overlay stay up? Does
      `agtermctl session overlay close --target <row> [--pane <p>]` close a `--block` one?
      What does the killed call print, and what exit code?
- [ ] a non-submitting page closed with ⌘W: confirm exit 2 and the stdout shape. A submitting
      page: confirm `{"outcome":"submitted","value":…}` on exit 0
- [ ] a second `overlay open` on a busy row/pane, both while agr's page is up and while one of
      the user's own overlays is: does it replace, and what does the first `--block` call return?
- [ ] on a row the user isn't viewing (no `--follow`), check how noticeable the overlay is, and
      record the answer to the open question below
- [ ] update Solution Overview / Technical Details with the results

### Task 2: `agterm.Ctl` overlay open/close

**Files:**
- Modify: `internal/agterm/ctl.go`, `internal/agterm/ctl_test.go`

- [ ] `Overlay(ctx, row string, opts OverlayOpts) (exit int, out []byte, err error)` with
      `OverlayOpts{File, Pane string; JS bool; SizePercent int}`. It runs
      `session overlay open --html <file> --block [--js] [--pane p | --size-percent n]
      --target row --socket …`.
      - Exit 0 and 2 come back with stdout and a nil error, like `Confirm`.
      - Every other exit comes back as an error that carries stderr.
- [ ] `OverlayClose(ctx, row, pane string) error` runs
      `session overlay close --target row [--pane p]`.
- [ ] tests:
      - argv for each option combination;
      - exit 2 is not an error; exit 1 and exit -1 are errors that carry stderr;
      - `Pane` and `SizePercent` together are refused before anything runs.
- [ ] `make test` must pass before Task 3

### Task 3: Receiver — overlay requests on the bridge socket

**Files:**
- Create: `internal/receiver/overlay.go`, `internal/receiver/overlay_test.go`
- Modify: `internal/receiver/listener.go`, `internal/receiver/dependency.go`,
  `internal/receiver/listener_test.go`, `internal/receiver/mocks/dependency_mock.go` (generated),
  `internal/daemon/daemon.go` (the `NewListener` call only)

- [ ] `OverlayRequest{Row, Pane string; JS, Wait bool; SizePercent int; Page []byte}` and
      `decodeOverlayHeader(line)` with the limits in Technical Details (it rejects `session_id`)
- [ ] `OverlaySink` interface (`Overlay(ctx, OverlayRequest) (exit int, payload []byte)`) in
      `dependency.go`. `NewListener` takes it; a nil sink means overlays are refused with exit 1.
- [ ] `serveConn`:
  - Peek `cmd` on each line. A line with `overlay` switches the connection to overlay mode.
    The status lines before it have already been routed as usual.
  - Read the body as described in Technical Details (deadline, `io.ReadFull` on the same reader).
  - Resolve the binding (a miss replies 1), then write the ack.
  - Start a reader that cancels the request ctx (derived from the Serve ctx) on EOF.
  - Call the sink, write the reply, close.
- [ ] tests:
  - status lines, then an overlay header, on one connection
  - a body bigger than 4096 bytes, sent in the same write as the header
  - trailing bytes after the body
  - oversized `bytes`, or a short body before the deadline: exit 1, no sink call
  - `session_id` header rejected
  - unknown session: exit 1, no sink call
  - the ack comes before the reply
  - client EOF after the ack cancels the ctx
  - the Serve ctx being cancelled while the sink blocks: `Serve` returns
  - `MarkAlive` on a valid header
  - nil sink: exit 1
- [ ] `make test` must pass before Task 4

### Task 4: Daemon — show the page and reply

**Files:**
- Create: `internal/daemon/overlay.go`, `internal/daemon/overlay_test.go`
- Modify: `internal/daemon/daemon.go`, `internal/daemon/dependency.go`,
  `internal/daemon/mocks/dependency_mock.go` (generated), `cmd/agr/wire.go`

- [ ] `Overlays` interface (`Overlay`, `OverlayClose`) in `dependency.go`, plus a field on
      `daemon.Config`. Wire it to `*agterm.Ctl` in `cmd/agr/wire.go` with
      `_ daemon.Overlays = (*agterm.Ctl)(nil)` beside the existing assertions.
- [ ] the daemon implements `receiver.OverlaySink` per Solution Overview:
      - temp file
      - pane rule
      - the busy `(row, pane)` guard
      - `wait: true` on the request ctx
      - `wait: false` in a tracked goroutine on the host ctx with the 500 ms early-error window
      - cleanup and `OverlayClose` on a 2 s ctx detached from cancellation
- [ ] remove leftover `<cache>/overlay/*` once at daemon start, before any host listener
- [ ] tests (mocked `Overlays`):
  - submitted, dismissed and error replies
  - an early exit 1 within 500 ms comes back as the reply
  - for `wait: false`, a client disconnect after `opened` does **not** call `OverlayClose`
  - `wait: true` cancelled calls `OverlayClose` with a live ctx
  - host ctx cancelled while `wait: false` is pending → `OverlayClose`, and `stopHost` returns
    within `teardownTimeout`
  - busy pane → exit 1
  - the temp file is gone in every case, with the right content and mode 0600
  - `--size-percent` drops `--pane`, and an invalid `binding.Pane` drops `--pane`
  - leftover cleanup
- [ ] `make test` must pass before Task 5

### Task 5: Remote verb `agr overlay`

**Files:**
- Modify: `internal/remotescript/agr.sh`
- Create: `tests/remote/tmux/overlay_test.sh`, `tests/remote/zmx/overlay_test.sh`

- [ ] extract `tmux_current_session` / `zmx_current_session` (no side effects), and rebase
      `tmux_status` / `zmx_status` on them (existing status tests must stay green)
- [ ] parse `overlay <file> [--js] [--wait] [--size-percent N]` (N 1-100); an unknown option is a
      usage error, exit 2; add it to the remote usage text
- [ ] file checks and `have python3` per Technical Details, all before connecting
- [ ] python3 client per Technical Details: separate argv, 2 s connect timeout, no
      `SHUT_WR`, 5 s ack timeout, untimed wait for the reply, the three messages, reply-code
      check, quiet Ctrl-C (130)
- [ ] `shellcheck -s sh` clean
- [ ] remote tests with a python3 fake server in the test, on a short socket path under
      `$REMOTE_TEST_TMP`. The test kills the server itself, because the `lib.sh` trap only kills tmux.
  - submitted → stdout and exit 0; dismissed → exit 2; error → stderr and exit 1
  - the header carries session, js, wait and size_percent
  - no ack → old-daemon message; ack then EOF → bridge-dropped message
  - no socket → bridge-down message
  - outside a session → exit 1; oversized file → exit 1 with no connection made
  - the zmx test covers the session name and outside a session
- [ ] `make check` must pass before Task 6

### Task 6: Verify acceptance criteria

- [ ] every Overview behaviour holds; status events unchanged (existing tests pass)
- [ ] old daemon, missing bridge and a dropped bridge give the documented messages, never a hang
- [ ] `agr down <host>` and daemon exit return within `teardownTimeout` while an overlay is open
- [ ] `make check` green (test, lint, shellcheck, check-remote)

### Task 7: [Final] Update documentation

- [ ] README: "Show a page from the remote" section covering:
      - the command and its exit codes, and `--wait`
      - one self-contained file ≤ 2 MB
      - one agr overlay per pane
      - add `overlay` to the remote verbs list
- [ ] README "Requirements": python3 on the host for `agr overlay`; the Mac daemon must be at
      least this release
- [ ] README "Failure modes": the bridge-down, old-daemon and bridge-dropped messages
- [ ] README "Security": a `--js` page runs inside agterm on the Mac, can reach the Mac's
      network (localhost services included), and sends results back through its submitted value
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

## Open questions

- **Notice on an unviewed row**: the overlay opens without `--follow`, so a `decide` panel from
  a remote row you are not looking at can wait unseen. Should agr also raise something (a
  status glyph or HUD), switch to the row, or nothing? The Task 1 spike records how visible it
  is.
