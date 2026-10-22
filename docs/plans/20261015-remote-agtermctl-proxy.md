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
- **Handoff to a local agent, with consent**: a remote session may `session type` into a local
  pane running Claude or Codex that it has no grant for. agterm asks the user first (Allow once
  / Allow this host here / Deny). Mac → remote typing already works today.
- **An `agr` skill for Claude**: agr ships a skill, installed on the Mac and on each host. It
  teaches Claude what "on <host>" means, how to wrap any agterm spawn for a host, and how to
  hand off from a remote row. Other users' skills then work by composition, with no changes.

Known limits, documented rather than hidden:

- **Foreground reads `agr`**: a remote row's `foreground` in `tree --json` is the Mac-side
  `agr`, not the remote program, so tools that poll it see `agr`. `spawn-agent` does this
  (`spawn-agent.sh:160-183`), so it reports "not running"; see Post-Completion.
- **Single-file pages**: HTML pages must be one self-contained file.
- **Restricted commands**: only the commands and options in the allowlist work from a host.
  Everything else fails with a clear message.

Decided by the user (not open):

- **Scope**: all six items above.
- **Generic**: an agtermctl proxy, not agr-specific verbs. This supersedes the earlier
  `agr overlay` plan and reuses its reviewed transport rules.
- **Trust, scoped**: a host may read, may write only to panes bound to it (including ones it
  created), and is refused anything that runs or reads something on the Mac. The one widening
  is `session type` into a local agent pane under a grant (Technical Details > Grants).
- **Orchestrator grant**: a host session started for a local orchestrator may also message that
  orchestrator's pane. See Technical Details > Grants.
- **Consent grant**: any other pane can be messaged after the user allows it in an agterm `ask`
  dialog; "Allow for this pair" is remembered like an orchestrator grant. See Technical
  Details > Grants.
- **Skill**: agr ships a generic `agr` skill (this repo). The user's own `spawn-agent` and
  `teardown-agent` gain `--host` in machine-setup (Post-Completion).
- **Host with a real agtermctl** (a Mac running agterm): `agr install` skips the proxy there.
- **Status from hosts is scoped too**: a `session_id` status event is applied only to a row
  that has a pane bound to the sending host. This closes the unscoped path in
  `event.go:63-67`.

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
- **agterm versions**: agr supports agterm from 0.25.0 (`internal/agterm/client.go:22`,
  `Client.Version`, `VersionLess`). The proxy needs 0.36.0: on an older agterm the daemon
  refuses every `ctl` call with `agtermctl (via agr): needs agterm 0.36.0 (found <v>)`, and
  everything else in agr keeps working.
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
- **Dialogs**: `agterm.Ctl.Confirm` (`internal/agterm/ctl.go:283`) runs `ask open TITLE
  --message … --button id=Label … --default … --target <row>`; exit 2 is a cancel, and the
  answer is `{"result":"answered","id":…}`. `agr end` already confirms through it.
- **Skills**: Claude Code reads `~/.claude/skills/<name>/SKILL.md`. agterm installs its own
  `agterm` skill the same way, with an `<!-- agterm-skill -->` marker.
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
  - grants: the program rule per surface, the sanitiser, consent outcomes, the re-check after
    the answer, per-host anti-spam, grants carried over a re-bind
  - the skill: embed, marker, install rules
- **remote (sh)**: `tests/remote/{tmux,zmx}/proxy_test.sh` run the installed proxy against a
  fake python3 bridge server.
- Every test-only helper takes `*testing.T` first and calls `t.Helper()`.
- **live**: `tests/live/proxy_live.sh` (Task 8) runs the scriptable round trips against `homelab`
  and the local agterm; checks that need a click or an agterm restart are Post-Completion.

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
  hook (`overlay open --block` → `session overlay close --target <row> [--pane <side>]`; the
  spike confirmed a killed `--block` leaves the overlay up and this closes it).
- **Ownership** is per pane, and only while attached. A write is allowed only on a pane that:
  1. has a binding for this host, and
  2. is still running `agr open` (Technical Details > Spike results > Attached). This check is
     what closes the detached-pane hole: after a detach or a dead transport the binding stays
     while the pane is a local Mac shell (spike).

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
| `session overlay result` (`--page`, `--block`), `session overlay text` (`--all`, `--lines`) | read | owned row; `--pane <side>` injected |
| `session type` (`--stdin` reads stdin; `--select` refused) | write, pane | `--pane-id` injected; target pane owned and attached, or a local pane with a grant or consent (Grants) |
| `session status` (`--blink`, `--auto-reset`, `--sound`, `--color`, `--shape`, `--pane-id`), `session context` (`--clear`), `session rename` | write, row | owned row |
| `session hud open/update/close` (message options only, no `--file`) | write, row | owned row |
| `session overlay open --html @i [--js] [--block] [--size-percent]` | write, pane | **never opened as `--html`**: served through the page shim as a URL page (Host pages); `--cwd`, `--follow`, `--persistent`, `--browse`, `--chromeless`, `--navigation` refused; `--pane <side>` injected; cancel hook = overlay close |
| `session overlay open COMMAND…` | write, pane | COMMAND → `agr run` (see Rewrites) |
| `session overlay open --url URL` | write, pane | static only: `--js`, `--persistent` and `--browse` refused (a host's JavaScript runs only under the shim's CSP); localhost hosts warned (a UX hint, not a security control) |
| `session overlay close`, `session overlay submit` (`--value`), `session overlay resize` (`--size-percent`, `--full`) | write, pane | owned; `--pane <side>` injected (resize is session-wide) |
| `session new` (`--name`, `--cwd`, `--command`, `--workspace`, `--no-select`, `--wait`; `--after`, `--before`, `--workspace-name`, `--create-workspace` refused) | create | rewritten to `agr open` on the host; the new row is pre-bound |
| `session close` (repeated `--target`) | write, row | every target owned |
| `session restore CMD` (`--pane-id`; `--none`, `--clear` allowed) | write, pane | rewritten to `agr open … --command CMD` |
| `notify` | write | message only; targets the caller's row |
| `session split visibility on/off` (`--axis`) | write, row | only on an owned, attached row. The spike confirmed showing the split runs the `pane.split` hook and the new pane becomes remote (`agr open <host> <name>-2 --cwd … --parent <name>`). If the hook fails it stays a local shell, which is never owned, so nothing can be typed into it |

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
- **`<agr>`** is the stable absolute path from a new resolver, `agrpath.Stable()`: when
  `os.Executable()` is inside a Homebrew Caskroom or Cellar, it is the `<brew prefix>/bin/agr` symlink if
  that resolves to the same binary, else `os.Executable()`. Today `cmd/agr/wire.go` passes
  `os.Executable()` to setup and the pane hooks; they switch to the resolver too, so a
  `brew upgrade` no longer leaves hooks pointing at a removed Cellar path.
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
    <primary pane id from tree>, Pane: left, Host: host, Name: gen}`. `agr open` re-binds the
    same pane when it starts.
  - The attached check still applies: until `agr open` is the new pane's foreground, a `type`
    or `restore` into it is refused with `the new row is not attached yet; retry`, and the
    caller retries.
- **`session overlay open COMMAND…`** becomes `session overlay open "<agr> run <host> --cwd
  <cwd> -- COMMAND…"`. `<cwd>` is the header's `cwd` (the host's `$PWD`), not agterm's Mac
  default.
- **`session restore CMD`** on an owned pane becomes `session restore "<agr> open <host> <name>
  --command CMD" --pane-id <p>`. After a restart, agterm reattaches, and `CMD` runs only if the
  remote session is gone.
- **`--url`**: `localhost`, `127.*`, `[::1]` and `0.0.0.0` get a warning on stderr (localhost
  means the Mac). It is not a security control.

### Grants

A grant lets a caller message one **local** pane it does not own. Grants live on the caller's
binding: `bindings.Binding.Grants []Grant` (JSON `grants`, omitted when empty; old files still
load), where `Grant{Row, PaneID, Kind}` and `Kind` is `orchestrator` or `consent`.

- **Scope is the host**: the header's `session` is chosen by the host, so any process on that
  host can claim any of its session names. A grant is therefore treated, worded and documented
  as "anything on `<host>` may type into this pane", never as one session's.
- **What a grant allows**: only `session type`, only into that exact pane. `notify` stays as in
  the allowlist (caller's row only). `session text` stays limited to host-bound panes; grants do
  not widen reads.
- **Local targets only**: a pane bound to any host is never a grant or consent target. Its
  `foreground` always reads `agr open …`, so the program rule below can't see a remote shell.
  Mac → remote typing already works; host → other host is out of scope.
- **The program rule** (checked on every call, granted or consented):
  - each surface is mapped to its own fields: left → `foreground` / `foregroundShell`, right →
    `splitForeground` / `splitForegroundShell` (only when `hasSplit`); a scratch or any surface
    without these fields is **unknown**
  - allowed only when the basename of `foreground[0]` is in the agent list (default `claude`,
    `codex`; the spike saw `["claude", …]` and `["codex"]`). A shell, an unknown foreground, or any
    other program (`python3`, `vim`, `less` run commands) is refused
  - read from one `tree --json` immediately before `session type`; the small window between that
    read and the typing is documented
- **Typed text is sanitised** (granted and consented typing, not owned panes):
  - refused if it contains C0 controls other than `\n` and `\t`, DEL, or Unicode Cf characters
    (bidi, zero-width). That blocks `\e[Z` (Claude's mode cycle), `\e[201~` (paste end) and
    hidden text
  - at most 64 KiB
- **`session type` schema for grants**: `--select` and `--pane` refused.
- **Lifetime**: a grant ends with the caller's binding (row close, `agr end`, stale-status
  cleanup). A re-attach of the same host and name (`agr open` again, `session restore` after an
  agterm restart, the daemon's pre-bind) **carries the grants over**: `bindings.upsert` copies
  `Grants` when it replaces an entry with the same host and name. A grant whose target pane is
  gone is refused by `Authorize`, and the daemon drops it from the store.

**Orchestrator grant** (set up front):
- `agr open … --orchestrator <row-id>:<pane-id>` adds one `orchestrator` grant to the new
  binding.
- Both values must be full ids of a live local pane (`tree --json`, not bound to any host).
- The orchestrator's shell must expand `$AGTERM_SESSION_ID:$AGTERM_PANE_ID` itself (double
  quotes in the spawning command); expanded later, the variables would name the new row.
- A row created from a host with `session new` gets no grant, since its caller is already
  host-owned, and a host-sent `--command` can't inject `--orchestrator` through the rewrites
  (every value is quoted; tested).

**Consent grant** (asked on first use):
- **Trigger**: `session type` into a local pane with no grant. Every other write leaf on such a
  pane is refused as before, with no dialog.
- **Target pane**: `--target` must be a full row id. With `--pane-id`, that pane. Without it,
  the row's one pane that passes the program rule; with zero or two, the call is refused and
  the message lists the row's pane ids.
- **Checks before the dialog**: the program rule and the sanitiser. A call that can't proceed
  never asks the user.
- **Dialog**: `ask open --no-block --pane-id <target pane> --target <row>` (no `--follow`, no
  `--hotkey`), so
  it sits on the pane the text would go to:
  - title: `Anything on <host> wants to type into “<target row name>”`
  - message (one line; Spike results > ask): `From session <name> on <host>:` then the first
    300 characters of the text, newlines as `⏎`, between
    `⟦ ⟧` delimiters (Cf characters shown escaped), plus `… (<n> characters)` when cut; the
    text can't pass for dialog wording
  - buttons: `once=Allow once`, `pair=Allow <host> here until it closes`, `deny=Deny`
    (default `deny`)
- **Waiting**: the daemon polls `ask result <id>`; on a client EOF, a host shutdown or 120 s it
  runs `ask cancel <id>`, so a dead dialog never stays up to be clicked.
- **Outcome**:
  - `once`: the program rule is checked **again** (fresh `tree --json`), then the call runs;
    nothing is stored
  - `pair`: the same re-check, the grant stored on the caller's binding (found by host and name
    under the store lock), then the call runs
  - a re-check failure after the answer: refused with `the pane is no longer running an agent`
  - `deny`, a cancel or no answer in 120 s: refused with `the user declined` / `no answer`
- **Dialog busy**: agterm allows one pending dialog per row. If the target row already has one
  (`ask already pending`), the call is refused with `the user has a dialog open on that row`.
- **Anti-spam, per host**:
  - one open consent dialog per host; a second consent call while it is open is refused with
    `a request to the user is already open`
  - after a deny, a timeout or a client cancel, consent calls from that host to the same target
    row are refused without a dialog for 60 s
- **Concurrency**: a call waiting on the dialog counts toward the 16-call cap.

### Spike results (2026-10-22, agterm 0.36.0, tmux 3.5a on homelab)

- **Environment**
  - tmux: `new-session -e K=V` reaches the session's shell. `set-environment -t <s> K V` on
    re-attach reaches new windows and panes only; a running shell keeps the old value. Nothing
    leaks into the global environment.
  - A command in place of the shell works: `new-session -d -s <n> -e … '<CMD>; exec "$SHELL" -l'`
    runs CMD, then leaves a login shell.
  - zmx: homelab has no zmx, and the macOS zmx agterm bundles is an agterm fork that sets
    `AGTERM_*` itself, so it can't stand in. Rule: on create, `env K=V … zmx attach <n> <CMD>`;
    on re-attach no refresh. **Unverified on a Linux zmx host** (Post-Completion).
- **Attached**: a pane is attached when its own fields (left: `foreground`; right:
  `splitForeground`) are present, `basename(argv[0]) == "agr"`, `argv[1] == "open"`,
  `argv[2]` is the binding's host and, when `argv[3]` is present and not an option, it is the
  binding's name. Seen: `["/opt/homebrew/bin/agr","open","homelab","agrspike-sh"]`, and for a
  split pane `[…,"open","homelab","agrspike-sh-2","--cwd","/home/kosta","--parent","agrspike-sh"]`.
  - after a tmux detach or a killed transport: `foreground` absent, `foregroundShell: "zsh"`,
    and the binding **stays**
  - a `--command` row whose `agr open` exits closes, and its binding is removed
  - the transport is `mosh-client` here. A network drop keeps `agr` in the foreground while
    mosh reconnects, and that counts as attached
  - a row without a split has only a `left` surface
- **Overlays**
  - `session overlay open/close/submit/result/text` take `--pane left|right` (or
    primary/split), **not** `--pane-id`. The daemon maps the bound pane id to its side from
    `surfaces[]` at call time, and leaves out `--pane` on a row without a split.
  - a killed `--block` leaves the overlay up; `overlay close --pane <side>` closes it
  - submit: exit 0, `{"outcome":"submitted","pageID":…,"value":…}`; dismissed: exit 2,
    `{"outcome":"dismissed","pageID":…}`
  - page bridge: every `--html` page gets agterm's bridge (`data-agterm` buttons; with `--js`,
    `agterm.request`), which can run any socket command; `--url` pages get none. Hence Host
    pages below
- **ask**
  - `open --no-block` prints `{"id":…}`. `ask result <id>`: `{"result":"pending"}` exit 1,
    `{"result":"cancelled"}` exit 2, answered exit 0 `{"result":"answered","id":…}`
  - **one pending dialog per row** (`error: ask already pending`); other rows are independent. A
    consent dialog open on a row therefore makes `agr end`'s confirm on that row fail until it
    closes (120 s at most)
  - title and message **must not contain control characters**, newlines included: the preview
    is one line, with a newline shown as `⏎` and a tab as `⇥`
  - no `--hotkey` is set and the default is `deny`, so Return denies. Whether any other key can
    pick a button was not testable without a person (Post-Completion)
- **Agents**: Claude Code shows `["claude", …]`, Codex shows `["codex"]`.
- **Stable path**: brew installs agr as a **cask** (`/opt/homebrew/Caskroom/agr/<v>/agr`,
  symlinked from `/opt/homebrew/bin/agr`), not under Cellar. `agrpath.Stable` handles Caskroom
  and Cellar. The running daemon is `~/.local/bin/agr` (an older manual install).
- **Option names** per leaf were read from `agtermctl <leaf> --help` and are in the allowlist
  table.

### Host pages (URL shim)

Decided: a page from a host is never opened as an agterm `--html` page, because that page's
bridge would get around the whole allowlist. Reviewed with Codex.

- **Serving**: the daemon runs one loopback HTTP server (`127.0.0.1`, a random port) for pages.
  Each page gets a random 128-bit token and is served at `/<token>/` only while it is open.
- **Opening**: `session overlay open --url http://127.0.0.1:<port>/<token>/ --js --target <row>
  [--pane <side>]`. URL pages get no agterm bridge, so `data-agterm` buttons are inert. The
  page id from the reply is bound to the token.
- **Shim**: the served HTML gets one injected script, first in `<head>`, defining
  `window.agterm.request(cmd, opts)`. Only `session.overlay.submit` (`args.value`, a string) and
  `session.overlay.close` are accepted; they POST to `/<token>/submit` or `/<token>/close`. Any
  other command rejects its promise with `not available to pages from a host`. `window.close()`
  counts as a close.
- **CSP** (a response header, which a page can't remove; a `<meta>` tag can only add to it):
  `default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline';
  img-src 'self' data:; font-src 'self' data:; connect-src 'self'; form-action 'self';
  frame-src 'none'; base-uri 'none'`. Page JavaScript can therefore reach only its own server, so
  no requests, form posts or images to other localhost services or the network. agterm already
  pins a URL page to its origin and asks before following a link elsewhere.
- **Binding and state**: each token holds `{page id, row, pane side, state}`. Exactly one
  transition from `open` to `submitted` or `dismissed` is allowed, under a lock. A POST for a
  token that is not `open`, or whose page id is no longer the one in that slot
  (`htmlOverlays` in `tree --json`), gets 409 and changes nothing. The outcome returned to the
  host is built from that one transition. The page id is the one agterm reported, and the value
  is the submitted string.
- **`--block`**: the daemon waits for the transition, or for the page to leave `htmlOverlays`
  (dismissed: ⌘W, panel button, reload into another page), polled every 250 ms. It returns
  agtermctl's shapes and exit codes: 0 `{"outcome":"submitted","pageID","value"}`, 2
  `{"outcome":"dismissed","pageID"}`. After a submit, the daemon closes the overlay itself.
  A non-blocking open returns agterm's reply and keeps the token until the page closes.
- **Host `session overlay submit/close`** on a slot holding a shim page drive the same
  transition, so they never act on a different page.
- **Limits**: a POST body is at most 64 KiB (refused, not buffered); read and write deadlines of
  5 s; at most 16 concurrent handlers; at most 8 open pages per host; a GET serves only the one
  page (no directory, no other files). Tokens are dropped and the page's temp directory removed
  when its page closes; daemon start clears leftovers.
- **Compatibility**: `decide.py` works unchanged (it only calls submit and close). A URL page
  shows the browser canvas, but the theme CSS variables still apply.

### Status events (`session_id` path)

- `receiver.Listener.route` applies a `session_id` status only when `ForRow(session_id)` has a
  pane bound to this host; otherwise it logs and drops the event.
- `session`-name events are unchanged; they already resolve through this host's bindings.
- This breaks cookbook-format senders whose rows were never opened with `agr open`. The
  release notes say so.

### `agr open` / `agr run` / attach (Mac and remote)

- **`agr open <host> <name> [--cwd D] [--command CMD]`**: `CMD` runs only when the remote
  session is new. Attaching to an existing session prints a notice and keeps it.
- **Environment**: `attach --env K=V …` carries `AGTERM_ENABLED=1`, `AGTERM_SESSION_ID`,
  `AGTERM_PANE`, `AGTERM_PANE_ID`, `AGTERM_WORKSPACE_ID`, `AGTERM_WINDOW_ID` (from `agr open`'s
  own environment at open time) and `AGTERM_SOCKET=agr-proxy`. Values are checked as UUIDs or
  tokens.
  - tmux: `new-session -e` on create, `set-environment -t` on re-attach (Spike results)
  - a re-attach refreshes the environment for **future** processes only. A running process keeps
    its old ids, and the self-id mapping routes them to the current binding. A process started
    before this release has no `AGTERM_*` at all and needs a restart (README)
  - zmx: per Spike results
- **Remote-version gating**: `--env` and `--command` are sent only when `HostInfo.AgrVersion`
  is at least **1.3.0**, or is `dev` (a local build, so the run's tests and the live checks
  pass).
  - Older host: no env, and `--command` is refused with `run agr install <host>`.
  - Tests cover both cases.
- **`agr run <host> [--cwd D] -- argv…`** (new Mac verb, `internal/cli/run.go`): a one-shot
  `ssh -t` running argv in D with the open/attach quoting. Its exit code is passed through.

### Remote proxy (`agr ctl`, python3)

- **Install**: `~/.local/bin/agtermctl` is a sh wrapper, `exec "$HOME/.local/bin/agr" ctl
  "$@"`, written by `remoteScriptWrite`.
  - If a login shell on the host already finds an `agtermctl` that is not agr's proxy (a Mac
    running agterm), install skips the proxy and says so.
  - A proxy agr wrote earlier, recognised by a marker line, is updated.
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

### The `agr` skill

- **Source**: `internal/skill/SKILL.md`, embedded with `go:embed`. Its first body line is
  `<!-- agr-skill <version> -->`.
- **Install**:
  - Mac: `agr setup` writes `~/.claude/skills/agr/SKILL.md`.
  - Host: `agr install <host>` writes the same file to the host's `~/.claude/skills/agr/`
    (`~/.claude` exists by then: `mergeHooks` creates it). One more `sshClient().Run` with a
    new `skillWriteScript` (sh): write when the file is missing or carries the marker, else
    print `kept`. Best-effort, like `ensureTerminfo`: a failure becomes a report line, not an
    install error.
  - Both overwrite only a file that is missing or carries the `agr-skill` marker. A
    user-edited file without the marker is left alone, with a notice.
  - `agr skill` prints it to stdout, for other agents (Codex `AGENTS.md`, etc.).
- **Content** (bullets and commands, no host names baked in):
  - **Triggers**: "on <host>", "from <host>", "remote session/worker", "hand off to <row>",
    and a host the user names that `agr ls <host>` accepts.
  - **Spawn on a host from the Mac**: keep the user's own spawn skill and wrap its program.
    - `session new --command X` becomes `session new --command "agr open <host> <name> --cwd
      <dir> --command 'X' --orchestrator $AGTERM_SESSION_ID:$AGTERM_PANE_ID"`, run from the
      orchestrator's own shell so the variables expand to the orchestrator.
    - Quoting rule: when X contains `'`, write X to a script on the host (over ssh) and pass
      `--command 'sh <script>'` instead.
    - Filesystem steps (worktree, brief file) run over `ssh <host>`.
    - Confirm it started with `session text`, not `foreground` (that reads `agr`).
  - **Teardown**: `agr end <row>` (the user confirms), or `agr kill <host> <name>` plus
    `agtermctl session close`; remote worktree cleanup over `ssh <host>`.
  - **On a remote row** (`AGTERM_SOCKET=agr-proxy`): `agtermctl` is the proxy.
    - Full ids only, from `tree --json`.
    - The allowlist; refused leaves say why.
    - **Handoff**: find the target row by name in `tree --json`, then `session type --target
      <id> [--pane-id <p>] --stdin`. Only a local pane running `claude`/`codex`; plain text, no
      control characters, at most 64 KiB. The user gets a dialog; on refusal, say so and fall
      back to the clipboard.
  - **Messages between sessions on the same host** (tmux): use tmux directly, not the proxy.
    `tmux send-keys -t <name> -l "text"`, then `tmux send-keys -t <name> Enter`; read with
    `tmux capture-pane -p -t <name>`. It works for detached sessions and never touches the
    Mac. Under zmx, use the proxy's `session type` (both rows must be attached).
  - **Messages from the Mac to a remote row**: plain `agtermctl session type` works today.

## What Goes Where

- **Implementation Steps**: code, tests, README in this repo.
- **Post-Completion**: manual round trips on `homelab`, a skill sweep, the release.

## Implementation Steps

### Task 1: `agr open --command`, environment export, `agr run`

**Files:**
- Modify: `internal/cli/open.go`, `internal/cli/open_test.go`, `internal/remotescript/agr.sh`,
  `cmd/agr/run.go`, `cmd/agr/wire.go`, `internal/bindings/store.go`,
  `internal/bindings/store_test.go`, `internal/cli/dependency.go`,
  `internal/cli/mocks/dependency_mock.go` (generated), `internal/agterm/ctl.go` (a tree read
  with panes, bindings and foregrounds), `internal/agterm/ctl_test.go`, `cmd/agr/wire_test.go`,
  `tests/remote/tmux/*`, `tests/remote/zmx/*`
- Create: `internal/agrpath/agrpath.go`, `internal/agrpath/agrpath_test.go`,
  `internal/cli/run.go`, `internal/cli/run_test.go`, `internal/proxy/quote.go`,
  `internal/proxy/quote_test.go`

- [ ] `agr open … [--command CMD]`, and `attach --env/--command` behind the remote-version gate
- [ ] the remote `attach` applies env and command per Spike results > Environment, and validates
      the values
- [ ] `bindings.Binding.Grants` (`Grant{Row, PaneID, Kind}`, JSON `grants`, omitted when
      empty; old files still load); `upsert` carries `Grants` over when it replaces an entry with
      the same host and name; `AddGrant(host, name, grant)` and `DropGrant` find the entry under
      the store lock
- [ ] `agr open … --orchestrator <row>:<pane>`: validated against `tree --json` (live, not
      host-bound) and stored as an `orchestrator` grant
- [ ] `agr run <host> [--cwd D] -- argv…`
- [ ] `internal/agrpath`: `Stable()` per Technical Details > Rewrites; `cmd/agr/wire.go` passes
      it to setup and the pane hooks instead of `os.Executable()`
- [ ] the version gate: at least 1.3.0, or `dev`
- [ ] the Mac-side shell quoting helper (`internal/proxy/quote.go`)
- [ ] tests (Go):
  - argv for `--command`, env pairs, invalid values
  - the old-remote gate: no env, `--command` refused
  - `--orchestrator`: valid, malformed, host-bound pane refused, unknown pane refused
  - `Grants` round-trip, and a bindings file without `grants` still loads
  - a re-bind of the same host and name keeps the grants; a different name doesn't
  - `AddGrant` on a binding that disappeared is an error, not a new entry
  - `agr run` argv
  - `agrpath.Stable`: a Cellar path with a matching brew symlink, a Cellar path with a symlink to
    another binary, a non-brew path
  - `cmd/agr/wire_test.go`: `application.setup` and `application.paneHooks` get the stable path
  - `internal/agterm/ctl_test.go`: the tree decoder reads panes, `foreground`,
    `foregroundShell`, `splitForeground`, `splitForegroundShell` and `hasSplit`
  - the gate: 1.2.0 refused, 1.3.0 and `dev` pass
  - quoting with `'`, `"`, `;`, `$()`, a backtick and a newline
- [ ] tests (remote): a new tmux session sees the env and runs CMD; re-attach refreshes the env
      and doesn't re-run CMD; the same for zmx

### Task 2: Wire codec and listener dispatch

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

### Task 3: Parser, allowlist and ownership

**Files:**
- Create: `internal/proxy/argv.go`, `internal/proxy/argv_test.go`,
  `internal/proxy/policy.go`, `internal/proxy/policy_test.go`,
  `internal/proxy/dependency.go`, `internal/proxy/mocks/dependency_mock.go` (generated)

- [ ] parser for agtermctl's grammar (`--opt value`, `--opt=value`, repeats, options after
      positionals, `--`)
- [ ] the allowlist table, with a schema per leaf
- [ ] `Authorize(ctx, host, caller, header, argv)` → normalised call (targets, pane ids,
      files, commands), **needs consent** (Technical Details > Grants), or a refusal:
  - full ids and self markers only
  - injected `--target` / `--pane-id`
  - per-pane binding to this host
  - the attached check (Spike results > Attached)
  - row-level leaves need every pane bound and attached
  - `--window` / `--socket` stripped
- [ ] tests (table-driven):
  - every allowed leaf
  - every refused leaf and option (`scratch`, `restart`, `paste`, `reveal`, `duplicate`,
    `split close`, `swap`, `overlay navigate`, `run-job`, `hud --file`, `background`,
    `--persistent`, `--browse`, `pick`, `ask`, `dashboard`)
  - `--target=X` and `--command=` forms
  - grants (both kinds): `session type` into the granted pane allowed while its foreground is
    `claude` or `codex`; refused at a shell, for `python3`/`vim`, for an unknown foreground,
    for a scratch surface, for another pane of that row, and for any other leaf (`notify`
    included)
  - the program rule per surface: left uses `foreground`, right uses `splitForeground` only
    with `hasSplit`
  - the sanitiser: `\e`, `\e[Z`, `\e[201~`, `\r`, DEL, a bidi and a zero-width character
    refused; `\n` and `\t` allowed; 64 KiB limit; `--select` and `--pane` refused
  - consent: `session type` into an ungranted local agent pane returns **needs consent** (the
    resolved row and pane, the preview), never a plain allow; a pane bound to any host is
    refused with no consent; a shell or sanitiser failure is refused with no consent; any
    other write leaf is refused
  - a grant whose target pane is gone is refused and reported for dropping (the daemon drops
    it; `Authorize` stays pure)
  - consent pane resolution: `--pane-id` given; omitted with exactly one program pane; omitted
    with zero or two (refused, the message lists the pane ids)
  - prefix and `active` refused; stale self ids mapped
  - the unbound split pane of an owned row refused; a detached pane refused
  - `session close` with one unowned `--target` refused
  - `events --run` allowed

### Task 4: Rewrites, streaming execution, daemon wiring

**Files:**
- Create: `internal/proxy/rewrite.go`, `internal/proxy/rewrite_test.go`,
  `internal/daemon/proxy.go`, `internal/daemon/proxy_test.go`, `internal/daemon/pageshim.go`,
  `internal/daemon/pageshim_test.go`
- Modify: `internal/agterm/exec.go`, `internal/agterm/ctl.go` (+ tests),
  `internal/daemon/daemon.go`, `internal/daemon/dependency.go`, `cmd/agr/wire.go`

- [ ] `Rewrite`: files, `session new` with name collision check and pre-bind, overlay
      COMMAND with `cwd`, `restore`, URL warning, `<agr>` stable path
- [ ] the page shim (`internal/daemon/pageshim.go`): loopback server, tokens bound to page ids,
      injected script, CSP header, one-transition state, `--block` semantics, limits, cleanup
      (Technical Details > Host pages)
- [ ] the agterm streaming call (stdin, stdout, stderr; kill on ctx), exposed through a daemon
      interface
- [ ] the daemon checks the agterm version once per host start (`Client.Version`); below
      0.36.0 the sink refuses every call with the version message
- [ ] the daemon implements `CtlSink`:
  - Authorize → Rewrite → run
  - cancel hook on a detached 2 s ctx
  - per-host cap of 16
  - temp lifetime and the 24 h sweep
  - goroutines tracked so `stopHost` returns within `teardownTimeout`
  - leftovers cleared at start
- [ ] the consent flow (Technical Details > Grants):
  - `agterm.Ctl`: `AskOpen` (`--no-block`, returns the id), `AskResult`, `AskCancel`.
    `Confirm` stays as it is (its argv and exit-2 behaviour pinned by a test)
  - title and message: `Anything on <host> …`, the session name, the target row name, the
    preview in `⟦ ⟧` with Cf escaped
  - poll `ask result`; `ask cancel` on client EOF, host shutdown or 120 s
  - after `once`/`pair`, re-check the program rule on a fresh `tree --json` before typing;
    `pair` stores the grant via `AddGrant`
  - one open dialog per host; 60 s refusal per host and target row after a deny, a timeout or
    a client cancel
  - drop grants `Authorize` reports as gone
- [ ] wire it in `cmd/agr/wire.go` with compile-time interface assertions
- [ ] tests:
  - each rewrite's exact argv and Mac shell string
  - pre-bind after `session new`: a `restore` before `agr open` is the foreground is refused
    with the retry message, and authorised once it is
  - agterm older than 0.36.0: every `ctl` call refused with the version message; status events
    unaffected
  - temp files: content, 0600; removed for blocking calls and kept for non-blocking ones
  - streaming order of stdout and stderr; exit passthrough; `-1` becomes 1
  - cancel kills the process and runs the overlay-close hook on a live ctx
  - host shutdown while a call blocks; the 17th concurrent call refused
  - page shim: an `--html` request opens `--url http://127.0.0.1:…/<token>/ --js`, never
    `--html`; the served page has the CSP header and the shim first in `<head>`; submit → exit 0
    and the outcome JSON; close, `window.close()` and the page leaving `htmlOverlays` → exit 2;
    a second submit, a submit after close, a replayed token, a POST for a replaced page id → 409
    and no change; a 65 KiB body refused unread; the 17th concurrent handler and the 9th page
    refused; tokens and temp files gone after close
  - `--url --js` from a host refused; `--url` without `--js` allowed
  - consent: each button; cancel; timeout (`ask cancel` called); a second call from the same
    host while a dialog is open, from another session name too; the 60 s refusal after a deny,
    a timeout and a client cancel; EOF during the dialog cancels it and stores nothing; a
    stored `pair` grant lets the next call through with no dialog
  - the re-check: program pane, then the dialog, then a shell pane → refused after `once`
  - the dialog title and message: cut at 300 characters, delimiters, Cf escaped

### Task 5: Remote proxy (`agr ctl`, `~/.local/bin/agtermctl`, doctor)

**Files:**
- Modify: `internal/remotescript/agr.sh`, `internal/remote/install.go`,
  `internal/remote/probe.go`, `internal/remote/testdata/probe.txt`, `internal/cli/doctor.go`,
  `internal/remote/install_test.go`, `internal/remote/probe_test.go`,
  `internal/cli/doctor_test.go`
- Create: `tests/remote/tmux/proxy_test.sh`, `tests/remote/zmx/proxy_test.sh`

- [ ] `agr ctl`:
  - the current-session helpers (status rebuilt on them, existing tests green)
  - `have python3` or exit 1
  - the python3 client: header from argv and env, wait for ack, then the body, demux the
    frames, the messages, quiet Ctrl-C
- [ ] install writes the wrapper (0755), skips it when a non-agr `agtermctl` is found, and
      updates an earlier agr proxy (marker line); the doctor probe and report
- [ ] `shellcheck -s sh` clean
- [ ] Go tests: install skips the wrapper when a non-agr `agtermctl` is found, writes it when
      none is, updates a marked one; the probe parses the proxy and PATH facts
      (`testdata/probe.txt`); doctor prints both
- [ ] remote tests against a fake python3 server on a short socket path, which the test kills
      itself:
  - header content (argv, `@i`, `self`, `cwd`)
  - the body is sent only after the ack
  - stdin only with `--stdin`
  - stdout and stderr frames; exit codes 0, 1, 2
  - no ack, ack then EOF, and no socket each give their message
  - outside a session → refused; zmx covers the session lookup

### Task 6: The `agr` skill

**Files:**
- Create: `internal/skill/SKILL.md`, `internal/skill/skill.go`, `internal/skill/skill_test.go`,
  `internal/cli/skill.go`, `internal/cli/skill_test.go`
- Modify: `internal/cli/setup.go`, `internal/cli/setup_test.go`, `internal/remote/install.go`,
  `internal/remote/install_test.go`, `internal/remote/hooks.go` (the new `skillWriteScript`
  beside `hooksWriteScript`), `cmd/agr/run.go`, `cmd/agr/wire.go`
- Create: `tests/remote/tmux/skill_install_test.sh`

- [ ] write `SKILL.md` per Technical Details > The `agr` skill: frontmatter `name: agr` and a
      `description` that lists the triggers; marker line; spawn, teardown, remote-row and
      handoff sections; no host names
- [ ] `internal/skill`: the embedded bytes, the version stamped into the marker, and
      `Install(dir)`, which writes only when the file is missing or carries the marker
- [ ] `agr setup` installs it on the Mac; `agr install <host>` installs it on the host with
      `skillWriteScript` after `mergeHooks`, best-effort (a report line, never an install error)
- [ ] `agr skill` prints it
- [ ] tests:
  - the marker and version present; the frontmatter parses; no `homelab` or other host literal
  - install: missing file written; marked file replaced; unmarked file left alone with a notice
  - setup and remote install call it; a remote write failure is a report line and install
    still succeeds
  - `skill_install_test.sh`: missing → written; marked → replaced; unmarked → `kept`
  - `agr skill` output equals the embedded bytes

### Task 7: Scope `session_id` status events to bound rows

**Files:**
- Modify: `internal/receiver/listener.go`, `internal/receiver/dependency.go` (`ForRow` added to
  the resolver interface), `internal/receiver/listener_test.go`,
  `internal/receiver/mocks/dependency_mock.go` (generated)

- [ ] `route`: a `session_id` event is applied only when `ForRow(id)` has a binding for
      `l.host`; otherwise log `status for unbound row <id> from <host> dropped`
- [ ] tests: a bound row is applied; an unbound row is dropped; a row bound to another host is
      dropped; `session` events unchanged

### Task 8: Live round-trip script

**Files:**
- Create: `tests/live/proxy_live.sh`, `tests/live/restore_test.sh`, `tests/live/README.md`

A script, not part of `make check` (it needs `homelab` and the running agterm), that runs every
check below with throwaway rows it creates (names start with `agrlive-`) and exits non-zero on
the first failure, naming it. It exits 2 with `homelab or agterm unreachable` when either can't
be reached, before changing anything.

**Swap and restore** (decided: only one daemon can serve a host, because the bridge socket on
the host is fixed at `~/.cache/agr/bridge.sock`):
1. record the running daemon's executable (`agr.pid` → its argv[0], e.g. `~/.local/bin/agr`)
   and stop it
2. start `bin/agr daemon` in the background, and run `bin/agr install homelab`
3. run the checks
4. a trap on EXIT, INT and TERM, in this order: closes every `agrlive-` row, kills every
   `agrlive-` session on homelab, stops the test daemon, runs `<recorded agr> install homelab`
   (which restores the previous remote script), deletes `~/.claude/skills/agr/` on homelab
   when the test created it, and starts `<recorded agr> daemon` again
5. a SIGKILL skips the trap: `tests/live/README.md` says how to restore by hand (`agr install
   homelab`, then start the daemon)

- [ ] from a throwaway remote row on `homelab`, with unchanged tools:
  - an overlay `--html --js --block` round trip through the shim, answered by the page's own
    `agterm.request('session.overlay.submit', …)` (a test page that submits on load); the
    outcome JSON reaches the host
  - a test page with a `data-agterm="session.type"` button clicked by script, a call to
    `agterm.request('session.type', …)`, and a `fetch` to another localhost port: none takes
    effect (a marker pane receives nothing, the fetch is blocked by the CSP)
  - `session text` / `session type` on a sibling row of the same host
  - `session type` refused on a local row with no grant, on the local split pane of an owned
    row, and on a detached pane
  - `session new --command '<marker program>'` gives a bound homelab row running it; a `type`
    into it before it is attached gets the retry message
  - a worker opened with `--orchestrator` types into a local throwaway pane running a stand-in
    program named `claude`, and is refused once that pane is at a shell prompt
- [ ] the consent path, without a click: a `session type` into a local `claude` pane opens a
      dialog (`askPending` in `tree --json`); the host's Ctrl-C closes it and stores nothing; a
      second request from another session on the same host while it is open is refused; a
      host-bound target row is refused with no dialog
- [ ] the bridge: status events unchanged; refusal, old-daemon and bridge-down messages as
      documented; `agr down homelab` returns within `teardownTimeout` during a blocking call
- [ ] the trap: a test of its own (`tests/live/restore_test.sh`, run with stand-in `agr`,
      `agtermctl` and `ssh` on PATH) shows each restore step runs, in order, on EXIT and on INT
- [ ] `tests/live/README.md`: what it needs, how to run it, and how to restore by hand

### Task 9: [Final] Update documentation

**Files:**
- Modify: `README.md`

- [ ] README "Remote agterm control":
  - what works unchanged; the allowlist; full ids only; one-file pages
  - the `foreground` limitation
  - how to request a new leaf
  - `agr open --command`, `agr run`
  - handoff with consent, from the user's side (the dialog and its three buttons)
  - the `agr` skill: where it is installed, `agr skill`, and how a user's own skills compose
    with it (wrap the spawned program in `agr open <host> <name> --command`)
- [ ] README "Requirements": python3 on the host; Mac daemon and remote both at least 1.3.0;
      agterm 0.36.0 for the proxy (older agterm keeps everything else)
- [ ] README "Upgrading": re-open rows to get the environment; agents started before the upgrade
      need a restart to see agterm
- [ ] README "Failure modes": the proxy messages
- [ ] README "Security":
  - the per-pane scoped policy and the attached check
  - pages from a host are served through the shim: no agterm bridge, only submit and close, and
    a CSP that keeps page JavaScript on its own server; `--url` pages from a host are static
    (no `--js`); the localhost warning is not a control
  - `events` streams the Mac's whole feed
  - grants: orchestrator and consent; a grant covers **anything on that host**; local agent
    panes only (`claude`, `codex`); `session type` only; the sanitiser; the 60 s refusal; a
    pair grant lasts as long as the caller's binding, re-attaches included
  - status `session_id` events are scoped to bound rows; cookbook senders on rows not opened with
    `agr open` are dropped
  - the proxy is skipped on hosts that have a real agtermctl

## Acceptance Criteria

- Run: `tests/live/proxy_live.sh`
- Run: `bin/agr skill | grep -q 'agr-skill'`
- Run: `grep -q 'Remote agterm control' README.md && grep -q 'agterm 0.36.0' README.md`

## Post-Completion

- **Skill sweep** (machine-setup): run `decide`, `show-image`, `teardown-agent` and
  `autoimplementation` from a remote Claude, unchanged, and record what breaks.
  - `spawn-agent` will break on its `foreground == claude` poll (it reads `agr`). The fix there
    is skill-side: treat `agr` as running on a remote row, or poll `session text`.
  - The README notes this class of break for other users.
- **`--host` in the user's skills** (machine-setup, after this ships):
  - `spawn-agent.sh --host <host> [--repo <remote path>]`:
    - the worktree via `ssh <host> 'cd <repo> && git worktree add …'`
    - the brief copied with `scp`
    - the row from `session new --command "agr open <host> <name> --cwd <wt> --command 'claude
      …' --orchestrator $AGTERM_SESSION_ID:$AGTERM_PANE_ID"`
    - the start check through `session text`
    - the remote repo path comes from `--repo`, else a per-host default in the skill's config
  - `teardown-agent --host <host>`: `agr end <row>` (the user confirms), then the remote
    worktree and branch removed over ssh
  - trigger phrases in both skills' descriptions: "on <host>", "from <host>"
- **Checks that need a click or a restart** (the live script can't do them):
  - consent: **Allow once** types it and the next call asks again; **Allow homelab here** types
    it and the next call goes straight through; **Deny** refuses and a retry within 60 s gets no
    dialog; no answer for 120 s closes it; a dialog on a row in a hidden workspace
  - `session restore` on a host-created row reattaches after an agterm restart
  - a `decide.py` panel and `show-image.sh` from a remote Claude
  - the skill: on the Mac, "spawn a worker on homelab for X" wraps the spawn in `agr open …
    --command`; on the host, "hand this to <local row>" calls `session type` and the dialog
    appears
  - Ctrl-C during a `--block` overlay closes it
  - with a consent dialog open, typing letters in that row picks no button (only Return, which
    denies)
  - on a Linux host with zmx: a new session sees the exported `AGTERM_*`, and a re-attach keeps
    the old values (Spike results > Environment)
- Move this plan to `docs/plans/completed/` once the run has ended.
- Restart the Mac `agr daemon` after upgrading. Release notes: `brew upgrade agr`, restart the
  daemon, `agr install <host>`, re-open rows to pick up the environment; a breaking note for
  cookbook-format `session_id` status senders.
