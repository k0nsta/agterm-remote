# agterm-remote (`agr`)

`agr` brings agent status from a remote host running zmx or tmux into the
[agterm](https://github.com/umputun/agterm) sidebar on your Mac. The agent
stays remote and survives SSH disconnects; status, colors, and notifications
return through a self-healing bridge.

## Quick start

Install the Mac binary and keep its daemon running:

```sh
brew install k0nsta/apps/agr
# or: go install github.com/k0nsta/agterm-remote/cmd/agr@latest

agr daemon &
agr setup            # ⌘D/⌘J follow a remote row; asks for an End key
agr install <host>
agr open <host> <name>
```

Run `agr open` from the agterm session that should own the remote session.
Repeat it with the same name to reconnect to the same agent from another Mac.

The complete Mac-side command list is:

```text
usage: agr <command> [args...]

Commands:
  open <host> [name] [--cwd <dir>] [--parent <name>]
                          Attach to a remote session; without name, use the picker
  ls <host>                List agr-owned remote sessions
  kill <host> <name>…      Kill one or more agr-owned remote sessions
  up <host>                Start the host's status bridge
  down <host>              Stop the host's status bridge
  install <host> [--mux zmx|tmux]
                          Install the remote script and agent hooks
  daemon                  Run the local bridge daemon
  doctor <host>            Check local and remote prerequisites
  shell <host> [--cwd <dir>]
                          Open a plain remote login shell (no session)
  end [row]                Kill a row's remote sessions and close the row
  paste [row] [pane-id]   Send clipboard files/image or latest screenshot to a remote row

Options:
  --help, -h               Show this help
  --version                Show the agr version
```

With no name, `open` lists the host's owned sessions in agterm's native picker;
you can select one or type a new name. The picker requires `agtermctl` and an
agterm session.

## How it works

`agr open` records the Mac pane binding (both panes of a split row can hold
one), starts the local daemon's bridge for the host, and attaches to the named
remote multiplexer session. The daemon maintains one reverse SSH-forwarded
Unix socket per host. Remote hooks send
events through the installed relay; the daemon resolves the session binding
and updates agterm directly.

The remote script uses zmx by default when available and otherwise uses tmux.
It marks sessions as agr-owned, so `ls` and `kill` never operate on arbitrary
user sessions. Under tmux, `open` also sets the server's `Ms` clipboard
capability so copy-mode selections reach the Mac clipboard over mosh, which
forwards OSC 52 only for selection `c`; clients attached earlier must
re-attach. `install` also installs the Claude Code hooks; other agents can
call the remote status entry point from their own hooks.

```text
Mac: agterm ← agr daemon ← per-host receiver ← ssh -N -R
                                                     ↓
Remote: agent hook → agr relay → forwarded Unix socket → agterm
```

The supported levels are `active`, `blocked`, `completed`, and `idle`. In
`agr ls`, `IDLE` is the time since the last agent status event, not terminal
inactivity. There is no `WIN` column: agr sessions are named multiplexer
sessions, not windows in a shared session.

## Usage

```sh
# Start or attach to a named remote session from an agterm row.
agr open remote api

# List owned sessions. The ROW column says bound, stale, or -.
agr ls remote

# Open the native picker instead of naming a session.
agr open remote

# Stop one or more owned sessions.
agr kill remote api infra

# Control the shared bridge explicitly (the daemon must be running).
agr up remote
agr down remote

# Re-probe the host and install the embedded remote script and hooks.
agr install remote --mux zmx

# Print local, remote, and daemon diagnostics.
agr doctor remote

# A plain login shell on the host, in a given directory (no session).
agr shell remote --cwd /srv/app
```

`agr ls` reports the remote command and the elapsed age of its last event.
The `ROW` value is live only when agterm's tree can be queried; otherwise it
is shown as `-`.

## agterm keys on a remote row

On a row opened with `agr open`, agterm's own keys can follow the remote host:

| Key | Local row | Row running `agr open remote a1` |
| --- | --- | --- |
| ⌘D | local split | `a1-2` on the remote host, in `a1`'s current directory |
| ⌘J | local scratch | a plain shell on the remote host, in `a1`'s current directory |
| End key (yours, e.g. ctrl+a x) | — | asks, then ends `a1` and `a1-2` and closes the row |

### Setup

```sh
agr setup                        # asks for the End key; Enter skips it
agr setup --end-key 'ctrl+a>x'   # bind or change it later
agr setup --paste-key 'ctrl+a>v' # Paste gets ctrl+a>v when first added and free
```

It writes these lines with the binary's own path, then reloads agterm. Lines
that already run agr only get the path updated, so your key, or a key you
removed, stays as it is:

```text
# hooks.conf
on pane.split     '/opt/homebrew/bin/agr' on-split
on pane.scratch   '/opt/homebrew/bin/agr' on-scratch

# keymap.conf (with no key, run it from agterm's command palette)
command "End remote session" ctrl+a>x --error-hud '/opt/homebrew/bin/agr' end "{AGT_SESSION_ID}"
command "Paste to remote" ctrl+a>v --error-hud '/opt/homebrew/bin/agr' paste "{AGT_SESSION_ID}" "{AGT_PANE_ID}"
```

Run `agr install <host>` once per host so it has the current remote script.
Tested on agterm 0.35.

### Paste a screenshot (or files)

On a row opened with `agr open`, press `ctrl+a>v` to upload local content to
the remote host and type its remote path into the pane without submitting the
shell line. `agr setup` binds that chord when it is free; use
`agr setup --paste-key 'ctrl+a>v'` to choose a different chord. The same
operation is available as `agr paste [row] [pane-id]`.

The sources are checked in this order:

- regular files copied in Finder with `⌘C` (several files are supported; these
  take precedence over Finder's clipboard icon image);
- image data on the Mac clipboard, written as a PNG;
- the newest regular screenshot in macOS's screenshot folder, only when it is
  less than five minutes old.

Each file is uploaded to the host selected by the pane binding and stored at
`~/.cache/agr/paste/` with a safe name and mode `0600`. Files older than seven
days are pruned when an upload runs. The remote `paste-put` verb is installed
by `agr install <host>`; run that command on every host before using this
feature after upgrading `agr`. If no source is available, or the row is local,
`agr` reports an error through agterm's error HUD.

There is a macOS screenshot timing caveat: with the floating thumbnail enabled,
macOS may not write the screenshot file until roughly five seconds after the
capture. Pressing the paste key sooner can find an older screenshot or none at
all. Wait for the thumbnail to disappear, click the thumbnail to dismiss it,
or disable the delay with:

```sh
defaults write com.apple.screencapture show-thumbnail -bool false
```

The paths arrive as one bracketed paste, so Claude Code shows each image as
`[Image #N]`, as it does for a local drag. Your Mac clipboard is not touched.

### What each piece is

| Piece | What it is | ⌘W closes the row | End (`agr end`) |
| --- | --- | --- | --- |
| `a1` | agr session | keeps running | killed |
| `a1-2` (⌘D) | agr session, child of `a1` | keeps running | killed |
| scratch (⌘J) | plain ssh/mosh shell | ends | ends |

⌘W only detaches, so an agent survives a disconnect or a move to another Mac.
`agr end` is the way to stop the work. `agr kill <host> a1` also takes `a1`'s
children.

### Details

- ⌘D acts once per pane: hiding and showing the split again does nothing, and
  a pane you bound yourself is left alone.
- `exit` in the remote scratch closes it; the next ⌘J opens a fresh one.
- The directory comes from tmux, or from zmx on Linux hosts. Elsewhere, or
  with an older remote script, the new session starts in the remote home.
- `agr end` does nothing until you press End. If the host cannot be reached,
  it keeps the row so you can run it again.
- Hook errors are written to `~/.cache/agr/hooks.log`.

## Remote agterm control

On a row opened with `agr open`, `agtermctl` also works on the host. `agr install`
puts a proxy there; each call goes over the bridge to the Mac, is checked, and
runs the real `agtermctl`. `agr open` also exports `AGTERM_ENABLED`,
`AGTERM_SESSION_ID`, `AGTERM_PANE`, `AGTERM_PANE_ID` and `AGTERM_SOCKET=agr-proxy`
into the remote session, so tools that look for agterm find it.

```sh
# on the host, inside an agr row:
agtermctl session overlay open --html /tmp/panel.html --js --block
{"outcome":"submitted","value":"…"}
```

- **Works unchanged**: `decide` panels, `show-image`, `session text` / `type`
  between rows, `notify`, `session status`, `session new` (the new row runs on
  the same host).
- **Allowlist**: only listed commands and options work. Everything else is
  refused with the reason (`pick`, `ask`, `workspace`, `session paste`,
  `session scratch --command`, `--url … --persistent`, …).
- **Full ids only**: `--target` and `--pane-id` take full ids from
  `agtermctl tree --json`. Prefixes and `active` are refused.
- **One-file pages**: an HTML page must be a single self-contained file.
- **`foreground` limitation**: a remote row's `foreground` in `tree --json` is
  `agr`, not the remote program. Tools that poll it (such as `spawn-agent`'s
  "is it running" check) see `agr`; read `session text` instead.
- **New leaf**: a command that is not in the allowlist is added only on request.
  Open an issue with the command line and what it is for.

### Workers on a host

```sh
agr open <host> <name> --cwd <dir> --command '<program>'   # runs only when the session is new
agr run <host> [--cwd <dir>] -- make test                   # one-shot ssh -t, exit code passed through
```

`--command` and the environment need the host's agr to be 1.3.0 or newer
(`agr install <host>`).

### Handing off to a local agent

A remote session may `session type` into a local pane that runs `claude` or
`codex`. The first time, agterm shows a dialog on that pane, plus a desktop
notification for its row: **Allow once**, **Allow `<host>` here until it
closes**, or **Deny**. Return denies, and so does no answer within 2 minutes.
Nothing is typed until you allow it. Text typed later under the grant posts a
notification too, which leaves agterm's unseen badge on the row.

### The `agr` skill

`agr setup` installs `~/.claude/skills/agr/SKILL.md` on the Mac and
`agr install <host>` installs it on each host. A file without agr's
`<!-- agr-skill -->` marker is yours and is left alone. `agr skill` prints it
for agents that read instructions elsewhere.

Your own skills compose with it: keep your spawn skill and wrap the program it
starts, for example `agtermctl session new --command "agr open <host> <name>
--command '<program>'"`. The skill tells Claude how.

## agterm versions

agterm 0.26 is recommended. Enable its Live sessions restore mode so pane
processes survive an agterm relaunch, and use `session context` for the durable
purpose line that `agr open` sets to `<host> · <name>`. `doctor` reports these
capabilities and shows `n/a (agterm < 0.26)` where an older agterm cannot
provide them.

agterm's own remote sessions are Mac-to-Mac sessions. They do not carry agent
status from a Linux or BSD multiplexer over SSH, so they do not replace agr's
remote relay and per-host forwarding.

## Requirements

Mac side:

- macOS, agterm, and `agtermctl` (agterm 0.25 or newer; 0.26 recommended);
- OpenSSH 6.7 or newer with Unix-domain socket forwarding;
- `autossh`/`mosh` are optional conveniences (`brew install autossh mosh`).

Remote side:

- POSIX `sh`, SSH access, and zmx or tmux (zmx is preferred when installed);
- one relay with Unix-socket support: OpenBSD `nc -U`, `python3`, or `socat`;
- `python3` (the `agtermctl` proxy needs it);
- `mosh-server` is optional. `agr install` probes the host and persists the
  selected multiplexer, relay, and zmx socket directory.

Versions: the Mac daemon and the remote script must both be 1.3.0 or newer for
`--command`, the environment and the proxy. The proxy needs agterm 0.36.0;
with an older agterm every proxied call fails and everything else keeps working.

## Failure modes

| Situation | Behavior |
| --- | --- |
| Network or bridge drop | Hooks no-op, the agent keeps running, and the daemon reconnects. The last level is pushed again after reconnect. |
| agterm quits | The relay's short socket timeout drops the event; the next agterm attach rebinds the row. |
| Switch Macs | The new bridge reclaims the per-host socket and `open` rewrites the session binding. |
| No agterm client | The remote agent continues normally; status delivery is simply unavailable. |
| Event while fully offline | It is not queued. The most recent level is restored when the bridge reconnects. |
| Bridge down | `agtermctl (via agr): the bridge to the Mac is down (run agr up <host> on the Mac)` |
| Old or stopped daemon | `agtermctl (via agr): the Mac's agr daemon is too old or not running` |
| Bridge dropped mid-call | `agtermctl (via agr): the bridge to the Mac dropped` |
| Not in an agr row | `agtermctl (via agr): not inside a row opened with agr open` |
| No python3 on the host | `agtermctl (via agr): python3 is required on this host` |
| agterm older than 0.36.0 | `agtermctl (via agr): needs agterm 0.36.0 (found <v>)` |
| Refused call | `agtermctl (via agr): <reason>`, exit 1 |
| Local and remote versions differ | `ls`, `kill`, and `doctor` report a warning; run `agr install <host>` to upgrade the remote script. |

## Security

The forwarded Unix socket exposes agterm's control API, including operations
that can inject text into terminals. Treat the remote host as trusted as a
local terminal. The socket is user-owned, is not a TCP listener, and travels
over the authenticated SSH connection; do not use agr with an untrusted host.

### Remote agterm control

- **Scoped per pane**: a host may read, and may write only to panes bound to it
  (including rows it created). A write also needs the pane to be attached, that
  is, still running `agr open`; a detached pane is a local Mac shell and is
  refused. Calls that run or read something on the Mac are refused.
- **Pages from a host** are never opened as agterm `--html` pages, whose bridge
  could run any command. They are served by a loopback shim with no agterm
  bridge: only submit and close work, through `agterm.request` or
  `data-agterm` buttons and forms, and a CSP keeps page JavaScript on its own
  server. `--url` pages from a host are static (no `--js`). The warning for
  localhost URLs is a hint, not a control.
- **`events`** streams the Mac's whole event feed, not just this host's rows.
- **Grants**: a grant lets a host type into one local pane. Anything on that host
  may use it, not just one session.
  - *Orchestrator*: `agr open --orchestrator <row>:<pane>` lets the host's
    session message the pane that spawned it.
  - *Consent*: the dialog above; "here until it closes" lasts as long as the
    caller's binding, re-attaches included.
  - Local `claude` or `codex` panes only, `session type` only, plain text up to
    64 KiB (control and invisible characters are refused). A refused or
    unanswered dialog blocks that host's requests to the same row for 60 s.
- **Status events**: a `session_id` status event is applied only to rows with a
  pane bound to the sending host. Cookbook senders on rows not opened with
  `agr open` are dropped.
- **Real agtermctl**: the proxy is skipped on hosts that already have one.

## Upgrading to 1.3

Run `agr install <host>` for each host, then re-open its rows with `agr open`
to get the `AGTERM_*` environment. Agents started before the upgrade need a
restart to see agterm.

## Upgrading from agr 0.4 or 0.3

Version 1.0 is a Go binary. The old root `agr` script and `install.sh` are no
longer shipped. Remove an old checkout symlink before installing the new
binary if necessary:

```sh
if [ -L "$HOME/.local/bin/agr" ]; then rm "$HOME/.local/bin/agr"; fi
brew install k0nsta/apps/agr
agr install <host>
```

`agr install` replaces the remote script and merges the current hooks. It also
copies the Mac terminal's terminfo entry (`$TERM`, `xterm-ghostty` under agterm)
to the host when the host lacks it — without that entry tmux and everything
under it cannot see the terminal's capabilities (clipboard, keys), and `doctor`
reports the gap as `terminfo (<TERM>): MISSING`. Sessions
created by 0.4 with a non-empty `@agr_target` remain recognized as owned.
Sessions from 0.3's `~/.cache/agterm/targets` are reported by `doctor` as
legacy and are not listed until you adopt them with `agr open`; after that,
the old target files can be removed on the remote host.

## License

MIT. Not affiliated with agterm; built to complement it.
