# agr 1.0 — Go daemon + POSIX remote, zmx/tmux backends, agterm 0.26 alignment

## Overview

`agr` bridges agent-status events from AI agents running on a **remote host** inside a
terminal multiplexer back to **agterm**'s sidebar on the Mac. Version 0.4 is one bash script
on both sides; three revmux rounds on it found that 6 of 11 defects were bash-shaped
(`pipefail`/`set -e` aborts, `read` collapsing fields, `${x%% *}` truncation, `%q` quoting,
an unquoted id, python spawned per hook). agterm 0.26.0 (2026-09-02) changed the ground:
Live-sessions restore mode keeps every pane's process across a relaunch, `session context`
gives rows a durable purpose line, remote sessions are Mac↔Mac only, and nothing carries
agent status across a remote attach — so agr's job stands, and its shape should match
agterm's own.

1.0 rewrites the **Mac side in Go** (CLI + a supervising daemon) and shrinks the **remote
side to one embedded POSIX `sh` script** with two multiplexer backends — **zmx (default)**
and tmux. The daemon owns the `ssh -N -R` tunnels, receives events on its own forwarded
unix socket per host, speaks agterm's control socket directly for `session.status` (with an
`agtermctl` exec fallback on a decode failure), shows a HUD while a host is unreachable,
re-pushes each row's last level on reconnect, and drops bindings when a row closes. The
agterm row id never leaves the Mac.

Out of scope for 1.0: LaunchAgent install, a zmx-native `cmd` column, two-Macs handoff,
an offline event queue, any agr↔agr protocol, `golang.org/x/crypto/ssh`, Linux builds of
the Mac binary.

## Context (from discovery)

- Repo: `agr` (bash, **0.4.0, now merged to `master`** via PR #1), `README.md`, `install.sh`,
  `docs/backlog/*.md`, `.revmux/profile.md`. Work happens on branch **`go-version`** (has
  `master` merged in); bash `agr` remains the shipping tool on `master` until 1.0.
- **`agr quick` is out of scope**: `master` reverted it as untested with the quick terminal
  broken (`6ae33a1`), and the 0.4 merge mirrored that (`cd3f282`), so no `quick` exists in
  the code 1.0 replaces. A binding-based rewrite is a separate, later decision.
- Naming convention kept: Mac verbs user-facing (`open ls kill up down install doctor`),
  remote verbs mechanical (`attach sessions reap status`).
- **agterm 0.26.0 facts (verified):** control protocol = one newline-delimited JSON request +
  response per connection, 1 MiB cap, `{"ok":true,"result":…}` / `{"ok":false,"error":…}`;
  `ControlArgs` is synthesized Codable with no CodingKeys → a server that predates a field
  drops it and answers ok; wire keys are Swift property names `status blink autoReset pane
  paneID sound color shape`; request `{"cmd":"session.status","target":"<row>","args":{…}}`;
  `{"cmd":"version"}` → `{"ok":true,"result":{"app":{"version":…}}}`; socket:
  `AGTERM_CONTROL_SOCKET` → `$AGTERM_STATE_DIR/agterm.sock` → `~/Library/Application
  Support/agterm/agterm.sock` (sessions also export `AGTERM_SOCKET`); 0.26 refuses a status
  from a non-blocked pane while the session is blocked: `blocked status owned by pane <p>`;
  `agtermctl` = `/Applications/agterm.app/Contents/MacOS/agtermctl` (symlink at
  `/usr/local/bin`); `launchctl getenv PATH` is empty → resolve absolute paths at daemon
  start. Measured: exec `agtermctl` ≈ 10–13 ms/event; raw dial ≈ 0.2–1.3 ms.
- **agtermctl pick:** stdin JSON array `[{id,label,subtitle?}]`; prints ONE bare JSON result
  `{"result":"picked","id":…}` / `{"result":"custom","query":…}` (exit 0),
  `{"result":"cancelled"}` (exit 2), errors exit 1; `[]` only with `--allow-custom`.
- **agtermctl events** `--json --kind session.closed` streams one JSON event per line.
- **Cookbook `container-agent-status` sender shape** (must be accepted verbatim):
  `{"cmd":"session-status","state":…,"session_id":…,"pane":…,"pane_id":…,"args":["--blink","--auto-reset"]}`.
- **zmx 0.7.1 facts (verified):** `zmx attach <name>` create-or-attach; `zmx list` lines
  `[→ ]name=X\tpid=N\tclients=N\tcreated=EPOCH[\tcwd=…][\tk=v…]`; `zmx list --short`;
  labels `zmx set <name> k=v…` / `zmx get <name> [key]` / `key=` deletes; `zmx kill <name>`;
  `zmx version` prints socket dir + log dir; `$ZMX_SESSION` inside a session; `attach
  --labels` exists in `main` but is unreleased → probe `zmx attach --help`; socket dir
  `ZMX_DIR` → `XDG_RUNTIME_DIR/zmx` → `TMPDIR/zmx-uid` → `/tmp/zmx-uid` (pin `ZMX_DIR` at
  install); IPC-changing upgrades kill sessions; no foreground-command query.
- **tmux facts:** `set-option`/`show-option -t "=name:"` (colon); `has-session`/`kill-session`/
  `list-panes -s` take `-t "=name"`; `display-message -p -t "$TMUX_PANE" '#{@opt}'`.
- **Relay tool:** OpenBSD `nc -U` (Debian/Ubuntu/Alpine); GNU/busybox `nc` lack `-U` →
  fallback `python3`, then `socat`.
- User Go conventions: consumer-defined 1–3-method interfaces in `dependency.go` with
  `//go:generate go run go.uber.org/mock/mockgen -source=dependency.go -destination=mocks/dependency_mock.go -package=mocks`;
  producers return concrete types; unit tests beside code, integration in `/tests`; every
  test helper takes `*testing.T` first and calls `t.Helper()`; no duplicated test utilities;
  CI runs the same `make` targets as local.

## Development Approach

- **testing approach**: Regular (code first, then tests) — every task still ends with its
  tests written and green before the next task starts.
- Go 1.22+, no CGO, `internal/` packages, `cmd/agr` only entry point. `make test lint
  shellcheck check-remote` are the gates locally and in CI.
- The Mac side **never builds a shell string**: `ssh`, `mosh`, `agtermctl` run with argv.
  The only shell text is the constant remote scripts inside `internal/remote` and the
  embedded `remote/agr.sh`.
- Names and ids are validated to `[A-Za-z0-9_.-]` on both sides before use as argv,
  label values, or JSON values.
- Each task leaves `go build ./... && make test` green; the daemon is assembled from
  packages that already have tests.
- **CRITICAL: every task MUST include new/updated tests** for the code it adds or changes,
  covering success and failure paths, listed as separate checklist items.
- **CRITICAL: all tests must pass before starting next task.**
- **CRITICAL: update this plan file when scope changes during implementation.**
- Backward compatibility: `sessions` treats a tmux session with non-empty `@agr_target`
  (0.4 marker) as owned; `~/.cache/agterm/targets` (0.3) is ignored.

## Testing Strategy

- **unit tests** (beside code): fake unix-socket agterm server; fake `ProcessRunner`;
  table tests for event decode, pick decode, TSV parse, hooks merge, bindings store.
- **integration** (`/tests`): `tests/bridge/` — PATH-shim `ssh` that sleeps/exits, real
  process supervision; `tests/remote/` — shim-driven checks for `remote/agr.sh` with
  `tmux -L agr-test`, a fake `zmx` (shell script keeping state in a temp dir), and a fake
  `nc` capturing the line; `shellcheck -s sh remote/agr.sh`. Both run on macOS + Linux CI.
- **e2e**: no UI framework applies; the real-host checklist in Post-Completion is run by
  hand before a release.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope

## What Goes Where

- **Implementation Steps** (`[ ]`): code, tests, README, CI/release config in this repo.
- **Post-Completion**: real-host checklist, upgrade notes, Homebrew tap publication.

## Implementation Steps

### Task 1: Module, Makefile, CI skeleton

**Files:**
- Create: `go.mod`, `cmd/agr/main.go`, `cmd/agr/main_test.go`
- Create: `Makefile`, `.golangci.yml`, `.github/workflows/ci.yml`
- Modify: `.gitignore` (add `dist/`, `bin/`)

- [ ] `go mod init github.com/k0nsta/agterm-remote`; `cmd/agr/main.go` with subcommand
      dispatch (stdlib `flag`), `--version` printing `agr <version>` from a `-ldflags`-set
      `version` var defaulting to `dev`
- [ ] `Makefile`: `build`, `test` (`go test -race ./...`), `lint` (golangci-lint), `shellcheck`
      (`shellcheck -s sh remote/agr.sh`, no-op until Task 7), `check-remote` (`tests/remote`),
      `check` = all of them
- [ ] `.github/workflows/ci.yml`: matrix `macos-latest` + `ubuntu-latest`, runs `make check`
      only (same targets as local); installs `shellcheck`, `tmux`
- [ ] write test: `--version` prints the injected version; unknown subcommand exits 2 with usage
- [ ] run `make check` — must pass before Task 2

### Task 2: `internal/agterm` — wire types, direct socket client, version handshake, exec fallback

**Files:**
- Create: `internal/agterm/protocol.go`, `internal/agterm/client.go`, `internal/agterm/socket.go`
- Create: `internal/agterm/dependency.go`, `internal/agterm/mocks/dependency_mock.go`
- Create: `internal/agterm/client_test.go`, `internal/agterm/socket_test.go`, `internal/agterm/testserver_test.go`

- [ ] `protocol.go`: `Request{Cmd string; Target string,omitempty; Args any,omitempty}`,
      `StatusArgs{Status string; Blink *bool "blink,omitempty"; AutoReset *bool "autoReset,omitempty"; Pane string "pane,omitempty"; PaneID string "paneID,omitempty"}`,
      `Response{OK bool; Result json.RawMessage; Error string}`; `ErrRefused` (error string
      prefix `blocked status owned by pane`), `ErrDecode` (server-side decode failure:
      `invalid request` / `DecodingError`) — exact JSON tags asserted by a marshal test
- [ ] `socket.go`: `SocketPath()` → `AGTERM_CONTROL_SOCKET` → `AGTERM_SOCKET` →
      `$AGTERM_STATE_DIR/agterm.sock` → `~/Library/Application Support/agterm/agterm.sock`;
      `CtlPath()` → `/Applications/agterm.app/Contents/MacOS/agtermctl` if executable, else
      `exec.LookPath`, resolved once
- [ ] `client.go`: `Client{sock string; timeout time.Duration; run Runner; ctl string}`;
      `Do(ctx, Request) (Response, error)` = one dial, write line, read one line, close;
      `Version(ctx) (string, error)`; `Status(ctx, target string, args StatusArgs) error`
      returning `ErrRefused` on the 0.26 refusal, and on `ErrDecode` calling the fallback
      `run.Run(ctx, ctl, "session","status",args.Status,"--target",target,"--socket",sock, [+"--pane-id" id, +"--blink", +"--auto-reset"])`
      on **every** decode-failure reply (events must not be dropped); the "falling back to
      agtermctl" warning is logged once per process (`sync.Once`)
- [ ] `dependency.go`: `Runner` interface `Run(ctx, name string, args ...string) error` +
      `//go:generate` mockgen line; generate mocks
- [ ] `MinTestedVersion = "0.25.0"`: `Version()` result below it → one warning; no hardcoded
      allow-list of versions
- [ ] write tests: fake unix server in `testserver_test.go` (`newFakeAgterm(t, handler)` with
      `t.Helper()`): ok reply; refused reply → `ErrRefused`; two decode-failure replies →
      fallback Runner called twice with the exact argv, warning logged once; ECONNREFUSED →
      error, no fallback; `Version` parses `result.app.version` and warns below 0.25.0;
      timeout honored; `SocketPath` precedence table
- [ ] run `make check` — must pass before Task 3

### Task 3: `internal/token` and `internal/bindings` — validator and row ↔ (host, name, mux) store

**Files:**
- Create: `internal/token/token.go`, `internal/token/token_test.go`
- Create: `internal/bindings/store.go`, `internal/bindings/store_test.go`

- [ ] `Binding{Row, PaneID, Host, Name, Mux string; BoundAt time.Time}`; `Store{path}` with
      `Load()`, `Save()` (write tmp + rename), `Bind(b)` (replaces any binding for the same
      Row and any for the same Host+Name), `UnbindRow(row)`, `ByHostName(host,name)`,
      `ByRow(row)`, `ForHost(host) []Binding`, `Dangling(host, live []string) []Binding`
- [ ] `internal/token`: `Valid(s string) bool` for `[A-Za-z0-9_.-]` and `ValidState(s string) bool`
      for `idle|active|completed|blocked` — the single validators every package imports
- [ ] write tests: round-trip; replace-on-rebind (same row, same host+name); missing file →
      empty; corrupt file → error, not panic; `Dangling` set arithmetic; `token.Valid` table
      incl. `''`, `-x`, `a b`, unicode; `ValidState` accepts exactly the four states
- [ ] run `make check` — must pass before Task 4

### Task 4: `internal/receiver` — forwarded-socket listener and event decode

**Files:**
- Create: `internal/receiver/event.go`, `internal/receiver/listener.go`
- Create: `internal/receiver/dependency.go`, `internal/receiver/mocks/dependency_mock.go`
- Create: `internal/receiver/event_test.go`, `internal/receiver/listener_test.go`

- [ ] `event.go`: `Event{Host, Session, SessionID, State, Pane, PaneID string; Blink, AutoReset bool}`;
      `Decode(host string, line []byte) (Event, error)` accepting **both** shapes — agr
      (`"session"`) and cookbook (`"session_id"`, `"pane"`, `"pane_id"`); `args` array
      allowlisted to `--blink`/`--auto-reset`; unknown `cmd` → error; state must be one of
      `idle|active|completed|blocked`; session/session_id must pass `ValidToken`
- [ ] `dependency.go`: `StatusSink` interface `Status(ctx, target string, args agterm.StatusArgs) error`;
      `Resolver` interface `ByHostName(host, name string) (bindings.Binding, bool)`
- [ ] `listener.go`: `Listener{sock string; host string; sink; resolve}` — **one listener per
      host** on `~/.cache/agr/recv-<host>.sock` (the host's tunnel forwards to this socket, so
      the listener knows which host every line came from — a single shared socket could not
      distinguish two hosts' `api` sessions); `Serve(ctx)` accepts, per connection reads lines (`bufio.Scanner`, 64 KiB cap) until EOF,
      each line → `Decode` → agr shape: resolve binding → `sink.Status(row, args with PaneID
      from binding)`; cookbook shape: target = `session_id`, pane/pane_id passed through;
      errors logged per line, never fatal; `LastEvent()` timestamp for liveness
- [ ] write tests: decode table (both shapes, missing state, bad state, extra args dropped,
      malformed JSON, unknown cmd, bad token); listener with mock sink: two lines on one
      connection → two calls; unknown session → logged, no call; malformed line between two
      good ones → the good ones still delivered; oversized line → dropped
- [ ] run `make check` — must pass before Task 5

### Task 5: `internal/bridge` — per-host `ssh -N -R` supervisor

**Files:**
- Create: `internal/bridge/supervisor.go`, `internal/bridge/backoff.go`
- Create: `internal/bridge/dependency.go`, `internal/bridge/mocks/dependency_mock.go`
- Create: `internal/bridge/supervisor_test.go`, `internal/bridge/backoff_test.go`
- Create: `tests/bridge/supervisor_test.go`, `tests/bridge/testdata/ssh` (shim)

- [ ] `dependency.go`: `ProcessRunner` interface `Start(ctx, argv []string, logPath string) (Process, error)`;
      `Process` interface `Wait() error; Kill() error`. Concrete `ExecRunner` sets
      `Setpgid`, redirects stdout/stderr to the log, kills the process group
- [ ] `backoff.go`: `Next(prev time.Duration, connectedFor time.Duration) time.Duration` —
      1 s → ×2 → cap 30 s, ±20 % jitter, reset to 1 s when `connectedFor > 30 s`
- [ ] `supervisor.go`: `Supervisor{host, remoteSock, localSock string; runner}` — `localSock`
      is that host's `recv-<host>.sock`, `remoteSock` is `<remote home>/.cache/agr/bridge.sock`;
      `ssh` resolved once via `exec.LookPath` with `/usr/bin/ssh` fallback (launchd PATH is
      empty); builds argv `ssh -N -o BatchMode=yes -o ExitOnForwardFailure=yes -o StreamLocalBindUnlink=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -R <remoteSock>:<localSock> <host>`;
      `Run(ctx)` loop: start → state `Up` after 5 s alive (or first event via `MarkAlive()`)
      → on exit state `Down`, backoff, restart; `Stop()` cancels and kills; `State()` and a
      `Changes() <-chan State` channel for the daemon's HUD logic
- [ ] write unit tests with mock runner: restart after exit; backoff sequence; reset after long
      connection; `Stop` kills the running process; argv exactness
- [ ] write integration test `tests/bridge`: PATH shim `ssh` (`case "$*" in *-N*) exec sleep 300;; esac`)
      → start, assert `pgrep sleep`, `Stop`, assert gone; shim that exits 255 immediately →
      restarts observed with growing gaps
- [ ] run `make check` — must pass before Task 6

### Task 6: `internal/daemon` — assembly, control socket, liveness HUD, resync, events

**Files:**
- Create: `internal/daemon/daemon.go`, `internal/daemon/control.go`, `internal/daemon/resync.go`, `internal/daemon/events.go`
- Create: `internal/daemon/dependency.go`, `internal/daemon/mocks/dependency_mock.go`
- Create: `internal/daemon/daemon_test.go`, `internal/daemon/control_test.go`, `internal/daemon/resync_test.go`, `internal/daemon/events_test.go`

- [ ] `dependency.go`: `UI` interface `HudOpen(ctx, row, msg string) error; HudClose(ctx, row string) error`
      (via `agtermctl session hud … --target row --socket sock`); `Remote` interface
      `Home(ctx, host string) (string, error); Sessions(ctx, host string) ([]Session, error); EnsureDirs(ctx, host string) error`
      (`Session` is defined HERE as the consumer's type; Task 9's concrete package satisfies
      it — so this task builds and tests against mocks before `internal/remote` exists, and
      `cmd/agr` wires the concrete type in Task 10); `EventSource` interface
      `Closed(ctx) (<-chan string, error)` (row ids from `agtermctl events --json --kind session.closed`)
- [ ] `daemon.go`: single instance (flock on `~/.cache/agr/agrd.lock`, pidfile); on start:
      resolve `agterm.SocketPath()`/`CtlPath()`, `Version()` handshake logged (warn below
      `MinTestedVersion`), load bindings, and per host that has bindings: `EnsureDirs` once
      (`mkdir -p ~/.cache/agr` on the remote — `ExitOnForwardFailure` fails on a missing
      parent), start its `receiver.Listener` on `recv-<host>.sock` and its
      `bridge.Supervisor`; subscribe events; log to `~/.cache/agr/agrd.log`; SIGTERM/SIGINT →
      stop supervisors, close listeners, remove sockets + pidfile
- [ ] `control.go`: unix socket `~/.cache/agr/agrd.sock`, JSON lines
      `{"op":"up","host":…}` / `down` / `status` / `reload-bindings` → `{"ok":…,"result":…}`;
      `up` = EnsureDirs + listener + supervisor for a host not yet running; `down` = stop both,
      remove its recv socket; `status` reports per host: state, since, attempts, last event
- [ ] `resync.go`: on a supervisor's `Down→Up`: `HudClose` for that host's rows; `Sessions(host)`
      → for each binding with a live session and a non-empty level → `sink.Status(row, level
      with AutoReset for completed)`; on `Up→Down`: `HudOpen(row, "<host>: reconnecting…")`
      for each bound row
- [ ] `events.go`: consume `Closed` rows → `bindings.UnbindRow`, and if a host has no
      bindings left, stop its supervisor
- [ ] agterm-absent handling: on ECONNREFUSED from the sink, log once, poll `SocketPath()`
      every 5 s, on return re-run handshake + events subscription + full resync
- [ ] write tests: control protocol round-trip (each op, malformed line); resync with mocks
      (levels pushed with correct args, `completed` carries AutoReset, dangling ignored,
      HUD open/close ordering); events → unbind → supervisor stopped when last binding goes;
      single-instance lock refuses a second daemon; SIGTERM path removes sockets and pidfile;
      `EnsureDirs` called once per host per daemon lifetime
- [ ] run `make check` — must pass before Task 7

### Task 7: `remote/agr.sh` — POSIX script, tmux backend, shim harness

**Files:**
- Create: `remote/agr.sh`
- Create: `tests/remote/run.sh`, `tests/remote/lib.sh`, `tests/remote/tmux/*.sh`, `tests/remote/shims/nc`
- Modify: `Makefile` (`shellcheck`, `check-remote` real targets)

- [ ] `remote/agr.sh`: `#!/bin/sh`, `set -eu`; `AGR_VERSION="@VERSION@"` (substituted at
      embed time by Task 9); source `~/.config/agr/env` when present (`AGR_MUX`, `AGR_SOCK`,
      `ZMX_DIR`, `AGR_RELAY`), else defaults `AGR_MUX=tmux`, `AGR_SOCK=$HOME/.cache/agr/bridge.sock`,
      relay auto-detected `nc -U` → `python3` → `socat`; `valid_token`; verbs `attach <name>`, `sessions`, `reap <name>`,
      `status <state> [--blink] [--auto-reset]`, `--version`; dispatch `mux_*` on `$AGR_MUX`
- [ ] tmux backend: `attach` = `has-session -t "=$n" || new-session -d -s "$n"`;
      `set-option -t "=$n:" @agr 1`; `exec tmux attach -t "=$n"`. `sessions` = header
      `agr\t$AGR_VERSION` then per owned session (`@agr` = 1 **or** `@agr_target` non-empty)
      `name\tattached\tidle_secs\tcmds\tstate` where `state`/`idle_secs` come from
      `@agr_state` (`<state>@<epoch>`, `-`/`-` when unset), `cmds` as in 0.4 (`-` when empty).
      `reap` = refuse unowned, `kill-session -t "=$n"`, print `killed <n>`.
      `status` = `[ -n "${TMUX_PANE:-}" ] || exit 0`; owned check via
      `display-message -p -t "$TMUX_PANE" '#{@agr}#{@agr_target}'` → exit 0 if empty;
      `set-option -t "$TMUX_PANE" @agr_state "$state@$(date +%s)"` (session scope);
      `relay`; `exit 0` always
- [ ] `relay`: build `{"cmd":"session-status","session":"<name>","state":"<state>","args":[…]}`
      with `printf` (values already token-validated → no escaping needed); send via
      `$AGR_RELAY` = `nc` (`nc -U -w1 "$AGR_SOCK"`) | `python3` (one-liner) | `socat`
      (`socat - UNIX-CONNECT:"$AGR_SOCK"`); any failure → exit 0
- [ ] `tests/remote/lib.sh`: shim setup (`REAL_TMUX` resolved before PATH change; `tmux` shim
      injecting `-L agr-test`; `nc` shim appending stdin to `$NC_CAPTURE`); helpers
      `assert_eq`, `assert_contains` with names; `tests/remote/run.sh` runs every
      `tests/remote/*/*.sh`
- [ ] write tmux checks: attach marks `@agr`; sessions lists owned only and treats
      `@agr_target` as owned; TSV has exactly 5 fields and header; reap refuses unowned and
      kills exact name (`api` vs `api2`); status outside tmux exits 0 silently; status inside
      an owned session writes `@agr_state` and the nc capture holds the exact JSON line;
      status with `--blink --auto-reset` → `args` array; unowned session → no capture
- [ ] `shellcheck -s sh remote/agr.sh` clean; run `make check` — must pass before Task 8

### Task 8: `remote/agr.sh` — zmx backend and fake-zmx harness

**Files:**
- Modify: `remote/agr.sh`
- Create: `tests/remote/shims/zmx`, `tests/remote/zmx/*.sh`

- [ ] `tests/remote/shims/zmx`: a `sh` fake keeping sessions in `$ZMX_FAKE_DIR/<name>` files
      (`pid`, labels as `k=v` lines): implements `list [--short]` (real line format incl. `→`
      for `$ZMX_SESSION`), `set`, `get [key]`, `kill`, `attach [--labels …] <name>` (records
      the call, does not block), `attach --help` (with/without `--labels` via `ZMX_FAKE_LABELS`),
      `version` (prints `socket dir: $ZMX_FAKE_DIR`)
- [ ] zmx backend in `remote/agr.sh`: `attach` = `zmx set "$n" agr=1 2>/dev/null || true`;
      if `AGR_ZMX_LABELS=1` → `exec zmx attach --labels "agr=1" "$n"` else
      `( i=0; while [ $i -lt 10 ]; do sleep 0.2; zmx set "$n" agr=1 2>/dev/null && exit 0; i=$((i+1)); done ) &`
      then `exec zmx attach "$n"`. `sessions` = parse `zmx list` lines: strip `→ `, split on
      tabs, `name=`, `clients=`, labels `agr=`, `agr_state=`; `cmd` = deepest descendant of
      `pid=` via `pgrep -P` loop (max depth 8), else `-`. `reap` = `zmx get "$n" agr` non-empty
      else refuse; `zmx kill "$n"`. `status` = `[ -n "${ZMX_SESSION:-}" ] || exit 0`;
      `zmx get "$ZMX_SESSION" agr` empty → exit 0; `zmx set "$ZMX_SESSION" agr_state="$state@$(date +%s)"`; relay
- [ ] write zmx checks (fake on PATH, `AGR_MUX=zmx`): attach adopts + execs with/without
      `--labels`; retry path labels within 2 s when `--labels` unsupported; sessions parses
      `→`-prefixed and label-bearing lines, owned only, `state`/`idle` from the label; reap
      refuses unlabeled, kills labeled; status writes the label and the nc capture line;
      `ZMX_DIR` from env file is exported before any zmx call
- [ ] `shellcheck -s sh remote/agr.sh` clean; run `make check` — must pass before Task 9

### Task 9: `internal/remote` — embed, ssh runner, `sessions`/`reap`, install, hooks merge

**Files:**
- Create: `remote/embed.go` (package `remotescript`, `//go:embed agr.sh` — Go embed cannot reference parent directories, so the script and its embed live together at the module root and `internal/remote` imports the package), `internal/remote/runner.go`, `internal/remote/sessions.go`, `internal/remote/install.go`, `internal/remote/hooks.go`, `internal/remote/probe.go`, `internal/remote/hostinfo.go`
- Create: `internal/remote/dependency.go`, `internal/remote/mocks/dependency_mock.go`
- Create: `internal/remote/*_test.go`, `internal/remote/testdata/`

- [ ] `remote/embed.go`: `//go:embed agr.sh` → `remotescript.Script(version string) []byte`
      substituting `@VERSION@`
- [ ] `hostinfo.go`: `HostInfo{Home, Mux, Relay string; Mosh bool; AgrVersion, ZmxVersion,
      TmuxVersion string; ZmxLabels bool; ProbedAt time.Time}` persisted at
      `~/.cache/agr/hosts/<host>.json` by `install` (and refreshed by `doctor`); replaces the
      0.4 `home-<host>` / `mosh-<host>` files; `Home()` and `Mux()` read from it
- [ ] `dependency.go`: `SSH` interface `Run(ctx, host string, stdin []byte, argv ...string) ([]byte, error)`
      (argv-only; concrete impl execs `ssh -o BatchMode=yes host -- <argv…>`; a 255 exit maps
      to `ErrUnreachable`); `TTY` interface `Interactive(ctx, argv ...string) error` for
      `ssh -t`/`mosh` in `open`
- [ ] `runner.go`: `Home(ctx, host)` from `HostInfo` (probing once when absent);
      `EnsureDirs(ctx, host)` = constant argv `mkdir -p ~/.cache/agr ~/.config/agr ~/.local/bin`;
      `AgrPath(host) = <home>/.local/bin/agr`; `Data(ctx, host, verb, args…)` = run, parse
      header `agr\t<ver>` (missing → `ErrNotInstalled` with the install hint; mismatch →
      warning), propagate non-zero exit **after** the header check, return body
- [ ] `sessions.go`: `Session{Name, Attached int, IdleSecs int, Cmds, State string}`;
      `ParseSessions(body)` (5 tab fields, `-` for unknown idle/state); `Sessions(ctx, host)`;
      `Reap(ctx, host, name)` → `killed <name>` or the refusal error text
- [ ] `probe.go`: constant remote probe script (no interpolation) printing `k\tv` lines: `zmx`
      version or MISSING, `zmx_labels` yes/no (`zmx attach --help | grep -q -- --labels`),
      `zmx_dir` (from `zmx version`, login shell via `sh -lc`), `tmux` version or MISSING,
      `nc_u` yes/no (`nc -h 2>&1 | grep -q -- -U`), `python3`, `socat`, `mosh_server`, `home`,
      `agr` installed version, `sock` present, `legacy` targets count; `ParseProbe` → struct
- [ ] `install.go`: probe → `EnsureDirs` → choose `AGR_MUX` (zmx if present else tmux,
      `--mux` override), `AGR_RELAY` (nc-U > python3 > socat; none → error listing what to
      install), write `~/.config/agr/env` (constant script with stdin body; `AGR_SOCK=$HOME/.cache/agr/bridge.sock`),
      write script via `ssh … 'cat > ~/.local/bin/agr.tmp && chmod +x … && mv -f …'`
      (stdin = script), save `HostInfo`, merge hooks, print `installed <version> on <host> (mux=zmx, relay=nc)`
- [ ] `hooks.go`: `MergeHooks(existing []byte) (merged []byte, changed bool, err error)` — pure
      Go over `map[string]any`, the four events with `$HOME/.local/bin/agr status …` commands,
      idempotent (`agr status` already present in a bucket → skip), preserves unknown keys,
      `json.Indent` 2 spaces; remote read = constant script
      `p=$(readlink -f ~/.claude/settings.json 2>/dev/null || echo ~/.claude/settings.json); [ -e "$p" ] && cat "$p" || printf '{}'`,
      unparsable → refuse without writing; write = constant script: `.bak-agr` copy only when
      `changed`, tmp + `mv` beside the symlink target
- [ ] write tests: `Script` substitution; header parse table (missing/mismatch/ok/non-zero
      after header); `ParseSessions` (5 fields, empty body, `-` values); `ParseProbe`;
      `MergeHooks` table (missing file → `{}` input, foreign keys kept, idempotent second run,
      malformed → error, hooks already present in one bucket only); install decision table
      (mux/relay choice, none available → error); `HostInfo` round-trip; SSH mock asserting
      argv-only calls, `EnsureDirs` before any write, and the exact constant scripts as stdin
- [ ] run `make check` — must pass before Task 10

### Task 10: CLI — `open`, `up`, `down`, `ls`, `kill`; picker; context

**Files:**
- Create: `internal/cli/open.go`, `internal/cli/updown.go`, `internal/cli/ls.go`, `internal/cli/kill.go`, `internal/cli/pick.go`, `internal/cli/daemonclient.go`
- Create: `internal/cli/dependency.go`, `internal/cli/mocks/dependency_mock.go`
- Create: `internal/cli/*_test.go`, `internal/cli/testdata/ls_golden.txt`
- Modify: `cmd/agr/main.go` (wire subcommands)

- [ ] `daemonclient.go`: ensure daemon (connect to `agrd.sock`; if absent, `exec.Command(self, "daemon")`
      detached with `Setsid`, wait ≤3 s for the socket), `Up(host)`, `Down(host)`, `Status()`
- [ ] `dependency.go`: `Agterm` interface `Tree(ctx) (rows []string, err)`; `Rename(ctx,row,name)`;
      `Context(ctx,row,text)`; `Pick(ctx, items []PickItem, prompt string) (PickResult, error)`
      — all via `agtermctl` absolute path + `--socket`; `Pick` maps exit 2 → `Cancelled`
- [ ] `pick.go`: `PickItem{ID, Label, Subtitle}`; `DecodePick(out []byte, exit int)` →
      picked→id, custom→query (verbatim, then `ValidToken`), cancelled→`ErrCancelled`,
      other→error naming the exit; `ItemsFor(sessions, bindings, tree)` with subtitle
      `cmd · state · idle` mirroring `zmx tree`
- [ ] `cmd/agr/main.go`: wire the concrete `internal/remote` type into the daemon's `Remote`
      interface and the `agtermctl`-backed `UI`/`EventSource`/`Agterm` implementations
- [ ] `open.go`: args `<host> [name]`; no name → `Sessions` + `Pick` (no agtermctl → print
      `ls` and usage); validate name; **inside agterm** (`AGTERM_SESSION_ID` set): binding =
      `{Row, PaneID: $AGTERM_PANE_ID, Host, Name, Mux: HostInfo.Mux}` → `bindings.Bind` +
      daemon `reload-bindings` + `Up(host)`; **outside agterm**: no binding, no rename/context,
      still `Up(host)` and attach; `Rename(row,name)` +
      `Context(row, "host · name")` best-effort; then `mosh host -- <agrPath> attach name`
      (marker present) or `ssh -t host -- <agrPath> attach name` via `TTY.Interactive`; on
      return, terminal reset only if stdout is a tty
- [ ] `ls.go`: `Sessions` + bindings + `Tree` → table `NAME ATT IDLE STATE CMD ROW`
      (`ROW` = bound/stale/-; `-` when tree unavailable); footer `rows without a session: …`
      for `Dangling`; `no agr sessions on <host>` when empty
- [ ] `kill.go`: ≥1 name, each validated, `Reap`; non-zero if any failed. `updown.go`: thin.
      no `quick`: `master` reverted `agr quick` as untested with the quick terminal
      broken, so 1.0 does not carry it — a binding-based rewrite is a separate decision
- [ ] write tests: `DecodePick` table (picked/custom/space-in-custom rejected/cancelled/exit 1/
      malformed/empty id); `ItemsFor` subtitle golden; `ls` render golden incl. footer and
      tree-unavailable; `open` with mocks: binding written before `Up`, argv exactness for
      mosh vs ssh paths, picker cancel → exit 1 with no side effects; `kill` partial failure
      → non-zero; `open` outside agterm writes no binding and still attaches
- [ ] run `make check` — must pass before Task 11

### Task 11: `doctor`

**Files:**
- Create: `internal/cli/doctor.go`, `internal/cli/doctor_test.go`
- Modify: `cmd/agr/main.go`

- [ ] local block: agr version, agterm socket present, app version via handshake, `agtermctl`
      path + version, restore mode via `agtermctl restore mode --json` (recommend `live` when
      not; `n/a (agterm < 0.26)` when the command is unknown), mosh
- [ ] remote block from `Probe`: agr version vs local (hint on mismatch/missing), mux +
      version (+ `--labels` support for zmx, `ZMX_DIR` pinned), relay tool, bridge socket
      present, legacy count (0.3 `targets`, 0.4 `@agr_target`)
- [ ] daemon block from control `status`: per host state/since/attempts/last event; "not
      running" when absent
- [ ] write tests: rendering table from fixed probe/status structs (ok, mismatch, missing,
      daemon down, restore mode not live); no network in tests
- [ ] run `make check` — must pass before Task 12

### Task 12: Release config and README rewrite

**Files:**
- Create: `.goreleaser.yml`
- Modify: `README.md`, `Makefile` (`release-snapshot`), `.github/workflows/release.yml`
- Delete: `install.sh` (replaced by Homebrew/`go install`)

- [ ] `.goreleaser.yml`: `darwin/arm64`, `darwin/amd64`, `-ldflags -X main.version={{.Version}}`,
      archives, Homebrew tap `k0nsta/homebrew-tap` formula `agr`; `release.yml` on tag
- [ ] `README.md` rewrite, concise and example-first: problem, how it works (daemon, forwarded
      socket, levels, zmx/tmux), install (brew / go install), usage (`open`, `ls`, picker,
      `kill`, `doctor`), agterm 0.26 notes (Live sessions recommended, `session context`,
      why remote sessions don't replace agr), requirements per side, failure-mode table from
      the design, security, upgrading from 0.4/0.3
- [ ] `make release-snapshot` builds locally; `go run ./cmd/agr --version` shows the snapshot
      version
- [ ] verify: README code blocks match `agr --help` output; `make check` green

### Task 13: Verify acceptance criteria

- [ ] `make check` green on macOS locally; CI green on both runners
- [ ] `go vet ./...`, `golangci-lint` clean, `shellcheck -s sh remote/agr.sh` clean
- [ ] every consumer interface has a generated mock and no producer package defines one
- [ ] coverage ≥ 80 % on `internal/agterm`, `internal/receiver`, `internal/remote`, `internal/bindings`
- [ ] real-host checklist (Post-Completion) executed once end-to-end and recorded in this plan

### Task 14: [Final] Update documentation

- [ ] README reflects everything shipped (re-read once end to end)
- [ ] `docs/backlog/` reconciled: items 1.0 resolves are deleted, the rest kept
- [ ] move this plan to `docs/plans/completed/`

## Technical Details

**agterm wire (session.status):**
```json
{"cmd":"session.status","target":"A69B0B26-…","args":{"status":"blocked","paneID":"…","blink":true}}
→ {"ok":true,"result":{}}   |   {"ok":false,"error":"blocked status owned by pane left"}
```
One request per connection; 1 MiB cap; 2 s client timeout. Fallback exec only on a decode-failure reply.

**Event line (remote → daemon), both accepted:**
```json
{"cmd":"session-status","session":"api","state":"blocked","args":["--blink"]}
{"cmd":"session-status","state":"active","session_id":"A69B…","pane":"left","pane_id":"…","args":["--blink"]}
```

**bindings.json:** `[{"row":"A69B…","paneID":"…","host":"homelab","name":"api","mux":"zmx","boundAt":"2026-09-02T12:00:00Z"}]`

**Daemon control socket (`~/.cache/agr/agrd.sock`):** `{"op":"up|down|status|reload-bindings","host":"…"}` → `{"ok":true,"result":…}`.

**Remote env (`~/.config/agr/env`):** `AGR_MUX=zmx`, `AGR_RELAY=nc`, `AGR_SOCK=$HOME/.cache/agr/bridge.sock`, `ZMX_DIR=/run/user/1000/zmx`. Absent file → tmux, auto-detected relay, default socket.

**`sessions` TSV:** header `agr\t<version>`, then `name\tattached\tidle_secs\tcmds\tstate`; `idle_secs` = now − level epoch; `-` for unknown.

**Ownership / level:** zmx labels `agr=1`, `agr_state=<state>@<epoch>`; tmux options `@agr 1`, `@agr_state`; 0.4's `@agr_target` non-empty counts as owned.

**ssh supervisor argv:** `ssh -N -o BatchMode=yes -o ExitOnForwardFailure=yes -o StreamLocalBindUnlink=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -R <remote home>/.cache/agr/bridge.sock:<~/.cache/agr/recv-<host>.sock> <host>`; own process group; backoff 1→30 s ±20 %, reset after a >30 s connection. `mkdir -p ~/.cache/agr` is run on the remote once per host per daemon lifetime before the first tunnel.

**Paths (Mac):** `~/.cache/agr/{agrd.sock,agrd.lock,agrd.pid,agrd.log,bindings.json,recv-<host>.sock,bridge-<host>.log,hosts/<host>.json}`.
**Paths (remote):** `~/.local/bin/agr`, `~/.config/agr/env`, `~/.cache/agr/bridge.sock` (the forwarded receiver).

## Post-Completion

**Real-host checklist (run before each release; needs a Linux host with zmx and/or tmux, and agterm ≥ 0.26 in Live-sessions mode):**
1. `agr install <host>` prints version, mux, relay; `agr doctor <host>` all green, restore mode `live`.
2. `agr open <host> api` → row renamed, context `host · api`; `agr ls <host>` shows `api … bound`.
3. Agent in `api`: sidebar pulses active on prompt, blocked on permission prompt, completed on stop.
4. Toggle Wi-Fi off: HUD `host: reconnecting…` appears on the row within ~30 s; fire a status while offline; Wi-Fi on → HUD clears and the offline level lands.
5. Quit and relaunch agterm (Live mode): the `api` row returns still attached; colors resume on the next event.
6. `agr open <host>` (no name) → picker lists `api` with `cmd · state · idle`; type `new1` → created; cancel → quiet.
7. `zmx attach phone` / `tmux new -d -s phone` by hand → absent from `ls`; `agr kill <host> phone` refused.
8. Close the `api` row in agterm → `ls` shows row `-` within seconds; `agr kill <host> api new1` → gone.
9. `agr down <host>` → tunnel gone (`pgrep -f 'ssh.*recv-<host>'` empty); daemon `status` shows the host down.
10. zmx host: `agr ls` `CMD` shows the agent binary for a running agent (confirms `zmx list`'s `pid=` is the pane's process-tree root; if it is the daemon, `pgrep -P` needs one more hop — adjust and note here).

**Upgrade notes:** 0.4 hosts: run `agr install` (replaces the script, writes the env file); sessions marked `@agr_target` stay visible; delete `~/.cache/agterm/targets` on 0.3 hosts. Mac: remove the old `~/.local/bin/agr` symlink; `brew install k0nsta/tap/agr`.

**External:** publish the Homebrew tap; tag `v1.0.0`; zmx ≥ next release enables `attach --labels` (removes the retry path).
