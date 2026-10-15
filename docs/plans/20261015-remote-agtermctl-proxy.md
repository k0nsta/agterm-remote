# Remote agtermctl proxy

## Overview

On a row opened with `agr open`, programs run on the remote host. The host has no `agtermctl`
and no `AGTERM_*` environment, so every agterm-aware tool refuses to run or falls back to
something else. That includes the user's skills (`decide`, `spawn-agent`, `teardown-agent`,
`autoimplementation`, `agterm/show-image.sh`) and anyone else's.

This plan makes a remote row look like agterm to those tools:

- **Proxy on the host**: `agr install` puts an **`agtermctl` proxy** there. It forwards each
  call over the existing bridge to the Mac: the argv, the local files it names, and stdin
  where a command reads it.
- **Real agtermctl on the Mac**: the daemon checks the call against a **per-command allowlist
  with an option schema**, rewrites it, runs the real `agtermctl`, and streams stdout, stderr
  and the exit code back.
- **Environment**: `agr open` exports the `AGTERM_*` variables into the remote session, so
  tools see themselves as inside agterm. For ownership, the binding is the source of truth,
  not those variables.

```sh
# on homelab, inside an agr row:
agtermctl session overlay open --html /tmp/panel.html --js --block --target "$AGTERM_SESSION_ID"
{"outcome":"submitted","value":"…"}
```

What this enables:

- **Remote UI**: `decide` panels and `show-image` overlays from a remote Claude.
- **Messaging**: `session text` / `session type` between panes the host owns. Mac → any row
  already works today.
- **Remote spawn**: `agtermctl session new --command …` from the host becomes a new row
  attached to a new agr session on the **same host**, which runs the command there. The new
  row is bound right away.
- **Workers on a host from the Mac**: `agr open <host> <name> --command …` is the building
  block local tools use.

Known limits, documented rather than hidden:

- **Foreground reads `agr`**: a remote row's `foreground` in `tree --json` is the Mac-side
  `agr`, not the remote program, so tools that poll it see `agr`. `spawn-agent` does this
  (`spawn-agent.sh:160-183`), so it reports "not running"; see Post-Completion.
- **Single-file pages**: HTML pages must be one self-contained file.
- **Restricted commands**: only the commands and options in the allowlist work from a host.
  Everything else fails with a clear message.

Decided by the user (not open):

- **Scope**: all four items above.
- **Generic**: an agtermctl proxy, not agr-specific verbs. This supersedes the earlier
  `agr overlay` plan and reuses its reviewed transport rules.
- **Trust, scoped**: a host may read, may write only to panes bound to it (including ones it
  created), and is refused anything that runs or reads something on the Mac.

## Context (from discovery)

- **Bridge**:
  - `internal/remotescript/agr.sh` `relay()` sends status lines to `$AGR_SOCK`.
  - `internal/receiver/listener.go` `serveConn` reads JSON lines (64 KB max) and never writes
    back. An unknown `cmd` is logged and skipped, then the next line is read.
  - `Decode` accepts `session` (resolved via `Resolver.ByHostName`) or `session_id` (a direct
    row with no ownership check, `event.go:63-67`). The proxy never uses `session_id`.
- **Daemon** (`internal/daemon`):
  - one `receiver.Listener` per host (`daemon.go:560`)
  - goroutines tracked via `runtime.done` / `d.children`; `stopHost` waits up to
    `teardownTimeout` (5 s)
  - bindings are removed on row close (`events.go:97-121`), on `agr end` (`cli/end.go:72`), or
    when a status push gets "no such session" (`daemon.go:382`)
  - a detached or ended `agr open` leaves its binding, while the pane falls back to a local Mac
    shell
- **agterm package**: `internal/agterm/exec.go` has `ExecRunner.Output` (returns at exit;
  `-1` when killed or missing) and `ExecRunner.Stream` (`exec.go:65`). `Ctl` owns `CtlPath`
  and `--socket`.
- **`agr open`** (`internal/cli/open.go`): records the binding from `AGTERM_SESSION_ID`,
  `AGTERM_PANE` and `AGTERM_PANE_ID`, then runs `ssh -t host ~/.local/bin/agr attach <name>
  [opts]`. An older remote `attach` rejects unknown options with `usage 2`
  (`agr.sh:623`, `open.go:41-42`). `HostInfo.AgrVersion` holds the installed remote version
  (`remote/hostinfo.go:21`).
- **Bindings** (`internal/bindings/store.go:17-28`) are **per pane**: `Row`, `PaneID`, `Pane`,
  `Host`, `Name`. A split row can have one pane bound to a host and a local shell in the other.
- **Remote install**: `internal/remote/install.go:15` (`remoteScriptWrite`). Doctor facts come
  from `internal/remote/probe.go` and `testdata/probe.txt`.
- **agtermctl 0.36.0 facts the design relies on**:
  - every leaf takes `--window` and `--socket`, and options parse as `--opt value` or
    `--opt=value`
  - `--target` defaults to `active`; `--pane-id` overrides `--pane`
  - `events --run` is a resume cursor and runs nothing
  - `pick open` has no `--target` and reads choices from stdin with no `--stdin` flag
  - `pick cancel` / `ask cancel` need an id that a blocking open never prints
  - commands that run or read something on the Mac:
    - `session scratch --command`, `session restart`, `session duplicate`
    - `session split visibility on` (spawns a pane)
    - `session paste` (sends the Mac clipboard)
    - `session reveal`, `session overlay navigate browser|finder`, `session overlay run-job`
    - `session hud --file`, `session background image`
    - `--url … --persistent/--browse`
    - `session new --command`, `session overlay open COMMAND`, `session restore CMD`
- **Remote tests**: `tests/remote/{tmux,zmx}` with `lib.sh`.
- **homelab** uses `AGR_RELAY=python3`; the Mac daemon running now predates 1.2.0.

## Development Approach

- **testing approach**: Regular (code first, then tests)
- complete each task fully before moving to the next; small, focused changes
- **CRITICAL: every task MUST include new/updated tests**, success and error paths
- **CRITICAL: all tests must pass before starting next task** (`make check`)
- **CRITICAL: update this plan file when scope changes during implementation**
- **default deny**: a leaf or option that isn't in the allowlist is refused
- status events keep working exactly as before. An old daemon, an old remote, a missing
  bridge, or a refused call fails with a clear message and a non-zero exit, never a hang.

## Testing Strategy

- **unit (Go)**:
  - wire codec
  - listener dispatch
  - argv parser (agtermctl grammar)
  - allowlist and schema: every allowed leaf, refused leaves and options, `--opt=value`
  - ownership: per pane, attached-pane check, self-id mapping
  - rewrites and the exact Mac-side command strings, including quoting
  - streaming runner, cancellation, concurrency cap, temp-file lifetime
  - `agr open --command` / env export with remote-version gating
  - `agr run`
- **remote (sh)**: `tests/remote/{tmux,zmx}/proxy_test.sh` run the installed proxy against a
  fake python3 bridge server.
- Every test-only helper takes `*testing.T` first and calls `t.Helper()`.
- Real agterm round trips are a Post-Completion manual check on `homelab`.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- keep the plan in sync with the code

## Solution Overview

```
host:  tool → agtermctl (proxy: sh wrapper → agr ctl → python3 client)
         └─ header {"cmd":"ctl","v":1,"session":…,"self":{…},"cwd":…,"argv":[…],"files":[n…],"stdin":n}
                                                          ──► bridge.sock
                                                          ◄── "ack 1\n"   (header valid: new daemon)
         └─ body: file bytes, stdin bytes                 ──►
Mac:   Listener (cmd == "ctl") → binding(host, session) = caller pane
         └─ CtlSink.Ctl(reqCtx, req, frames)  (the daemon)
              1. parse argv with agtermctl's grammar; exact leaf + option schema, else refuse
              2. ownership: explicit full-id targets/panes, bound to this host and attached
              3. rewrite: --target/--pane-id injected, files → temp, commands → agr on host
              4. run real agtermctl (streaming), cancel hook on client EOF/shutdown
         ◄── frames "o <n>\n…" stdout · "e <n>\n…" stderr · "x <code>\n" exit
```

- **Connection** (reused from the reviewed overlay plan):
  - one connection per call, never half-closed
  - the daemon sends `ack 1\n` **right after a valid header**, before reading the body; the
    client sends the body only after the ack
  - so an old daemon never sees body bytes (it can't misread them as status lines), and a slow
    upload can't trip the ack timeout
  - body errors arrive as `e` + `x 1`
  - on the daemon side, EOF means the client is gone; the request ctx derives from the Serve ctx;
    cleanup runs on `context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)`
- **Streaming**: a new `agterm` method runs agtermctl with stdin, stdout and stderr pipes, built
  on `ExecRunner`. It is consumed through an interface in `internal/daemon/dependency.go`.
- **Cancellation**: client EOF or shutdown kills the local agtermctl, then runs the leaf's cancel
  hook (`overlay open --block` → `session overlay close --target <row> --pane-id <p>`).
- **Ownership** is per pane, and only while attached. A write is allowed only on a pane that:
  1. has a binding for this host, and
  2. is still running `agr open` (its `foreground[0]` in `tree --json` is `agr`, or `ssh`/`mosh`
     if the spike shows those). This check is what closes the detached-pane hole.

  Row-level operations (`session close`, `rename`, `context`, `status`) need every pane of the
  row that exists to be bound to this host and attached.
- **Concurrency**: at most 16 calls in flight per host; the 17th gets `e` + `x 1`.

## Technical Details

### Wire

- **Header** (one line, ≤ 64 KB):
  ```json
  {"cmd":"ctl","v":1,"session":"name","self":{"row":"<AGTERM_SESSION_ID>","pane_id":"<AGTERM_PANE_ID>"},
   "cwd":"/home/u/proj","argv":["session","overlay","open","--html","@0","--js","--block"],
   "files":[1234],"stdin":0}
  ```
  - `session` must pass `token.Valid`. `session_id` is refused.
  - `files` holds byte counts only. No names are sent, so no path ever comes from the host.
  - Limits: at most 8 files, 8 MiB in total; stdin at most 1 MiB.
  - The body is read with `io.ReadFull` on the same `bufio.Reader`, under a 60 s read deadline
    that is cleared afterwards.
- **Reply**: `ack 1\n`, then `o`/`e` frames, then exactly one `x <code>\n`.
  - agtermctl's exit codes pass through.
  - An internal failure (`-1`, missing binary) is 1.
  - A refusal is `e agtermctl (via agr): <reason>\n` then `x 1`.

### Argv parsing and allowlist (Mac, `internal/proxy`)

- **Parser**: agtermctl's grammar.
  - The subcommand path comes first.
  - Options take the forms `--opt value` and `--opt=value`, may repeat, and may follow
    positionals; `--` ends the options.
  - Anything ambiguous is refused.
- **Allowlist**: exact leaves only. Each leaf has an option schema:
  - `row` options: must be a full id or a self marker
  - `pane` options: a pane id
  - `file` options: the value must be `@<i>` with a valid index
  - `command` options and positionals: rewritten
  - plain options: allowed values
  - `stdin` behaviour
  - a cancel hook

  An option that is not in the schema is refused.
- **Stripped everywhere**: `--window` and `--socket`. The daemon adds the real `--socket`.

Allowlist, driven by what the four skills use; anything else is added only when asked for:

| leaf | class | notes |
| --- | --- | --- |
| `version`, `help …`, `--help` | read | |
| `tree` (`--json`) | read | |
| `events` (`--json`, `--run`, `--after`) | read | streams the Mac's whole event feed (README Security) |
| `session text` | read | row/pane must be bound to this host; `--pane-id` injected |
| `session overlay result`, `session overlay text` | read | owned row |
| `session type` (`--stdin` reads stdin) | write, pane | `--pane-id` injected; target pane owned and attached |
| `session status`, `session context`, `session rename` | write, row | owned row |
| `session hud open/update/close` (message options only, no `--file`) | write, row | owned row |
| `session overlay open --html @i [--js] [--block] [--size-percent] [--pane-id]` | write, pane | file uploaded; `--cwd` dropped; cancel hook = overlay close |
| `session overlay open COMMAND…` | write, pane | COMMAND → `agr run` (see Rewrites) |
| `session overlay open --url URL` | write, pane | `--persistent` and `--browse` refused; localhost hosts warned (a UX hint, not a security control) |
| `session overlay close`, `session overlay submit`, `session overlay resize` | write, pane | owned |
| `session new` (`--name`, `--cwd`, `--command`, `--workspace`, `--no-select`, `--wait`) | create | rewritten to `agr open` on the host; the new row is pre-bound |
| `session close` (repeated `--target`) | write, row | every target owned |
| `session restore CMD` | write, pane | rewritten to `agr open … --command CMD` |
| `notify` | write | message only; targets the caller's row |
| `session split visibility on/off` | write, row | only on an owned row; the spike confirms ⌘D's `pane.split` hook makes the new pane remote (`agr on-split`) and not a local shell, otherwise refused |

Refused, with the reason named: `pick`, `ask`, `dashboard`, `quick`, `workspace`, `window`,
`sidebar`, `font`, `keymap`, `hooks`, `config`, `theme`, `browser`, `restore`, `terminfo`,
`clipboard`, `zmx`, and in `session`: `duplicate`, `select`, `go`, `reveal`, `move`, `swap`,
`lead`, `scratch`, `focus`, `copy`, `paste`, `select-all`, `restart`, `flag`, `seen`, `search`,
`background`, `split close`, `overlay reload/navigate/run-job/copy`, `hud --file`.

### Targets and panes

- **Target ids**: only **full ids** (UUID form, case-insensitive) are accepted. Prefixes and
  `active` are refused; a remote caller would almost never mean the user's selected row.
- **Self markers**: a `--target` equal to the header's `self.row`, or a `--pane-id` equal to
  `self.pane_id`, is mapped to the caller's **current** binding (row and pane id).
  - So a stale `AGTERM_*` value inherited from an earlier attach still hits the right pane.
  - A missing `--target` means the caller's row; a missing `--pane-id` on pane-class leaves
    means the caller's pane.
- **Injection**: the daemon always passes an explicit `--target <full id>` and, for pane-class
  leaves, `--pane-id <bound pane id>`, overriding any `--pane`. A `--pane`/`--pane-id` that
  doesn't map to a pane bound to this host is refused.
- **Attachment** is checked against one `tree --json` read just before the call. There is still
  a small window between that read and the agtermctl call, which is accepted and documented;
  ids are full, so it can't redirect to another row.

### Rewrites

- **Files**: `@i` becomes `<cache>/proxy/<random>/<i>.html` (mode 0600; the extension depends
  on the leaf).
  - A blocking call removes its directory when agtermctl returns.
  - A non-blocking `--html` open keeps it, so reloads still work; a sweep removes directories
    older than 24 h, and daemon start clears the rest.
- **`<agr>`** is the stable absolute path `agr setup` already writes (the brew symlink, e.g.
  `/opt/homebrew/bin/agr`, never a Cellar path).
- **Quoting has two layers**, both specified and tested:
  1. the shell line agterm runs **on the Mac** (`--command` / overlay COMMAND); every embedded
     value is single-quoted with a Go helper (`'` → `'\''`)
  2. the remote command that `agr open --command` / `agr run` sends over ssh, quoted with the
     existing `remote.QuoteRemoteCommand`

  Tests cover `'`, `"`, `;`, `$()`, a backtick and a newline in both CMD and DIR, asserting the
  exact strings.
- **`session new [--name N] [--cwd D] [--command C] [--workspace W] [--no-select] [--wait]`**
  becomes `session new [--name N] [--workspace W] [--no-select] [--wait] --command
  "<agr> open <host> <gen> [--cwd D] [--command C]"`.
  - `<gen>` is a valid token from `N`, else the caller's session name plus `-<n>`. It is checked
    against `Remote.Sessions(host)` so it never reattaches to an existing session (which would
    skip `C`).
  - After agtermctl prints the new row id, the daemon **pre-binds** it: `{Row: id, PaneID:
    <primary pane id from tree>, Pane: left, Host: host, Name: gen}`. A following `restore` or
    `type` from the host is then authorised immediately. `agr open` re-binds the same pane when
    it starts.
- **`session overlay open COMMAND…`** becomes `session overlay open "<agr> run <host> --cwd
  <cwd> -- COMMAND…"`. `<cwd>` is the header's `cwd` (the host's `$PWD`), not agterm's Mac
  default.
- **`session restore CMD`** on an owned pane becomes `session restore "<agr> open <host> <name>
  --command CMD" --pane-id <p>`. After a restart, agterm reattaches, and `CMD` runs only if the
  remote session is gone.
- **`--url`**: `localhost`, `127.*`, `[::1]` and `0.0.0.0` get a warning on stderr (localhost
  means the Mac). It is not a security control.

### `agr open` / `agr run` / attach (Mac and remote)

- **`agr open <host> <name> [--cwd D] [--command CMD]`**: `CMD` runs only when the remote
  session is new. Attaching to an existing session prints a notice and keeps it.
- **Environment**: `attach --env K=V …` carries `AGTERM_ENABLED=1`, `AGTERM_SESSION_ID`,
  `AGTERM_PANE`, `AGTERM_PANE_ID`, `AGTERM_WORKSPACE_ID`, `AGTERM_WINDOW_ID` (from `agr open`'s
  own environment at open time) and `AGTERM_SOCKET=agr-proxy`. Values are checked as UUIDs or
  tokens.
  - tmux: `new-session -e` on create, `set-environment` on re-attach (spike)
  - zmx: per the spike
- **Remote-version gating**: `--env` and `--command` are sent only when `HostInfo.AgrVersion`
  is at least this release.
  - Older host: no env, and `--command` is refused with `run agr install <host>`.
  - Tests cover both cases.
- **`agr run <host> [--cwd D] -- argv…`** (new Mac verb, `internal/cli/run.go`): a one-shot
  `ssh -t` running argv in D with the open/attach quoting. Its exit code is passed through.

### Remote proxy (`agr ctl`, python3)

- **Install**: `~/.local/bin/agtermctl` is a sh wrapper, `exec "$HOME/.local/bin/agr" ctl
  "$@"`, written by `remoteScriptWrite`.
  - `agr doctor` probes whether the host's `agtermctl` in a login shell is the proxy, and
    whether `~/.local/bin` is on PATH there.
- **Arguments**: `agr ctl` finds file options from a small table (`--html`); each must be a
  readable regular file and is replaced with `@i`. stdin is forwarded when argv has `--stdin`.
  The header carries `self` from the env and `cwd` from `$PWD`.
- **Session lookup**: new side-effect-free `tmux_current_session` / `zmx_current_session`;
  `tmux_status`/`zmx_status` are rebuilt on them.
- **Messages**:
  - bridge down: `agtermctl (via agr): the bridge to the Mac is down (run agr up <host> on the
    Mac)`
  - no ack in 5 s: `agtermctl (via agr): the Mac's agr daemon is too old or not running`
  - EOF after the ack: `agtermctl (via agr): the bridge to the Mac dropped`
  - outside an agr session: `agtermctl (via agr): not inside a row opened with agr open`
- Ctrl-C exits 130 quietly.

## What Goes Where

- **Implementation Steps**: code, tests, README in this repo.
- **Post-Completion**: manual round trips on `homelab`, a skill sweep, the release.

## Implementation Steps

### Task 1: Spike — environment, attachment signal, and agtermctl behaviour

Use throwaway rows only, local and on `homelab`; record results here.

**Files:**
- Modify: this plan (findings)

- [ ] tmux:
  - `new-session -e` and `set-environment` on re-attach: does a new shell or window see the
    values? Does a running shell keep the old ones?
  - running a command in place of the shell
  - the same for zmx
- [ ] the attachment signal: `foreground` / `splitForeground` in `tree --json` while
      `agr open` is attached, after a tmux detach, and after ssh drops. Is `foreground[0]` always
      `agr`? If not, record what to check instead.
- [ ] `session split visibility on` on an agr row: does ⌘D's `pane.split` hook make the new pane
      remote (`agr on-split`) in every case? Decides its allowlist row.
- [ ] overlays:
  - killing a `--block` call: does the overlay stay up, and does `overlay close --pane-id` close
    it?
  - exit code and stdout for ⌘W and for a submit
  - what the `--js` page bridge exposes beyond submit
- [ ] `agtermctl help` for each allowed leaf: confirm option names and which leaves read stdin;
      finalise the schemas
- [ ] update Technical Details with the results

### Task 2: `agr open --command`, environment export, `agr run`

**Files:**
- Modify: `internal/cli/open.go`, `internal/cli/open_test.go`, `internal/remotescript/agr.sh`,
  `cmd/agr/run.go`, `tests/remote/tmux/*`, `tests/remote/zmx/*`
- Create: `internal/cli/run.go`, `internal/cli/run_test.go`, `internal/proxy/quote.go`,
  `internal/proxy/quote_test.go`

- [ ] `agr open … [--command CMD]`, and `attach --env/--command` behind the remote-version gate
- [ ] the remote `attach` applies env and command per the spike, and validates the values
- [ ] `agr run <host> [--cwd D] -- argv…`
- [ ] the Mac-side shell quoting helper (`internal/proxy/quote.go`)
- [ ] tests (Go): argv for `--command`, env pairs, invalid values, the old-remote gate (no env,
      `--command` refused), `agr run` argv, and quoting with `'`, `"`, `;`, `$()`, a backtick and
      a newline
- [ ] tests (remote): a new tmux session sees the env and runs CMD; re-attach refreshes the env
      and doesn't re-run CMD; the same for zmx
- [ ] `make check` must pass before Task 3

### Task 3: Wire codec and listener dispatch

**Files:**
- Create: `internal/proxy/wire.go`, `internal/proxy/wire_test.go`
- Modify: `internal/receiver/listener.go`, `internal/receiver/dependency.go`,
  `internal/receiver/listener_test.go`, `internal/receiver/mocks/dependency_mock.go` (generated),
  `internal/daemon/daemon.go` (the `NewListener` call only)

- [ ] `proxy.Header` (decoded; `session_id` refused; limits) is separate from `proxy.Request`
      (filled by the listener after resolution: caller binding, files, stdin). Add a frame writer
      that is safe for concurrent stdout and stderr.
- [ ] `CtlSink` interface (`Ctl(ctx, proxy.Request, proxy.FrameWriter) int`) in `dependency.go`.
      `NewListener` takes it; with a nil sink a call gets a refusal.
- [ ] `serveConn`:
  - a `cmd: ctl` line switches to proxy mode
  - resolve the binding (a miss is refused), then `ack 1`
  - read the body under the deadline
  - an EOF watcher cancels the request ctx (derived from the Serve ctx)
  - call the sink, write `x`, close
- [ ] tests:
  - status lines, then a ctl header, on one connection
  - ack before body
  - a body larger than 4096 bytes after the ack
  - limits; a short body before the deadline
  - `session_id` refused; unknown session refused
  - client EOF cancels; `Serve` returns promptly while the sink blocks
  - `MarkAlive`
- [ ] `make test` must pass before Task 4

### Task 4: Parser, allowlist and ownership

**Files:**
- Create: `internal/proxy/argv.go`, `internal/proxy/argv_test.go`,
  `internal/proxy/policy.go`, `internal/proxy/policy_test.go`,
  `internal/proxy/dependency.go`, `internal/proxy/mocks/dependency_mock.go` (generated)

- [ ] parser for agtermctl's grammar (`--opt value`, `--opt=value`, repeats, options after
      positionals, `--`)
- [ ] the allowlist table, with a schema per leaf
- [ ] `Authorize(ctx, host, caller, header, argv)` → normalised call (targets, pane ids,
      files, commands) or a refusal:
  - full ids and self markers only
  - injected `--target` / `--pane-id`
  - per-pane binding to this host
  - the attached check (Task 1 signal)
  - row-level leaves need every pane bound and attached
  - `--window` / `--socket` stripped
- [ ] tests (table-driven):
  - every allowed leaf
  - every refused leaf and option (`scratch`, `restart`, `paste`, `reveal`, `duplicate`,
    `split close`, `swap`, `overlay navigate`, `run-job`, `hud --file`, `background`,
    `--persistent`, `--browse`, `pick`, `ask`, `dashboard`)
  - `--target=X` and `--command=` forms
  - prefix and `active` refused; stale self ids mapped
  - the unbound split pane of an owned row refused; a detached pane refused
  - `session close` with one unowned `--target` refused
  - `events --run` allowed
- [ ] `make test` must pass before Task 5

### Task 5: Rewrites, streaming execution, daemon wiring

**Files:**
- Create: `internal/proxy/rewrite.go`, `internal/proxy/rewrite_test.go`,
  `internal/daemon/proxy.go`, `internal/daemon/proxy_test.go`
- Modify: `internal/agterm/exec.go`, `internal/agterm/ctl.go` (+ tests),
  `internal/daemon/daemon.go`, `internal/daemon/dependency.go`, `cmd/agr/wire.go`

- [ ] `Rewrite`: files, `session new` with name collision check and pre-bind, overlay
      COMMAND with `cwd`, `restore`, URL warning, `<agr>` stable path
- [ ] the agterm streaming call (stdin, stdout, stderr; kill on ctx), exposed through a daemon
      interface
- [ ] the daemon implements `CtlSink`:
  - Authorize → Rewrite → run
  - cancel hook on a detached 2 s ctx
  - per-host cap of 16
  - temp lifetime and the 24 h sweep
  - goroutines tracked so `stopHost` returns within `teardownTimeout`
  - leftovers cleared at start
- [ ] wire it in `cmd/agr/wire.go` with compile-time interface assertions
- [ ] tests:
  - each rewrite's exact argv and Mac shell string
  - pre-bind after `session new`, then a following `restore` authorised
  - temp files: content, 0600; removed for blocking calls and kept for non-blocking ones
  - streaming order of stdout and stderr; exit passthrough; `-1` becomes 1
  - cancel kills the process and runs the overlay-close hook on a live ctx
  - host shutdown while a call blocks; the 17th concurrent call refused
- [ ] `make test` must pass before Task 6

### Task 6: Remote proxy (`agr ctl`, `~/.local/bin/agtermctl`, doctor)

**Files:**
- Modify: `internal/remotescript/agr.sh`, `internal/remote/install.go`,
  `internal/remote/probe.go`, `internal/remote/testdata/probe.txt`, the doctor output code
- Create: `tests/remote/tmux/proxy_test.sh`, `tests/remote/zmx/proxy_test.sh`

- [ ] `agr ctl`:
  - the current-session helpers (status rebuilt on them, existing tests green)
  - `have python3` or exit 1
  - the python3 client: header from argv and env, wait for ack, then the body, demux the
    frames, the messages, quiet Ctrl-C
- [ ] install writes the wrapper (0755); the doctor probe and report
- [ ] `shellcheck -s sh` clean
- [ ] remote tests against a fake python3 server on a short socket path, which the test kills
      itself:
  - header content (argv, `@i`, `self`, `cwd`)
  - the body is sent only after the ack
  - stdin only with `--stdin`
  - stdout and stderr frames; exit codes 0, 1, 2
  - no ack, ack then EOF, and no socket each give their message
  - outside a session → refused; zmx covers the session lookup
- [ ] `make check` must pass before Task 7

### Task 7: Verify acceptance criteria

- [ ] from a throwaway remote row on `homelab`, with unchanged tools:
  - a `decide.py` panel round trip; `show-image.sh`
  - `session text` / `session type` on a sibling row of the same host
  - `session type` refused on a local row, on the local split pane of an owned row, and on a
    detached pane
  - `session new --command 'claude …'` gives a bound homelab row running it
  - `session restore` on that row reattaches after an agterm restart
- [ ] status events unchanged; refusal, old-daemon, old-remote and bridge messages as
      documented; `agr down` returns within `teardownTimeout` during a blocking call
- [ ] `make check` green

### Task 8: [Final] Update documentation

- [ ] README "Remote agterm control":
  - what works unchanged; the allowlist; full ids only; one-file pages
  - the `foreground` limitation
  - how to request a new leaf
  - `agr open --command`, `agr run`
- [ ] README "Requirements": python3 on the host; Mac daemon and remote both at least this
      release
- [ ] README "Failure modes": the proxy messages
- [ ] README "Security":
  - the per-pane scoped policy and the attached check
  - `--js` pages run in agterm on the Mac and can reach its network; the localhost warning is not
    a control
  - `events` streams the Mac's whole feed
  - the existing status `session_id` path is unchanged and still unscoped
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

- **Skill sweep** (machine-setup): run `decide`, `show-image`, `teardown-agent` and
  `autoimplementation` from a remote Claude, unchanged, and record what breaks.
  - `spawn-agent` will break on its `foreground == claude` poll (it reads `agr`). The fix there
    is skill-side: treat `agr` as running on a remote row, or poll `session text`.
  - The README notes this class of break for other users.
- **Spawning workers on a host from the Mac**: `spawn-agent --host <host>` on top of `agr open
  --command` (skill-side; the worktree via `ssh host git …`).
- Manual check on `homelab`: the Task 7 round trips with the real agterm; Ctrl-C during a
  `--block` overlay closes it.
- Restart the Mac `agr daemon` after upgrading. Release notes: `brew upgrade agr`, restart the
  daemon, `agr install <host>`, re-open rows to pick up the environment.

## Open questions

- **Remote worker → local orchestrator**: under the scoped policy a homelab worker cannot
  type into the local row that spawned it. Allow writes to that row, recorded when it runs
  `agr open` or `session new` for the host? Or keep the policy strictly host-scoped?
- **A real agtermctl on the host** (a Mac running agterm): install the proxy anyway (it
  shadows the real one in agr rows), or skip the proxy on such hosts?
- **Status `session_id` path**: it is unscoped today (`event.go:63-67`), so a host can set
  status on any row. Restrict it to rows bound to the host in this plan, or leave it?
