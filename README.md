# agterm-remote (`agr`)

`agr` brings agent status from a remote host running zmx or tmux into the
[agterm](https://github.com/umputun/agterm) sidebar on your Mac. The agent
stays remote and survives SSH disconnects; status, colors, and notifications
return through a self-healing bridge.

## Quick start

Install the Mac binary and keep its daemon running:

```sh
brew install k0nsta/tap/agr
# or: go install github.com/k0nsta/agterm-remote/cmd/agr@latest

agr daemon &
agr install <host>
agr open <host> <name>
```

Run `agr open` from the agterm session that should own the remote session.
Repeat it with the same name to reconnect to the same agent from another Mac.

The complete Mac-side command list is:

```text
usage: agr <command> [args...]

Commands:
  open <host> [name]       Attach to a remote session; without name, use the picker
  ls <host>                List agr-owned remote sessions
  kill <host> <name>…      Kill one or more agr-owned remote sessions
  up <host>                Start the host's status bridge
  down <host>              Stop the host's status bridge
  install <host> [--mux zmx|tmux]
                          Install the remote script and agent hooks
  daemon                  Run the local bridge daemon
  doctor <host>            Check local and remote prerequisites

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
agr open homelab api

# List owned sessions. The ROW column says bound, stale, or -.
agr ls homelab

# Open the native picker instead of naming a session.
agr open homelab

# Stop one or more owned sessions.
agr kill homelab api infra

# Control the shared bridge explicitly (the daemon must be running).
agr up homelab
agr down homelab

# Re-probe the host and install the embedded remote script and hooks.
agr install homelab --mux zmx

# Print local, remote, and daemon diagnostics.
agr doctor homelab
```

`agr ls` reports the remote command and the elapsed age of its last event.
The `ROW` value is live only when agterm's tree can be queried; otherwise it
is shown as `-`.

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
- `mosh-server` is optional. `agr install` probes the host and persists the
  selected multiplexer, relay, and zmx socket directory.

## Failure modes

| Situation | Behavior |
| --- | --- |
| Network or bridge drop | Hooks no-op, the agent keeps running, and the daemon reconnects. The last level is pushed again after reconnect. |
| agterm quits | The relay's short socket timeout drops the event; the next agterm attach rebinds the row. |
| Switch Macs | The new bridge reclaims the per-host socket and `open` rewrites the session binding. |
| No agterm client | The remote agent continues normally; status delivery is simply unavailable. |
| Event while fully offline | It is not queued. The most recent level is restored when the bridge reconnects. |
| Local and remote versions differ | `ls`, `kill`, and `doctor` report a warning; run `agr install <host>` to upgrade the remote script. |

## Security

The forwarded Unix socket exposes agterm's control API, including operations
that can inject text into terminals. Treat the remote host as trusted as a
local terminal. The socket is user-owned, is not a TCP listener, and travels
over the authenticated SSH connection; do not use agr with an untrusted host.

## Upgrading from agr 0.4 or 0.3

Version 1.0 is a Go binary. The old root `agr` script and `install.sh` are no
longer shipped. Remove an old checkout symlink before installing the new
binary if necessary:

```sh
if [ -L "$HOME/.local/bin/agr" ]; then rm "$HOME/.local/bin/agr"; fi
brew install k0nsta/tap/agr
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
