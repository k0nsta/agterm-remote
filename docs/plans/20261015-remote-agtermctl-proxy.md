# Remote agtermctl proxy

## Overview

On a row opened with `agr open`, programs run on the remote host, where there is no
`agtermctl` and no `AGTERM_*` environment. Every agterm-aware tool therefore refuses to run or
falls back. This covers the user's skills (`decide`, `spawn-agent`, `teardown-agent`,
`autoimplementation`, `agterm/show-image.sh`) and everyone else's.

This plan makes a remote row look like agterm to any tool, with no change to the tool:

- `agr install` puts an **`agtermctl` proxy** on the host. It forwards each call (argv, the
  local files it names, stdin when asked) over the existing bridge to the Mac.
- The Mac daemon runs the **real** `agtermctl` after a policy check and a rewrite, then streams
  stdout, stderr and the exit code back.
- `agr open` exports `AGTERM_ENABLED`, `AGTERM_SESSION_ID`, `AGTERM_PANE`, `AGTERM_PANE_ID`,
  `AGTERM_WORKSPACE_ID`, `AGTERM_WINDOW_ID` and `AGTERM_SOCKET` into the remote session, so
  tools see themselves as inside agterm.

```sh
# on homelab, inside an agr row, with no change to the tool:
agtermctl session overlay open --html /tmp/panel.html --js --block --target "$AGTERM_SESSION_ID"
{"outcome":"submitted","value":"…"}
```

What this enables:

- **Remote UI**: `decide` panels and `show-image` overlays from a remote Claude.
- **Messaging**: `session type` / `session text` between rows. Host → own rows and Mac → any
  row (the latter works today).
- **Remote spawn**: a remote `spawn-agent` runs `agtermctl session new --command …`. That
  becomes a new row attached to a new agr session on the **same host**, running the command
  there.
- **Spawning workers on a host from the Mac**: a new `agr open <host> <name> --command …`
  gives local tools the building block.

Decided by the user (not open):

- **Scope**: all four of the above.
- **Generic**: an agtermctl proxy, not agr-specific verbs plus per-skill adapters. This
  supersedes the earlier `agr overlay` verb plan, whose transport design (ack, no half-close,
  ctx rules, review findings) is reused here.
- **Trust, scoped**: a host may read everything, write only to rows bound to it (including rows
  it created), and is refused Mac-global commands.

## Context (from discovery)

- **Bridge, remote → Mac**: `internal/remotescript/agr.sh` `relay()` sends status lines to
  `$AGR_SOCK`. `internal/receiver/listener.go` `serveConn` reads JSON lines (64 KB max), only
  for `cmd: session-status`, and never writes back. `session` resolves to a binding via
  `Resolver.ByHostName`. `session_id` is accepted as a direct row with no check
  (`event.go:63-67`). That path stays as it is for status, and is never used by the proxy.
- **Daemon** (`internal/daemon`): one `receiver.Listener` per host (`daemon.go:560`).
  Goroutines are tracked through `runtime.done` / `d.children`. `stopHost` waits up to
  `teardownTimeout` (5 s).
- **agterm wrapper**: `internal/agterm/ctl.go` (`Ctl` over `Outputter`, `exec.CommandContext`, no
  shell). `ExecRunner.Output` returns only at exit, with exit `-1` for a killed process or a
  missing binary.
- **Opening a row**: `internal/cli/open.go` records the binding (`AGTERM_SESSION_ID`,
  `AGTERM_PANE`, `AGTERM_PANE_ID`) and runs `ssh -t host ~/.local/bin/agr attach <name> …`.
  The remote `attach` is `tmux_attach` (it creates with `new-session -d -s`, then attaches) or
  `zmx attach --labels …`.
- **Bindings** (`internal/bindings`): `ForRow`, `ForHost`, `ByHostName`, keyed by row and pane.
  The binding store is the ownership source for the policy.
- **agtermctl surface**: `tree events workspace session surface dashboard window quick sidebar
  notify font keymap hooks browser config theme pick ask restore zmx terminfo clipboard
  version`. Arguments that name **Mac-local** things:
  - `--html FILE`, `--cwd DIR` on overlays and `session new`
  - `session new --command`, `session overlay open COMMAND`, `session restore CMD`
  - `--url http://localhost…` (localhost is the Mac)
  - `events --run`
  - `--stdin`
- **Remote tests**: `tests/remote/{tmux,zmx}` with `lib.sh`.
- **homelab**: `AGR_RELAY=python3`. The Mac daemon running now predates 1.2.0.

## Development Approach

- **testing approach**: Regular (code first, then tests)
- complete each task fully before moving to the next; small, focused changes
- **CRITICAL: every task MUST include new/updated tests**, for both success and error paths
- **CRITICAL: all tests must pass before starting next task** (`make check`)
- **CRITICAL: update this plan file when scope changes during implementation**
- status events keep working exactly as before
- an old daemon, a missing bridge, or a refused command fails with a clear agtermctl-style
  message and a non-zero exit, never a hang

## Testing Strategy

- **unit (Go)**:
  - wire codec
  - listener dispatch: status lines, then a ctl request on the same socket
  - the policy table, with each class and target resolution
  - the rewrite table: paths become uploaded temp files; `--command` and `overlay open COMMAND`
    become `agr` commands that run on the host; `restore`
  - daemon execution: streaming, exit codes, cancellation, cleanup
  - `agr open` env export and `--command`
- **remote (sh)**: `tests/remote/{tmux,zmx}/proxy_test.sh` drive the installed `agtermctl` proxy
  against a fake python3 bridge server, covering:
  - argv and file upload framing
  - the stdin rule
  - stdout, stderr and exit codes
  - the old-daemon, bridge-down and bridge-dropped messages
  - Ctrl-C
- Every test-only helper takes `*testing.T` first and calls `t.Helper()`.
- The real agterm round trips are a Post-Completion manual check on `homelab`.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- keep the plan in sync with the code

## Solution Overview

```
host:  tool → agtermctl (proxy, python3)
         └─ request: header line {"cmd":"ctl","v":1,"session":…,"argv":[…],"files":[…],"stdin":N}
            + file bytes + stdin bytes                    ──► bridge.sock (reverse forward)
                                                          ◄── "ack 1\n"
Mac:   Listener.serveConn (cmd == "ctl") → binding(host, session) = caller row
         └─ CtlSink.Ctl(reqCtx, request, stream)          (the daemon)
              1. classify argv → read | write(target) | create | refused
              2. resolve target (--target id/prefix/active) → full row id; check ownership
              3. rewrite Mac-local arguments (files → temp, commands → agr on the host)
              4. run real agtermctl, stream frames back
         ◄── frames: "o <n>\n<bytes>" stdout · "e <n>\n<bytes>" stderr · "x <code>\n" exit
host:  proxy writes stdout/stderr as they arrive, exits with <code>
```

- **One connection per call, never half-closed.** It reuses the design from the reviewed overlay
  plan:
  - the sizes in the header mark where the body ends;
  - `ack 1\n` arrives after a valid header and body;
  - on the daemon side, EOF means the client is gone;
  - the request ctx derives from the Serve ctx;
  - cleanup runs on `context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)`.
- **Streaming**, not buffered: `events`, `pick`, `ask` and `overlay --block` can run for a long
  time, and stdout must arrive as it is produced. The daemon runs agtermctl through a new
  streaming runner (`exec.Cmd` with pipes), not `Outputter`.
- **Cancellation**: a client EOF or shutdown kills the local agtermctl, then runs the command's
  cancel hook from the rewrite table. `session overlay open … --block` → `session overlay close
  --target <row> [--pane p]`; `pick`/`ask` → `pick cancel` / `ask cancel`.
- **Environment**: on `agr open` (and every re-attach), the Mac passes the binding's ids to the
  remote `attach`.
  - tmux: `new-session -e` for a new session, plus `set-environment` on re-attach, so new
    shells and windows inherit them.
  - zmx: the equivalent; the Task 1 spike finds it.
  - `AGTERM_SOCKET` is set to the marker value `agr-proxy`; the proxy accepts and drops
    `--socket`.
- **The proxy** is `~/.local/bin/agtermctl`, written by `agr install` next to `agr`. It is a
  short sh wrapper that runs `agr ctl "$@"`; the python3 client lives in `agr.sh`. If a real
  agtermctl is already earlier on PATH, install leaves PATH order alone and `doctor` warns.

## Technical Details

### Wire

- **Request header**: one line, at most 64 KB. `session` must pass `token.Valid`; there is no
  `session_id`.
  ```json
  {"cmd":"ctl","v":1,"session":"name","argv":["session","overlay","open","--html","@0","--js","--block"],
   "files":[{"bytes":1234,"name":"panel.html"}],"stdin":0}
  ```
  - The proxy replaces each file argument with `@<index>`; the daemon maps it back to a temp
    path.
  - At most 8 files and 8 MiB in total; stdin at most 1 MiB.
  - The body follows the header: each file's bytes in order, then the stdin bytes. It is read
    with `io.ReadFull` from the same `bufio.Reader`, under a 30 s read deadline that is cleared
    after the ack.
- **Reply frames**, in this order:
  - `ack 1\n`
  - then any number of `o <n>\n<n bytes>` (stdout) and `e <n>\n<n bytes>` (stderr) frames
  - then exactly one `x <code>\n`
  - A daemon-side refusal is `e …` with the agtermctl-style message, then `x 1`.
- **Exit codes**: agtermctl's own (0/1/2/…) are passed through unchanged. An internal failure
  (exit `-1`, a missing binary) becomes 1.

### Classification and policy (Mac, `internal/proxy/policy.go`)

The table is keyed by subcommand path, so `session overlay open` matches before `session`.

| class | commands | rule |
| --- | --- | --- |
| read | `tree`, `version`, `window list`, `session text`, `keymap list`, `hooks list`, `pick result`, `ask result`, `zmx list/tree` | always allowed |
| write | other `session …` and `surface …` subcommands, `notify`, `pick open/cancel`, `ask open/cancel`, `dashboard` with targets | target must be owned |
| create | `session new`, `workspace new` | allowed; a new session is rewritten to `agr open` on the host, so it ends up owned |
| refused | `config`, `keymap reload/run`, `hooks reload`, `theme`, `font`, `quick`, `sidebar`, `window` (except `list`), `browser`, `restore`, `terminfo`, `clipboard`, `zmx` writes, `events --run`, unknown subcommands | exit 1: `agtermctl (via agr): '<cmd>' is not allowed from a remote host` |

- **Targets**: `--target` is resolved with one `tree --json`, which gives a full id from an
  id, a unique prefix or `active`.
  - With no `--target`, the target is the **caller's own row**, never `active`, because the user
    is usually looking at another row.
  - **Owned** means `bindings.ForRow(row)` has an entry for this host.
  - Commands with several targets (`session close <ids…>`, `dashboard`) check every target.
- `events` (the stream, no `--run`) is allowed as a read, and its output is the Mac's whole event
  feed.
- Unknown subcommands are refused: new agtermctl commands need a table entry. The refusal
  message names the command, and the README says how to request one.

### Rewrites (Mac, `internal/proxy/rewrite.go`)

- **Files**: each `@i` becomes a temp file under `<cache>/proxy/<random>/` (mode 0600). The whole
  directory is removed when agtermctl returns, and leftovers are cleared at daemon start.
- **`--cwd DIR`** on `--html` overlays: the directory is on the host and means nothing on the
  Mac. It is dropped, so pages must be one file (README). On `session new`, it goes to
  `agr open --cwd`.
- **`session new [--name N] [--cwd D] [--command C] [--wait] …`** becomes
  `session new [--name N] --command "<agr> open <host> <gen-name> [--cwd D] [--command C]" …`.
  - `<gen-name>` is a fresh valid token from `--name`, else the caller's session plus `-<n>`.
  - The new row binds itself through `agr open`, as rows do today, so the host owns it.
- **`session overlay open COMMAND…`** (a program, not `--html`/`--url`) becomes
  `session overlay open <agr> run <host> [--cwd D] -- COMMAND…`. That is a new Mac verb: a
  one-shot `ssh -t` that runs the command in the remote directory.
- **`session restore CMD --target R`** on a host-owned row becomes
  `session restore "<agr> open <host> <name of R's binding>"`, so a restart reattaches instead of
  running `CMD` on the Mac. The original `CMD` is recorded as that remote session's
  restart command (spike: tmux option `@agr_restore`, used by `attach` when the session is
  gone).
- **`--url http://localhost…` / `127.0.0.1`**: refused, with `localhost on the host is not
  reachable from agterm; use the host name`. Other URLs pass through.
- **`--socket X`**: dropped (the daemon adds the real one).

### `agr open` additions (Mac)

- **`--command CMD`**: on a new remote session, run `CMD` instead of a login shell. The remote
  `attach` passes it to `tmux new-session … CMD` / `zmx` (spike).
- **Env export**: `attach` gets `--env K=V` pairs for the seven `AGTERM_*` variables. Values are
  checked with `token.Valid`, or as UUIDs.

### Remote proxy (`agr ctl`, python3)

- Only `-<file>`-style arguments named in a small flag table (`--html`) are uploaded. The proxy
  checks they are readable regular files and replaces them with `@i`.
- stdin is forwarded only when argv contains `--stdin`.
- The messages:
  - bridge down: `agtermctl (via agr): the bridge to the Mac is down (run agr up <host> on the
    Mac)`
  - no ack in 5 s: `agtermctl (via agr): the Mac's agr daemon is too old or not running`
  - EOF after the ack: `agtermctl (via agr): the bridge to the Mac dropped`
- Ctrl-C exits 130 quietly.
- Outside an agr-owned session: `agtermctl (via agr): not inside a row opened with agr open`
  (exit 1).

## What Goes Where

- **Implementation Steps**: code, tests, README in this repo.
- **Post-Completion**: manual round trips on `homelab`, a skill sweep (whether anything still
  needs changes), and the release.

## Implementation Steps

### Task 1: Spike — environment, commands and agtermctl behaviour

Use throwaway rows only, local and on `homelab`, and record the results here.

**Files:**
- Modify: this plan (findings)

- [ ] tmux:
  - `new-session -e K=V` and `set-environment` on re-attach: does a new shell or window see
    them? Does the shell already running keep the old values?
  - running a command in place of the shell (`new-session … CMD`)
  - the same two questions for zmx
- [ ] `agtermctl` overlay behaviour:
  - `--pane left` on a row with no split
  - killing a `--block` call: does the overlay stay up, and does `session overlay close` close it?
  - exit code and stdout for ⌘W and for a submit
  - a second overlay on a busy pane
- [ ] list every agtermctl subcommand that reads stdin or takes a local path/command flag
      (`agtermctl help <cmd>` for each), and settle the policy and rewrite tables above
- [ ] restore flow: does `session restore` on a row survive an agterm restart and run
      `agr open` (reattach) as expected?
- [ ] update Solution Overview / Technical Details with the results

### Task 2: `agr open --command` and environment export

**Files:**
- Modify: `internal/cli/open.go`, `internal/cli/open_test.go`, `internal/remotescript/agr.sh`,
  `cmd/agr/run.go`, `tests/remote/tmux/*`, `tests/remote/zmx/*`

- [ ] `agr open <host> <name> [--cwd D] [--command CMD]`:
  - `CMD` is quoted for the remote side with the existing argv/quoting rules (never pasted into
    a shell string unquoted)
  - the remote `attach` runs it on a **new** session only
  - an existing session just reattaches, with a notice
- [ ] the Mac passes `--env AGTERM_…=…` for the seven variables to `attach`; the remote
      `attach` validates the values and applies them per the spike
- [ ] tests (Go): argv for `--command`, the env pairs, and invalid values refused
- [ ] tests (remote): a new tmux session sees the env and runs `CMD`; re-attach refreshes the
      env; the same for zmx
- [ ] `make check` must pass before Task 3

### Task 3: Wire codec and listener dispatch

**Files:**
- Create: `internal/proxy/wire.go`, `internal/proxy/wire_test.go`
- Modify: `internal/receiver/listener.go`, `internal/receiver/dependency.go`,
  `internal/receiver/listener_test.go`, `internal/receiver/mocks/dependency_mock.go` (generated),
  `internal/daemon/daemon.go` (the `NewListener` call only)

- [ ] `proxy.Request{Row, Pane string; Argv []string; Files []File; Stdin []byte}` and
      `DecodeHeader` with the limits above (it rejects `session_id`); a frame writer
      (`o`/`e`/`x`) that is safe for concurrent stdout/stderr writers
- [ ] `CtlSink` interface (`Ctl(ctx, proxy.Request, w proxy.FrameWriter) (exit int)`) in
      `dependency.go`; `NewListener` takes it; a nil sink means `e …` + `x 1`
- [ ] `serveConn`:
  - a `cmd: ctl` line switches the connection to proxy mode
  - read the body under the deadline
  - resolve the binding; a miss refuses
  - send `ack 1`
  - an EOF watcher cancels the request ctx (derived from the Serve ctx)
  - call the sink, write `x <code>`, close
- [ ] tests:
  - status lines, then a ctl header, on one connection
  - a body larger than 4096 bytes, sent in the same write as the header
  - trailing bytes after the body
  - limits: file count, total size, stdin
  - a short body before the deadline
  - `session_id` rejected; unknown session refused
  - the ack comes before any frame
  - client EOF cancels the ctx
  - `Serve` returns promptly while the sink blocks
  - `MarkAlive`
- [ ] `make test` must pass before Task 4

### Task 4: Policy and target resolution

**Files:**
- Create: `internal/proxy/policy.go`, `internal/proxy/policy_test.go`,
  `internal/proxy/dependency.go`, `internal/proxy/mocks/dependency_mock.go` (generated)

- [ ] `Classify(argv) (class, targets []string, err)` per the table, with longest-prefix
      matching on subcommand paths
- [ ] `Authorize(ctx, host, callerRow, argv)`:
  - resolve each target through `Tree` (id, prefix, `active`)
  - a missing `--target` means the caller's row
  - write-class targets must be owned (`ForRow` has this host)
  - the refusal messages from Technical Details
- [ ] tests (table-driven):
  - every class, and a nested subcommand matching before its parent
  - an unknown command is refused
  - a missing `--target` becomes the caller's row
  - a prefix and `active` resolve to full ids
  - a local (unowned) row is refused
  - multi-target `session close` with one unowned target is refused
  - `events --run` is refused; plain `events` is allowed
- [ ] `make test` must pass before Task 5

### Task 5: Rewrites and execution on the Mac

**Files:**
- Create: `internal/proxy/rewrite.go`, `internal/proxy/rewrite_test.go`,
  `internal/proxy/exec.go`, `internal/proxy/exec_test.go`, `internal/daemon/proxy.go`,
  `internal/daemon/proxy_test.go`
- Modify: `internal/daemon/daemon.go`, `internal/daemon/dependency.go`, `cmd/agr/wire.go`,
  `cmd/agr/run.go` (the new `agr run` verb)

- [ ] `Rewrite(host, callerBinding, argv, files) (argv', cleanup, cancelHook, err)` per Technical
      Details: files to a 0600 temp dir, `--cwd` handling, `session new` to `agr open`, overlay
      `COMMAND` to `agr run`, `restore` to `agr open`, localhost URLs refused, `--socket` dropped
- [ ] `agr run <host> [--cwd D] -- argv…` (Mac verb): `ssh -t` with the same quoting as `open`
- [ ] the streaming runner: real agtermctl with stdout/stderr pipes copied to frames as they
      arrive. On ctx cancel it kills the process, then runs the cancel hook on a detached 2 s
      ctx.
- [ ] the daemon implements `CtlSink` (`Authorize` → `Rewrite` → run). It is wired in `wire.go`
      with compile-time interface assertions, and its goroutines are tracked so `stopHost`
      returns within `teardownTimeout`. Leftover `<cache>/proxy/*` is cleared at start.
- [ ] tests:
  - each rewrite, with the exact argv
  - temp files: content, 0600, and removed afterwards in every case
  - streaming order of stdout and stderr; exit passthrough; `-1` becomes 1
  - cancel kills the process and runs the overlay-close hook with a live ctx
  - host shutdown while a call blocks
  - `agr run` argv and quoting
- [ ] `make test` must pass before Task 6

### Task 6: Remote proxy (`agr ctl` and `~/.local/bin/agtermctl`)

**Files:**
- Modify: `internal/remotescript/agr.sh`, `internal/cli/install.go` (or wherever install writes
  the remote files), `internal/cli/doctor.go`
- Create: `tests/remote/tmux/proxy_test.sh`, `tests/remote/zmx/proxy_test.sh`

- [ ] `agr ctl …` in `agr.sh`:
  - session lookup through new side-effect-free `tmux_current_session` / `zmx_current_session`
    (`tmux_status`/`zmx_status` are rebased on them and stay green)
  - `have python3`, else exit 1 with a message
  - the python3 client per Technical Details: file flag table, `@i`, `--stdin` rule, no
    `SHUT_WR`, a 5 s ack timeout, frame demux to stdout/stderr, exit code, quiet Ctrl-C
- [ ] `agr install` writes `~/.local/bin/agtermctl` (`#!/bin/sh` + `exec "$HOME/.local/bin/agr"
      ctl "$@"`), mode 0755. `agr doctor` reports whether `agtermctl` on the host's PATH is the
      proxy.
- [ ] `shellcheck -s sh` clean
- [ ] remote tests against a python3 fake bridge server on a short socket path, which the test
      kills itself:
  - argv and file framing, the `@i` substitution, and stdin only with `--stdin`
  - stdout and stderr frames, exit codes 0, 1 and 2
  - no ack → old-daemon message; ack then EOF → dropped message; no socket → bridge-down message
  - outside a session → refused; zmx covers the session lookup
- [ ] `make check` must pass before Task 7

### Task 7: Verify acceptance criteria

- [ ] from a throwaway remote row on `homelab`, unchanged tools:
  - `decide.py` shows a panel and returns the answer
  - `show-image.sh` shows an image
  - `agtermctl session text --target <sibling>` reads a sibling row
  - `session type` into a sibling row; the same into a local row is refused
  - `session new --command 'claude …'` opens a new bound row on homelab running it
- [ ] status events are unchanged (existing tests pass); refused, old-daemon and bridge
      messages are as documented; `agr down` returns within `teardownTimeout` during a blocking
      call
- [ ] `make check` green

### Task 8: [Final] Update documentation

- [ ] README "Remote agterm control" section:
  - what works unchanged
  - the policy table
  - single-file pages; localhost URLs
  - how to ask for a new command class
  - `agr open --command`, `agr run`
- [ ] README "Requirements": python3 on the host; the Mac daemon must be at least this release
- [ ] README "Failure modes": the proxy messages
- [ ] README "Security": the scoped policy, that `--js` pages run in agterm on the Mac and can
      reach the Mac's network, and that `events` streams the Mac's whole event feed to the host
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

- **Skill sweep** (machine-setup): run `decide`, `spawn-agent`, `teardown-agent`,
  `autoimplementation` and `agterm/show-image.sh` from a remote Claude, unchanged. Fix only
  what still breaks, and record which assumptions broke so the README can warn other users.
  Known suspect: `spawn-agent` runs `git -C "$REPO"`, which works because it now runs on the host.
- **Spawning on a host from the Mac**: `spawn-agent --host <host>` on top of `agr open --command`
  (skill-side change; the worktree is created with `ssh host git …`).
- Manual check on `homelab`: the Task 7 round trips with the real agterm; Ctrl-C during a
  `--block` overlay closes it; an agterm restart reattaches remote-created rows.
- Restart the Mac `agr daemon` after upgrading. Release notes: `brew upgrade agr`, restart the
  daemon, `agr install <host>`, re-open rows to pick up the environment.

## Open questions

- **Remote worker → local orchestrator**: with the scoped policy, a worker on `homelab` cannot
  type into the local row that spawned it. Allow writes to the row that spawned a host session,
  recorded when `agr open` runs from that row? Or keep it strictly host-scoped?
- **A real agtermctl on the host** (a Mac host running agterm): install the proxy there anyway
  (it shadows the real one in agr rows only), or skip the proxy on such hosts?
