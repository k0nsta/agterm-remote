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

Out of scope for 1.0: `agr quick` (reverted upstream as untested — see Context),
LaunchAgent install, a zmx-native `cmd` column, two-Macs handoff, an offline event queue,
any agr↔agr protocol, `golang.org/x/crypto/ssh`, and Linux builds of the Mac binary.

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
- **Verified `agtermctl` argv** (this Mac serves 0.25.0): `session status <state> [--blink]
  [--auto-reset] [--pane <role>] [--pane-id <id>] [--target] [--socket]`; `session hud open
  <message> [--detail] [--target] [--socket]`; `session hud close [--target] [--socket]`;
  `events --json --kind session.closed` (a bogus kind errors `invalid event kind`).
  **`session context` and `restore mode` do NOT exist on 0.25** — both are 0.26+, so every
  call site treats them as best-effort and `doctor` prints `n/a (agterm < 0.26)`.
- **agtermctl pick:** stdin JSON array `[{id,label,subtitle?}]`; prints ONE bare JSON result
  `{"result":"picked","id":…}` / `{"result":"custom","query":…}` (exit 0),
  `{"result":"cancelled"}` (exit 2), errors exit 1; `[]` only with `--allow-custom`.
- **Cookbook `container-agent-status` sender shape** (must be accepted verbatim):
  `{"cmd":"session-status","state":…,"session_id":…,"pane":…,"pane_id":…,"args":["--blink","--auto-reset"]}`.
- **zmx 0.7.1 facts (verified):** `zmx attach <name>` create-or-attach; `zmx list` lines
  `[→ ]name=X\tpid=N\tclients=N\tcreated=EPOCH[\tcwd=…][\tk=v…]`; `zmx list --short`;
  labels `zmx set <name> k=v…` / `zmx get <name> [key]` / `key=` deletes; `zmx kill <name>`;
  `zmx version` prints socket dir + log dir; `$ZMX_SESSION` inside a session; `attach
  --labels` exists in `main` but is unreleased → probe `zmx attach --help`; socket dir
  `ZMX_DIR` → `XDG_RUNTIME_DIR/zmx` → `TMPDIR/zmx-uid` → `/tmp/zmx-uid` (pin `ZMX_DIR` at
  install); IPC-changing upgrades kill sessions; no foreground-command query.
- **tmux facts:** `set-option`/`show-option` need `-t "=name:"` (trailing colon);
  `has-session`/`kill-session`/`list-panes -s` take `-t "=name"`;
  `display-message -p -t "$TMUX_PANE" '#{session_name}'` resolves the pane's session.
- **Relay tool:** OpenBSD `nc -U` (Debian/Ubuntu/Alpine); GNU/busybox `nc` lack `-U` →
  fallback `python3`, then `socat`.
- **Go facts verified in a scratch module (go1.26):** `//go:embed` beside its script in a
  package at the module root compiles and is importable; a DTO must be owned by ONE package
  (two `Session` types in two packages do not satisfy each other); `internal/remote`
  importing `internal/daemon` compiles but makes any future daemon→remote import a cycle,
  so the DTO lives in the producer; `Setpgid: true` + `syscall.Kill(-pgid, SIGTERM)` kills
  the whole group including grandchildren (3 procs → 0, no orphans); macOS `sun_path` is
  104 bytes and `t.TempDir()` + `recv-<host>.sock` already measures ~101.
- User Go conventions: consumer-defined 1–3-method interfaces in `dependency.go` with
  `//go:generate go run go.uber.org/mock/mockgen -source=dependency.go -destination=mocks/dependency_mock.go -package=mocks`;
  producers return concrete types; unit tests beside code, integration in `/tests`; every
  test helper takes `*testing.T` first and calls `t.Helper()`; no duplicated test utilities;
  CI runs the same `make` targets as local.

## Development Approach

- **testing approach**: Regular (code first, then tests) — every task still ends with its
  tests written and green before the next task starts.
- Go 1.22+, no CGO, `internal/` packages, `cmd/agr` the only entry point. `make check`
  (= `test lint shellcheck check-remote`) is the gate locally and in CI.
- **Type ownership:** `internal/remote` owns the `Session` DTO. Consumers define the
  *interfaces* (per the convention) but speak in the producer's data types, so no package
  ever needs a back-import. Every implementation carries a compile-time assertion
  (`var _ daemon.Remote = (*remote.Runner)(nil)`) in the task that introduces it.
- The Mac side **never builds a shell string**: `ssh`, `mosh`, `agtermctl` run with argv.
  The only shell text is the constant remote scripts in `internal/remote` and the embedded
  `internal/remotescript/agr.sh`.
- All cache paths come from an injected `Dirs` value honouring `XDG_CACHE_HOME`
  (as 0.4 did), so every test points at a temp dir.
- **CRITICAL: every task MUST include new/updated tests** for the code it adds or changes,
  covering success and failure paths, listed as separate checklist items.
- **CRITICAL: all tests must pass before starting next task.**
- **CRITICAL: update this plan file when scope changes during implementation.**
- Backward compatibility: `sessions` treats a tmux session with non-empty `@agr_target`
  (0.4 marker) as owned; `~/.cache/agterm/targets` (0.3) is ignored.

## Testing Strategy

- **unit tests** (beside code): one shared fake-agterm unix server helper (Task 3, reused by
  Tasks 7/9/10 — sockets under `os.MkdirTemp("/tmp", …)` to stay inside the 104-byte
  `sun_path` limit); fake `ProcessRunner`; table tests for event decode, pick decode, TSV
  parse, hooks merge, bindings store, token validation.
- **integration** (`/tests`): `tests/bridge/` — PATH-shim `ssh` with a unique argv marker,
  real process supervision; `tests/remote/` — shim-driven checks for `agr.sh` with
  `tmux -L agr-test`, a fake `zmx`, and a fake `nc` capturing the line; `shellcheck -s sh`.
  Both run on macOS + Linux CI. No test requires a live host or a running agterm.
- **e2e**: none applies; the real-host checklist in Post-Completion is run by hand.

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
- Create: `go.mod`, `cmd/agr/main.go`, `cmd/agr/run.go`, `cmd/agr/run_test.go`
- Create: `Makefile`, `.golangci.yml`, `.github/workflows/ci.yml`
- Modify: `.gitignore` (add `dist/`, `bin/`, `coverage.out`)

- [ ] `go mod init github.com/k0nsta/agterm-remote`; `run.go` exposes
      `run(args []string, out, errw io.Writer) int` with a package-level `version` var
      (`-ldflags -X main.version=…`, default `dev`); `main.go` is `os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))`
      so every CLI test calls `run` directly instead of exec'ing a binary
- [ ] `Makefile`: `build`, `test` (`go test -race ./...`), `cover`
      (`go test -coverprofile=coverage.out ./... && go tool cover -func`), `lint`,
      `shellcheck` and `check-remote` (both `[ -f … ] || { echo "skip (not yet)"; exit 0; }`
      guarded until Tasks 11/13 create their targets), `check` = all
- [ ] `.golangci.yml`: default linters plus `depguard` forbidding `internal/remote` from
      importing `internal/daemon` (the cycle guard the type-ownership rule exists to keep)
- [ ] `.github/workflows/ci.yml`: matrix `macos-latest` + `ubuntu-latest`, installs
      `golangci-lint` (official action), `shellcheck`, `tmux`, then runs `make check`
- [ ] write tests: `run` with `--version` prints the injected version to `out`; unknown
      subcommand → usage on `errw`, exit 2; no args → usage, exit 0
- [ ] run `make check` — must pass before Task 2

### Task 2: `internal/token` — validation for names, states and hosts

**Files:**
- Create: `internal/token/token.go`, `internal/token/token_test.go`

- [ ] `Valid(s)` — `[A-Za-z0-9_.-]`, **rejects** empty, a leading `-` or `.`, and exactly
      `.`/`..` (closes `docs/backlog/valid_token-allows-leading-dash-dot.md`: 1.0 is where
      these reach `--target`/`--pane-id` argv and filenames)
- [ ] `ValidState(s)` — exactly `idle|active|completed|blocked`
- [ ] `ValidHost(s)` — an ssh destination, so it must accept `user@example.com` and `host-1`:
      printable ASCII without whitespace, quotes, or shell metacharacters, no leading `-`
- [ ] `FileKey(host)` — the filename-safe form used for `recv-<key>.sock`,
      `hosts/<key>.json`, `bridge-<key>.log`: `Valid`-safe characters kept, everything else
      replaced, plus a short hash suffix so two hosts can never collide (closes
      `docs/backlog/unsanitised-host-sid-in-cache-filenames.md`)
- [ ] write tests: `Valid` table stating the verdict for each — `""` reject, `-x` **reject**,
      `.` / `..` reject, `a b` reject, `a-b_c.d` accept, unicode reject; `ValidState` four
      accepts + rejects; `ValidHost` accepts `user@host`, `h1.example.com`, rejects
      `a b`, `a;b`, `-h`; `FileKey` is stable, filename-safe, and collision-free for
      `user@h` vs `user_h`
- [ ] run `make check` — must pass before Task 3

### Task 3: `internal/agterm` — wire types, direct socket client, version handshake, exec fallback

**Files:**
- Create: `internal/agterm/protocol.go`, `internal/agterm/client.go`, `internal/agterm/socket.go`
- Create: `internal/agterm/dependency.go`, `internal/agterm/mocks/dependency_mock.go`
- Create: `internal/agterm/client_test.go`, `internal/agterm/socket_test.go`, `internal/agterm/testserver_test.go`

- [ ] `protocol.go`: `Request{Cmd string; Target string,omitempty; Args any,omitempty}`,
      `StatusArgs{Status string; Blink *bool "blink,omitempty"; AutoReset *bool "autoReset,omitempty"; Pane string "pane,omitempty"; PaneID string "paneID,omitempty"}`,
      `Response{OK bool; Result json.RawMessage; Error string}`; `ErrRefused` (error text
      prefix `blocked status owned by pane`); `IsDecodeFailure(err)` matching **both**
      spellings `invalid request` and `DecodingError` — the exact text is an unverified
      heuristic, so a miss costs only the fallback, never correctness. Assert the JSON tags
      with a marshal golden test.
- [ ] `socket.go`: `SocketPath()` → `AGTERM_CONTROL_SOCKET` → `AGTERM_SOCKET` →
      `$AGTERM_STATE_DIR/agterm.sock` → `~/Library/Application Support/agterm/agterm.sock`;
      `CtlPath()` → the app-bundle path if executable, else `exec.LookPath`, resolved once
- [ ] `client.go`: `Client{sock, ctl string; timeout time.Duration; run Runner}`;
      `Do(ctx, Request) (Response, error)` = one dial, write line, read one line, close;
      `Version(ctx) (string, error)`; `Status(ctx, target string, args StatusArgs) error`
      returning `ErrRefused` on the 0.26 refusal, and on a decode failure calling
      `run.Run(ctx, ctl, "session","status",args.Status,"--target",target,"--socket",sock, [+"--pane" role, +"--pane-id" id, +"--blink", +"--auto-reset"])`
      on **every** such reply (events must not be dropped); the "falling back to agtermctl"
      warning is logged once per process (`sync.Once`)
- [ ] `dependency.go`: `Runner` interface `Run(ctx, name string, args ...string) error` +
      `//go:generate` mockgen line; generate mocks
- [ ] `MinTestedVersion = "0.25.0"`: a lower `Version()` result warns once
- [ ] `testserver_test.go`: **the one shared fake** — `newFakeAgterm(t *testing.T, handler)`
      with `t.Helper()`, listening under `os.MkdirTemp("/tmp", …)` (never `t.TempDir()`:
      macOS `sun_path` is 104 bytes and the temp path alone measures ~101). Exported for
      Tasks 7/9/10 via a shared `internal/agterm/agtermtest` package so no test utility is
      duplicated across packages.
- [ ] write tests: ok reply; refused → `ErrRefused`; two decode-failure replies → fallback
      Runner called **twice** with exact argv, warning once; ECONNREFUSED → error, no
      fallback; `Version` parses `result.app.version` and warns below 0.25.0; timeout
      honored; `SocketPath` precedence table
- [ ] run `make check` — must pass before Task 4

### Task 4: `internal/agterm/ctl.go` — the concrete `agtermctl` adapters

**Files:**
- Create: `internal/agterm/ctl.go`, `internal/agterm/ctl_test.go`

- [ ] `Ctl{path, sock string; run Runner}` — one type providing every non-hot-path call, all
      with the absolute binary path and an explicit `--socket` (launchd `PATH` is empty):
      `HudOpen(ctx,row,msg)` → `session hud open <msg> --target <row> --socket <s>`;
      `HudClose(ctx,row)` → `session hud close --target … --socket …`;
      `Rename(ctx,row,name)`; `Context(ctx,row,text)` (**best-effort**: 0.25 has no
      `session context`, so an unknown-subcommand failure is logged, never fatal);
      `Tree(ctx) ([]string, error)` (live row ids via `tree --json`);
      `Pick(ctx, items []PickItem, prompt string) (PickResult, error)`;
      `ClosedRows(ctx) (<-chan string, error)` streaming `events --json --kind session.closed`
- [ ] `PickItem{ID,Label,Subtitle}`; `DecodePick(out []byte, exit int)` → `picked`→id,
      `custom`→query verbatim, exit 2 or `cancelled`→`ErrCancelled`, other→error naming the exit
- [ ] write tests with a mocked `Runner` asserting exact argv for every method; `DecodePick`
      table (picked / custom / cancelled / exit 1 / malformed / empty id); `Context` failing
      with "unknown subcommand" returns nil (best-effort); `ClosedRows` yields rows from
      canned JSON lines and closes cleanly on ctx cancel
- [ ] run `make check` — must pass before Task 5

### Task 5: `internal/paths` and `internal/bindings` — injectable dirs, row ↔ (host, name, mux) store

**Files:**
- Create: `internal/paths/paths.go`, `internal/paths/paths_test.go`
- Create: `internal/bindings/store.go`, `internal/bindings/store_test.go`

- [ ] `paths.Dirs{Cache string}` — `New()` honours `XDG_CACHE_HOME` else `~/.cache`, then
      `/agr`; accessors `Sock()`, `Lock()`, `Pid()`, `Log()`, `Bindings()`,
      `Recv(hostKey)`, `BridgeLog(hostKey)`, `HostInfo(hostKey)`. Every consumer takes a
      `Dirs`, so tests point at a temp dir.
- [ ] `Binding{Row, PaneID, Pane, Host, Name, Mux string; BoundAt time.Time}` — `Pane` (the
      role, from `$AGTERM_PANE`) is stored beside `PaneID` because 0.26's refusal names a
      role and `--pane-id` falls back to `--pane`
- [ ] `Store{dirs}` with `Load`, `Save` (tmp + rename), `Bind` (replaces any binding for the
      same Row **and** any for the same Host+Name), `UnbindRow`, `ByHostName`, `ByRow`,
      `ForHost`, `Dangling(host, live []string)`, `Reconcile(liveRows []string) (removed int)`
- [ ] write tests: round-trip; replace-on-rebind; missing file → empty; corrupt file → error
      not panic; `Dangling` and `Reconcile` set arithmetic; `Dirs` honours `XDG_CACHE_HOME`
- [ ] run `make check` — must pass before Task 6

### Task 6: `internal/remote/sessions.go` — the `Session` DTO and TSV parser

**Files:**
- Create: `internal/remote/sessions.go`, `internal/remote/sessions_test.go`

- [ ] **This package owns the `Session` type** — it is a dependency-free leaf, created here
      so Tasks 9–10 can compile their interfaces against `[]remote.Session` without any
      package importing `internal/daemon` back (verified: two `Session` types in two
      packages do not satisfy each other, and the back-import is one step from a cycle)
- [ ] `Session{Name string; Attached int; IdleSecs int; Cmds, State string}`
- [ ] `ParseSessions(body []byte) ([]Session, error)` — 5 tab-separated fields, `-` for an
      unknown idle/state, tolerant of a trailing newline, rejects a wrong field count
- [ ] write tests: well-formed multi-row body; empty body → empty slice, no error; `-`
      values; wrong field count → error naming the line; a name failing `token.Valid` → error
- [ ] run `make check` — must pass before Task 7

### Task 7: `internal/receiver` — per-host forwarded-socket listener and event decode

**Files:**
- Create: `internal/receiver/event.go`, `internal/receiver/listener.go`
- Create: `internal/receiver/dependency.go`, `internal/receiver/mocks/dependency_mock.go`
- Create: `internal/receiver/event_test.go`, `internal/receiver/listener_test.go`

- [ ] `event.go`: `Event{Host, Session, SessionID, State, Pane, PaneID string; Blink, AutoReset bool}`;
      `Decode(host string, line []byte) (Event, error)` accepting **both** shapes — agr
      (`"session"`) and cookbook (`"session_id"`, `"pane"`, `"pane_id"`); `args` allowlisted
      to `--blink`/`--auto-reset`; unknown `cmd` → error; state via `token.ValidState`;
      session name via `token.Valid`
- [ ] `dependency.go`: `StatusSink` interface `Status(ctx, target string, args agterm.StatusArgs) error`;
      `Resolver` interface `ByHostName(host, name string) (bindings.Binding, bool)`;
      `Liveness` interface `MarkAlive()` (the supervisor's; wired in Task 10)
- [ ] `listener.go`: **one listener per host** on `dirs.Recv(hostKey)` — the host's tunnel
      forwards to this socket, so the listener knows which host every line came from; a
      single shared socket could not tell two hosts' `api` sessions apart. `Serve(ctx)`
      accepts, and per connection reads with **`bufio.Reader`**: a line longer than 64 KiB is
      logged and **skipped up to the next `\n`**, keeping the connection usable
      (`bufio.Scanner` cannot do this — on overflow it stops the stream permanently).
      Each decoded line → resolve → `sink.Status` (+ `MarkAlive`); errors logged per line,
      never fatal. `LastEvent()` timestamp for liveness.
- [ ] write tests: decode table (both shapes, missing/bad state, extra args dropped,
      malformed JSON, unknown cmd, bad token); listener with mocks: two lines on one
      connection → two calls; unknown session → logged, no call; malformed line between two
      good ones → both good ones delivered; **oversized line → skipped and the next line on
      the same connection still delivered**; `MarkAlive` called on a delivered event
- [ ] run `make check` — must pass before Task 8

### Task 8: `internal/bridge` — per-host `ssh -N -R` supervisor

**Files:**
- Create: `internal/bridge/supervisor.go`, `internal/bridge/backoff.go`
- Create: `internal/bridge/dependency.go`, `internal/bridge/mocks/dependency_mock.go`
- Create: `internal/bridge/supervisor_test.go`, `internal/bridge/backoff_test.go`
- Create: `tests/bridge/supervisor_test.go`, `tests/bridge/testdata/ssh` (shim)

- [ ] `dependency.go`: `ProcessRunner` interface `Start(ctx, argv []string, logPath string) (Process, error)`;
      `Process` interface `Wait() error; Stop(grace time.Duration) error`. The concrete
      `ExecRunner` sets `Setpgid: true`; `Stop` sends **SIGTERM to the group**
      (`syscall.Kill(-pgid, …)`), waits `grace`, then SIGKILLs the group — verified to reap
      grandchildren with no orphans, and the bounded wait keeps `Stop` from hanging on an
      ssh that ignores TERM
- [ ] `backoff.go`: `Next(prev, connectedFor) time.Duration` — 1 s → ×2 → cap 30 s, ±20 %
      jitter, reset to 1 s when `connectedFor > 30 s`
- [ ] `supervisor.go`: `Supervisor{host, hostKey, remoteSock, localSock, sshPath string; runner}`
      — `sshPath` is resolved **per supervisor** (`exec.LookPath`, `/usr/bin/ssh` fallback),
      never a process-wide `sync.Once`, so a PATH shim takes effect and cannot leak between
      tests; argv `ssh -N -o BatchMode=yes -o ExitOnForwardFailure=yes -o StreamLocalBindUnlink=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -R <remoteSock>:<localSock> <host>`;
      `Run(ctx)` loop start → `Up` after 5 s alive or on `MarkAlive()` → on exit `Down`,
      backoff, restart; `Stop()`; `State()`; `Changes() <-chan State`
- [ ] write unit tests with the mock runner: restart after exit; backoff sequence; reset
      after a long connection; `Stop` calls `Process.Stop`; argv exactness; `MarkAlive`
      promotes to `Up` before the 5 s timer
- [ ] write integration test `tests/bridge`: PATH shim `ssh` exec'ing
      `sleep 3000 # agr-bridge-test-<uniq>`; assert with `pgrep -f agr-bridge-test-<uniq>`
      (never a bare `pgrep sleep`, which matches unrelated processes); `Stop` → gone;
      a shim exiting 255 immediately → restarts with growing gaps
- [ ] run `make check` — must pass before Task 9

### Task 9: `internal/daemon` — core, lifecycle, stale-socket recovery

**Files:**
- Create: `internal/daemon/daemon.go`, `internal/daemon/dependency.go`, `internal/daemon/sockets.go`
- Create: `internal/daemon/mocks/dependency_mock.go`
- Create: `internal/daemon/daemon_test.go`, `internal/daemon/sockets_test.go`

- [ ] `dependency.go` — every interface consumer-defined, speaking the producers' data types
      (no package imports `internal/daemon`): `UI` `HudOpen(ctx,row,msg) error; HudClose(ctx,row) error`;
      `Remote` `Home(ctx,host) (string,error); Sessions(ctx,host) ([]remote.Session,error); EnsureDirs(ctx,host) error`;
      `EventSource` `ClosedRows(ctx) (<-chan string,error)`; `Rows` `Tree(ctx) ([]string,error)`;
      `StatusSink` `Status(ctx,target string,args agterm.StatusArgs) error`. Generate mocks.
- [ ] `sockets.go`: `ListenClean(path)` — before `net.Listen`, dial the existing socket file;
      if the dial fails it is stale → unlink, then listen. Without this a `SIGKILL`ed or
      crashed daemon can never restart (`bind: address already in use`), since the SIGTERM
      path is otherwise the only cleanup.
- [ ] `daemon.go`: single instance (flock on `dirs.Lock()`, pidfile); on start resolve
      `agterm.SocketPath()`/`CtlPath()`, `Version()` handshake logged (warn below
      `MinTestedVersion`), load bindings, **reconcile them against `Rows.Tree`** (rows that
      vanished while the daemon was down — e.g. an agterm relaunch — are unbound here, so
      resync never pushes at dead targets), then per host with bindings: `EnsureDirs` once
      (`mkdir -p ~/.cache/agr` remotely — `ExitOnForwardFailure` fails on a missing parent),
      start its listener via `ListenClean` and its supervisor, wiring the listener's
      `MarkAlive` to that supervisor; log to `dirs.Log()`; SIGTERM/SIGINT → stop supervisors,
      close listeners, remove sockets + pidfile
- [ ] write tests: single-instance lock refuses a second daemon; a **leftover** socket file
      does not block startup while a **live** one does; SIGTERM removes sockets and pidfile;
      `EnsureDirs` called once per host per lifetime; startup reconciliation unbinds rows
      absent from `Tree`
- [ ] run `make check` — must pass before Task 10

### Task 10: `internal/daemon` — control socket, resync, close events, agterm-absent recovery

**Files:**
- Create: `internal/daemon/control.go`, `internal/daemon/resync.go`, `internal/daemon/events.go`
- Create: `internal/daemon/control_test.go`, `internal/daemon/resync_test.go`, `internal/daemon/events_test.go`

- [ ] `control.go`: unix socket `dirs.Sock()`, JSON lines
      `{"op":"up|down|status|reload-bindings","host":…}` → `{"ok":…,"result":…}`; `up` =
      EnsureDirs + listener + supervisor for a host not yet running; `down` = stop both and
      remove that recv socket; `status` = per host state, since, attempts, last event
- [ ] `resync.go`: on `Down→Up` — `HudClose` each of that host's rows, `Sessions(host)`, then
      for every binding whose session is live and whose level is non-empty
      `StatusSink.Status(row, level)` (`completed` carries `AutoReset`); on `Up→Down` —
      `HudOpen(row, "<host>: reconnecting…")` for each bound row
- [ ] `events.go`: consume `EventSource.ClosedRows` → `bindings.UnbindRow`; when a host has
      no bindings left, stop its supervisor and listener
- [ ] agterm-absent handling: on ECONNREFUSED from the sink, log once, poll `SocketPath()`
      every 5 s; when it returns, re-run the handshake, re-subscribe `ClosedRows`,
      **re-reconcile bindings against `Tree`**, and resync every host
- [ ] write tests: control round-trip for each op + malformed line; resync with mocks (levels
      pushed with correct args, `completed` carries AutoReset, dangling ignored, HUD
      open/close ordering); close event → unbind → supervisor stopped when the last binding
      goes; agterm-absent → single log, recovery path re-reconciles and resyncs
- [ ] run `make check` — must pass before Task 11

### Task 11: `internal/remotescript/agr.sh` — POSIX script, tmux backend, shim harness

**Files:**
- Create: `internal/remotescript/agr.sh`, `internal/remotescript/embed.go`, `internal/remotescript/embed_test.go`
- Create: `tests/remote/run.sh`, `tests/remote/lib.sh`, `tests/remote/tmux/*.sh`, `tests/remote/shims/nc`
- Modify: `Makefile` (`shellcheck`, `check-remote` now real)

- [ ] `embed.go`: package `remotescript` with `import _ "embed"` (required for a
      `string`/`[]byte` target) and `//go:embed agr.sh`; `Script(version string) []byte`
      substitutes `@VERSION@`. It lives **under `internal/`** so it is not public API and the
      package name matches its directory.
- [ ] `agr.sh`: `#!/bin/sh`, `set -eu`; `AGR_VERSION="@VERSION@"`; source
      `~/.config/agr/env` when present (`AGR_MUX`, `AGR_SOCK`, `ZMX_DIR`, `AGR_RELAY` — the
      file uses `export` so values reach the `zmx`/`tmux` children), else defaults
      `AGR_MUX=tmux`, `AGR_SOCK=$HOME/.cache/agr/bridge.sock`, relay auto-detected
      `nc -U` → `python3` → `socat`; `valid_token`; verbs `attach <name>`, `sessions`,
      `reap <name>`, `status <state> [--blink] [--auto-reset]`, `--version`; dispatch on `$AGR_MUX`
- [ ] tmux backend: `attach` = `has-session -t "=$n" || new-session -d -s "$n"`;
      `set-option -t "=$n:" @agr 1`; `exec tmux attach -t "=$n"`. `sessions` = header
      `agr\t$AGR_VERSION` then per owned session (`@agr` = 1 **or** `@agr_target` non-empty)
      `name\tattached\tidle_secs\tcmds\tstate`, where `state`/`idle_secs` come from
      `@agr_state` (`<state>@<epoch>`; `-`/`-` when unset) and `cmds` is `-` when empty.
      **`reap` prints the `agr\t$AGR_VERSION` header first** (it is a data verb and the Mac's
      `Data()` requires the handshake), then refuses unowned, `kill-session -t "=$n"`,
      `killed <n>`.
- [ ] tmux `status`: `[ -n "${TMUX_PANE:-}" ] || exit 0`; **resolve the session name**
      `n=$(tmux display-message -p -t "$TMUX_PANE" '#{session_name}')` — the event line
      carries it and the daemon resolves bindings by `(host,name)`, so without it every tmux
      event is unresolvable; ownership check via `show-option -t "=$n:" -qv @agr`/`@agr_target`
      → exit 0 if empty; `set-option -t "=$n:" @agr_state "$state@$(date +%s)"` (the
      `-t "=name:"` form throughout, matching the documented rule); `relay`; `exit 0` always
- [ ] `relay`: `{"cmd":"session-status","session":"<name>","state":"<state>","args":[…]}`
      built with `printf` (values already token-validated); sent via `$AGR_RELAY`
      (`nc -U -w1 "$AGR_SOCK"` | `python3` one-liner | `socat - UNIX-CONNECT:"$AGR_SOCK"`);
      any failure → exit 0
- [ ] `tests/remote/lib.sh`: shim setup (`REAL_TMUX` resolved **before** the PATH change;
      `tmux` shim injecting `-L agr-test`; `nc` shim appending stdin to `$NC_CAPTURE`);
      `assert_eq`/`assert_contains`; `run.sh` runs every `tests/remote/*/*.sh`
- [ ] write tmux checks: attach marks `@agr`; sessions lists owned only, treats `@agr_target`
      as owned, emits exactly 5 fields after the header; reap emits the header, refuses
      unowned, kills the exact name (`api` vs `api2`); status outside tmux exits 0 silently;
      status inside an owned session writes `@agr_state` **and the captured JSON line carries
      the right `session` name**; `--blink --auto-reset` → `args`; unowned → no capture
- [ ] `shellcheck -s sh` clean; `make check` — must pass before Task 12

### Task 12: `agr.sh` — zmx backend and fake-zmx harness

**Files:**
- Modify: `internal/remotescript/agr.sh`
- Create: `tests/remote/shims/zmx`, `tests/remote/zmx/*.sh`

- [ ] `tests/remote/shims/zmx`: an `sh` fake keeping sessions in `$ZMX_FAKE_DIR/<name>`
      (pid + `k=v` label lines): `list [--short]` in the real line format (incl. `→` for
      `$ZMX_SESSION`), `set`, `get [key]`, `kill`, `attach [--labels …] <name>` (records the
      call, does not block), `attach --help` (with/without `--labels` per `ZMX_FAKE_LABELS`),
      `version` (prints `socket dir: $ZMX_FAKE_DIR`)
- [ ] zmx backend: `attach` = `zmx set "$n" agr=1 2>/dev/null || true` (adopts an existing
      session), then `exec zmx attach --labels "agr=1" "$n"` when `AGR_ZMX_LABELS=1`, else a
      bounded background retry (10 × 200 ms `zmx set`) before `exec zmx attach "$n"` — the
      create-then-label race zmx's own help text names. `sessions` parses `zmx list` (strip
      `→ `, tab fields, `name=`, `clients=`, labels `agr=`/`agr_state=`); `cmd` is
      best-effort — deepest descendant of `pid=` via `pgrep -P` (max depth 8), else `-`.
      `reap` = header, `zmx get "$n" agr` non-empty else refuse, `zmx kill`. `status` =
      `[ -n "${ZMX_SESSION:-}" ] || exit 0`, ownership via `zmx get`, level via `zmx set`, relay.
- [ ] write zmx checks: attach adopts and execs with/without `--labels`; the retry path
      labels within 2 s; sessions parses `→`-prefixed and label-bearing lines, owned only,
      state/idle from the label; reap refuses unlabeled and emits the header; status writes
      the label and captures the line; **`ZMX_DIR` from the env file is exported before any
      `zmx` call**
- [ ] `shellcheck -s sh` clean; `make check` — must pass before Task 13

### Task 13: `internal/remote` — runner, host info, embed wiring

**Files:**
- Create: `internal/remote/runner.go`, `internal/remote/hostinfo.go`
- Create: `internal/remote/dependency.go`, `internal/remote/mocks/dependency_mock.go`
- Create: `internal/remote/runner_test.go`, `internal/remote/hostinfo_test.go`

- [ ] `dependency.go`: `SSH` interface `Run(ctx, host string, stdin []byte, argv ...string) ([]byte, error)`
      (argv-only; the concrete impl execs `ssh -o BatchMode=yes host -- <argv…>`, mapping a
      255 exit to `ErrUnreachable`); `TTY` interface `Interactive(ctx, argv ...string) error`
      for `ssh -t`/`mosh` — **`TTY` must NOT set `Setpgid`**, since an interactive attach
      needs the terminal's foreground process group
- [ ] `hostinfo.go`: `HostInfo{Home, Mux, Relay string; Mosh bool; AgrVersion, ZmxVersion, TmuxVersion string; ZmxLabels bool; ProbedAt time.Time}`
      at `dirs.HostInfo(token.FileKey(host))`; replaces 0.4's `home-<host>`/`mosh-<host>` files
- [ ] `runner.go`: `Runner{ssh; dirs}` with `Home(ctx,host)` (from `HostInfo`, probing once
      when absent), `EnsureDirs(ctx,host)` = constant argv
      `mkdir -p ~/.cache/agr ~/.config/agr ~/.local/bin`, `AgrPath(host)`,
      `Data(ctx,host,verb,args…)` = run, parse the `agr\t<ver>` header (missing →
      `ErrNotInstalled` with the install hint; mismatch → warning), propagate a non-zero exit
      **after** the header check, return the body; `Sessions` = `Data` + `ParseSessions`;
      `Reap(ctx,host,name)` = `Data` (so it needs the header Task 11 adds)
- [ ] add the compile-time assertion `var _ daemon.Remote = (*Runner)(nil)` — in
      `cmd/agr`'s wiring file (not here: `internal/remote` must never import
      `internal/daemon`, which `.golangci.yml`'s depguard enforces)
- [ ] write tests: header parse table (missing / mismatch / ok / non-zero after header);
      `Home` caches and probes once; `EnsureDirs` argv exactness; `Reap` surfaces the remote
      refusal text; `HostInfo` round-trip
- [ ] run `make check` — must pass before Task 14

### Task 14: `internal/remote` — probe, install, hooks merge

**Files:**
- Create: `internal/remote/probe.go`, `internal/remote/install.go`, `internal/remote/hooks.go`
- Create: `internal/remote/probe_test.go`, `internal/remote/install_test.go`, `internal/remote/hooks_test.go`, `internal/remote/testdata/`

- [ ] `probe.go`: one constant remote script (no interpolation) printing `k\tv` lines — `zmx`
      version or MISSING, `zmx_labels` (`zmx attach --help | grep -q -- --labels`), `zmx_dir`
      (from `zmx version`, run through a **login** shell `sh -lc` so it matches the user's
      interactive resolution), `tmux`, `nc_u` (`nc -h 2>&1 | grep -q -- -U`), `python3`,
      `socat`, `mosh_server`, `home`, installed `agr` version, `sock` present, `legacy`
      counts; `ParseProbe` → struct
- [ ] `install.go`: probe → `EnsureDirs` → choose `AGR_MUX` (zmx if present else tmux,
      `--mux` override) and `AGR_RELAY` (nc-U > python3 > socat; none → error naming what to
      install) → write `~/.config/agr/env` with **`export` on every line** and
      **`ZMX_DIR` pinned** from the probe, plus `AGR_SOCK=$HOME/.cache/agr/bridge.sock` →
      write the script via a constant `cat > …agr.tmp && chmod +x … && mv -f …` with the
      embedded bytes on stdin → save `HostInfo` → merge hooks → print
      `installed <version> on <host> (mux=zmx, relay=nc)`
- [ ] `hooks.go`: `MergeHooks(existing []byte) (merged []byte, changed bool, err error)` —
      pure Go over `map[string]any`, the four events with `$HOME/.local/bin/agr status …`,
      idempotent, unknown keys preserved, `json.Indent` 2 spaces. Remote read is a constant
      script `p=$(readlink -f ~/.claude/settings.json 2>/dev/null || echo ~/.claude/settings.json); [ -e "$p" ] && cat "$p" || printf '{}'`;
      unparsable → refuse without writing; write = constant script with `.bak-agr` **only
      when `changed`**, tmp + `mv` beside the symlink target
- [ ] write tests: `ParseProbe`; install decision table (mux/relay choice, none → error, env
      file contains `export` and `ZMX_DIR`); `MergeHooks` table (missing file → `{}` input,
      foreign keys kept, idempotent second run, malformed → error, one bucket already wired);
      SSH mock asserting argv-only calls, `EnsureDirs` before any write, and the exact
      constant scripts as stdin
- [ ] run `make check` — must pass before Task 15

### Task 15: CLI — daemon client, `ls`, `kill`, picker items

**Files:**
- Create: `internal/cli/daemonclient.go`, `internal/cli/ls.go`, `internal/cli/kill.go`, `internal/cli/pick.go`
- Create: `internal/cli/dependency.go`, `internal/cli/mocks/dependency_mock.go`
- Create: `internal/cli/*_test.go`, `internal/cli/testdata/ls_golden.txt`

- [ ] `dependency.go` — narrow, consumer-defined (1–3 methods each): `Picker`
      `Pick(ctx,[]agterm.PickItem,string) (agterm.PickResult,error)`; `Rows` `Tree(ctx) ([]string,error)`;
      `Labeler` `Rename(ctx,row,name) error; Context(ctx,row,text) error`; `Sessions`
      `Sessions(ctx,host) ([]remote.Session,error); Reap(ctx,host,name) error`
- [ ] `daemonclient.go`: connect to `dirs.Sock()`; if absent, start `agr daemon` detached
      (`Setsid`) and wait ≤3 s for the socket; `Up`, `Down`, `Status`, `ReloadBindings`
- [ ] `ls.go`: `Sessions` + bindings + `Tree` → table `NAME ATT IDLE STATE CMD ROW`
      (`ROW` = bound/stale/-, `-` when the tree is unavailable); footer
      `rows without a session: …` from `Dangling`; `no agr sessions on <host>` when empty
- [ ] `kill.go`: ≥1 name, each `token.Valid`, `Reap` each; non-zero if any failed
- [ ] `pick.go`: `ItemsFor(sessions, bindings, tree)` → `{id: name, label: name, subtitle: "cmd · state · idle"}`,
      mirroring `zmx tree`'s shape
- [ ] write tests: `ls` render golden incl. footer and tree-unavailable; `ItemsFor` subtitle
      golden; `kill` partial failure → non-zero, invalid name reported not sent;
      `daemonclient` spawns once and gives up cleanly if the socket never appears
- [ ] run `make check` — must pass before Task 16

### Task 16: CLI — `open`, `up`/`down`, `install`, `daemon`, and `cmd/agr` wiring

**Files:**
- Create: `internal/cli/open.go`, `internal/cli/updown.go`, `internal/cli/install.go`
- Create: `cmd/agr/wire.go`, `cmd/agr/wire_test.go`
- Modify: `cmd/agr/run.go` (register every subcommand), `internal/cli/open_test.go`, `internal/cli/install_test.go`

- [ ] `run.go` registers **all** subcommands: `open ls kill up down install doctor daemon`
      (`doctor` lands in Task 17) — `install` and `daemon` are required by the daemon client
      and by Post-Completion step 1, and neither existed before this task
- [ ] `install.go`: `agr install <host> [--mux zmx|tmux]` → `remote.Install`
- [ ] `open.go`: `<host> [name]`; no name → `Sessions` + `ItemsFor` + `Pick` (no `agtermctl`
      → print `ls` then usage); validate the name; **inside agterm** (`AGTERM_SESSION_ID`
      set) bind `{Row, PaneID: $AGTERM_PANE_ID, Pane: $AGTERM_PANE, Host, Name, Mux: HostInfo.Mux}`
      → `ReloadBindings` → `Up(host)` → `Rename` + `Context` (both best-effort);
      **outside agterm** no binding, no rename/context, still `Up` and attach; then
      `mosh host -- <agrPath> attach name` (marker present) or `ssh -t host -- <argv>` via
      `TTY.Interactive`; terminal reset on return only when stdout is a tty
- [ ] `cmd/agr/wire.go`: build the concrete `agterm.Ctl`, `remote.Runner`, `bindings.Store`
      and hand them to the daemon and CLI; carry the compile-time assertions
      `var _ daemon.Remote = (*remote.Runner)(nil)`, `var _ daemon.UI = (*agterm.Ctl)(nil)`,
      `var _ daemon.EventSource = (*agterm.Ctl)(nil)`, `var _ daemon.Rows = (*agterm.Ctl)(nil)`,
      `var _ daemon.StatusSink = (*agterm.Client)(nil)` so a mismatch fails in this task
      rather than three tasks later
- [ ] write tests: `run` dispatches every registered subcommand (table over argv → handler);
      `open` with mocks — binding written before `Up`, argv exactness for the mosh and ssh
      paths, picker cancel → exit 1 with no side effects, outside agterm writes no binding
      and still attaches, a failing `Context` (agterm 0.25) does not fail `open`
- [ ] run `make check` — must pass before Task 17

### Task 17: `doctor`

**Files:**
- Create: `internal/cli/doctor.go`, `internal/cli/doctor_test.go`
- Modify: `cmd/agr/run.go`

- [ ] local block: agr version, agterm socket present, app version via handshake, `agtermctl`
      path + version, restore mode via `agtermctl restore mode --json` (recommend `live`;
      **`n/a (agterm < 0.26)`** when the subcommand is unknown), `session context` support
      reported the same way, mosh
- [ ] remote block from `ParseProbe`: agr version vs local (hint on mismatch/missing), mux +
      version (+ `--labels` support and the pinned `ZMX_DIR` for zmx), relay tool, bridge
      socket present, legacy counts (0.3 `targets`, 0.4 `@agr_target`)
- [ ] daemon block from control `status`: per host state/since/attempts/last event, or
      "not running"
- [ ] write tests: rendering from fixed probe/status structs (ok, mismatch, missing, daemon
      down, restore mode not live, restore mode unsupported); no network in tests
- [ ] run `make check` — must pass before Task 18

### Task 18: Release config, README, and retiring the bash tool

**Files:**
- Create: `.goreleaser.yml`, `.github/workflows/release.yml`
- Modify: `README.md`, `Makefile` (`release-snapshot`)
- Delete: `install.sh`, and the root bash `agr`

- [ ] `.goreleaser.yml`: `darwin/arm64` + `darwin/amd64`, `-ldflags -X main.version={{.Version}}`,
      archives, Homebrew tap `k0nsta/homebrew-tap` formula `agr`; `release.yml` on tag
- [ ] **delete the root bash `agr` and `install.sh` in this task** — 1.0 replaces both, and
      leaving a 660-line bash `agr` beside `internal/remotescript/agr.sh` would ship two
      tools with one name (users who ran `install.sh` also keep a `~/.local/bin/agr` symlink
      pointing at it; the README upgrade note tells them to remove it)
- [ ] `README.md` rewrite, concise and example-first: problem, how it works (daemon,
      forwarded socket per host, levels, zmx/tmux), install (brew / `go install`), usage
      (`open`, `ls`, picker, `kill`, `doctor`), **a note that `IDLE` is now time since the
      last agent event, not terminal inactivity, and that there is no `WIN` column**,
      agterm 0.26 notes (Live sessions recommended, `session context`, why agterm's own
      remote sessions don't replace agr), requirements per side, the failure-mode table,
      security, upgrading from 0.4/0.3
- [ ] `make release-snapshot` builds; `go run ./cmd/agr --version` shows the snapshot version
- [ ] verify: README code blocks match `agr --help`; `make check` green

### Task 19: Verify acceptance criteria

- [ ] `make check` green on macOS locally; CI green on both runners
- [ ] `go vet ./...`, `golangci-lint` clean (incl. the depguard cycle rule),
      `shellcheck -s sh internal/remotescript/agr.sh` clean
- [ ] every consumer interface has a generated mock; no producer package defines an interface
      for its own consumers; every wiring assertion in `cmd/agr/wire.go` compiles
- [ ] `make cover` ≥ 80 % on `internal/agterm`, `internal/receiver`, `internal/remote`,
      `internal/bindings`, `internal/token`
- [ ] real-host checklist (Post-Completion) executed once end-to-end and recorded here

### Task 20: [Final] Update documentation

- [ ] README reflects everything shipped (re-read once end to end)
- [ ] `docs/backlog/` reconciled — **delete** `no-bats-or-ci.md` (CI + two test suites ship),
      `no-doctor-relay-probe.md` (doctor reports the relay tool and bridge socket),
      `relay-uses-python-not-nc-socat.md` (nc-U preferred), `valid_token-allows-leading-dash-dot.md`
      and `unsanitised-host-sid-in-cache-filenames.md` (Task 2); **keep**
      `no-agr-uninstall.md` and `no-zombie-session-cleanup.md`, which 1.0 does not address
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

**bindings.json:** `[{"row":"A69B…","paneID":"…","pane":"left","host":"homelab","name":"api","mux":"zmx","boundAt":"2026-09-02T12:00:00Z"}]`

**Daemon control socket (`<cache>/agr/agrd.sock`):** `{"op":"up|down|status|reload-bindings","host":"…"}` → `{"ok":true,"result":…}`.

**Remote env (`~/.config/agr/env`), every line `export`ed:** `AGR_MUX=zmx`, `AGR_RELAY=nc`, `AGR_SOCK=$HOME/.cache/agr/bridge.sock`, `ZMX_DIR=/run/user/1000/zmx`. Absent file → tmux, auto-detected relay, default socket.

**`sessions` / `reap` TSV:** both lead with `agr\t<version>`; `sessions` then emits `name\tattached\tidle_secs\tcmds\tstate`, `idle_secs` = now − level epoch (**time since the last agent event**, not terminal inactivity), `-` for unknown.

**Ownership / level:** zmx labels `agr=1`, `agr_state=<state>@<epoch>`; tmux options `@agr 1`, `@agr_state` (both set with `-t "=name:"`); 0.4's `@agr_target` non-empty counts as owned.

**ssh supervisor argv:** `ssh -N -o BatchMode=yes -o ExitOnForwardFailure=yes -o StreamLocalBindUnlink=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -R <remote home>/.cache/agr/bridge.sock:<cache>/agr/recv-<hostKey>.sock <host>`; own process group, SIGTERM to the group then SIGKILL after a grace; backoff 1→30 s ±20 %, reset after a >30 s connection. `mkdir -p ~/.cache/agr` runs remotely once per host per daemon lifetime before the first tunnel.

**Paths (Mac, all from `paths.Dirs`, honouring `XDG_CACHE_HOME`):** `<cache>/agr/{agrd.sock,agrd.lock,agrd.pid,agrd.log,bindings.json,recv-<hostKey>.sock,bridge-<hostKey>.log,hosts/<hostKey>.json}`, where `hostKey = token.FileKey(host)`.
**Paths (remote):** `~/.local/bin/agr`, `~/.config/agr/env`, `~/.cache/agr/bridge.sock` (the forwarded receiver).

## Post-Completion

**Real-host checklist (run before each release; needs a Linux host with zmx and/or tmux, and agterm ≥ 0.26 in Live-sessions mode):**
1. `agr install <host>` prints version, mux, relay; `agr doctor <host>` all green, restore mode `live`.
2. `agr open <host> api` → row renamed, context `host · api`; `agr ls <host>` shows `api … bound`.
3. Agent in `api`: sidebar pulses active on prompt, blocked on permission prompt, completed on stop.
4. Toggle Wi-Fi off: HUD `host: reconnecting…` appears within ~30 s; fire a status while offline; Wi-Fi on → HUD clears and the offline level lands.
5. Quit and relaunch agterm (Live mode): the `api` row returns still attached; colors resume on the next event; no binding was pushed at a dead row.
6. `agr open <host>` (no name) → picker lists `api` with `cmd · state · idle`; type `new1` → created; cancel → quiet.
7. `zmx attach phone` / `tmux new -d -s phone` by hand → absent from `ls`; `agr kill <host> phone` refused.
8. Close the `api` row in agterm → `ls` shows row `-` within seconds; `agr kill <host> api new1` → gone.
9. `agr down <host>` → tunnel gone (`pgrep -f 'ssh.*recv-'` empty); daemon `status` shows the host down.
10. `kill -9` the daemon, then `agr ls <host>` → the daemon restarts over the stale sockets rather than failing to bind.
11. zmx host: `agr ls` `CMD` shows the agent binary for a running agent (confirms `zmx list`'s `pid=` is the pane's process-tree root; if it is the daemon, `pgrep -P` needs one more hop — adjust and note here).

**Upgrade notes:** 0.4 hosts: run `agr install` (replaces the script, writes the env file); sessions marked `@agr_target` stay visible; delete `~/.cache/agterm/targets` on 0.3 hosts. Mac: remove the old `~/.local/bin/agr` symlink left by `install.sh`; `brew install k0nsta/tap/agr`.

**External:** publish the Homebrew tap; tag `v1.0.0`; zmx ≥ next release enables `attach --labels` (removes the retry path).
