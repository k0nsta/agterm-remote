# agterm-remote (`agr`)

Bring [agterm](https://github.com/umputun/agterm)'s agent-status sidebar — the
blue/amber/green indicators and desktop pushes — to AI coding agents that run on
a **remote host inside tmux**, reached over SSH.

Run agterm on your Mac as a thin front-end to a headless Linux box, keep your
agents alive in remote tmux across disconnects and device switches, and still
get per-session colors and "agent needs you" notifications on the Mac.

---

## The problem

agterm's agent integration is built for **local** agents. Its hooks call
`agtermctl session status …`, which talks to a **local** Unix-domain control
socket, targeting the session via `AGTERM_SESSION_ID`. That works beautifully
when the agent runs in an agterm session on the Mac.

It breaks the moment the agent runs on a remote box in tmux:

1. **No binary, no socket on the remote.** `agtermctl` and the app socket only
   exist on the Mac.
2. **Env vars don't cross.** SSH doesn't forward `AGTERM_SESSION_ID` /
   `AGTERM_SOCKET`, and tmux freezes the environment of already-running panes —
   so a reconnect from a *different* Mac would target a stale/incorrect row.
3. **tmux buffers off-screen panes.** Even agterm's in-band OSC notifications
   don't reach the Mac from a background agent pane until you look at it — the
   exact moment you wanted the push.

So with plain remote tmux you get no colors and no pushes for remote agents.

## The solution

`agr` bridges the agent's status events back to your Mac's agterm **out of
band**, so it's independent of which tmux pane is focused:

- A single **reverse-forwarded control socket** carries your Mac's agterm socket
  to a fixed path on the remote host (`ssh -R`), kept alive and self-healing.
- A tiny **relay** on the remote speaks agterm's JSON control protocol to that
  forwarded socket. Your agent hooks run unchanged; they just reach the Mac's
  app through the tunnel.
- Each session's **target row id is written to a file keyed by tmux session
  name** on every connect — defeating tmux's frozen-env problem and staying
  correct across reconnects and across Macs.

Non-agterm clients (iPhone, Windows, Linux terminals) attach the same tmux
sessions exactly as before; the status layer is additive and self-guarding, so
it simply no-ops when no Mac is attached.

---

## How it works

```
  Mac (agterm)                         remote host (tmux + agent)
  ┌───────────────────┐                ┌──────────────────────────────┐
  │ agterm.app        │                │  claude / codex / …          │
  │  control socket ◄─┼── ssh -R ──────┤   hooks → agr status <state> │
  │                   │  (agr up)      │            │                 │
  │  sidebar row  ◄───┼── JSON ────────┤   agr relay → forwarded sock │
  └───────────────────┘                │   target ← targets/<tmux name>│
        ▲                              └──────────────────────────────┘
        │ agr open <host> <name>: relabel row, ensure tunnel,
        │ ssh -t → agr attach → tmux new-session -A -s <name>
```

Mapping: **one agterm session (you create) ↔ one `agr open <host> <name>` ↔ one
remote tmux session `<name>`.** Reconnect with the same name from any Mac and it
re-attaches the same running agent and re-labels the row.

### Components

| Command | Side | Role |
|---|---|---|
| `agr open <host> <name>` | Mac | Adopt the current agterm session: relabel row, ensure tunnel, attach remote tmux. |
| `agr up` / `agr down` | Mac | Start / stop the shared control tunnel (autossh if present, else a reconnect loop). |
| `agr install <host>` | Mac | Copy `agr` to the host and wire Claude Code hooks. |
| `agr quick [id]` | Mac | Remote-aware quick terminal: remote-shell overlay on agr sessions, else the local quick terminal. |
| `agr doctor <host>` | Mac | Check prerequisites on both sides. |
| `agr attach <name> [id]` | remote | Record the row id, `tmux new-session -A -s <name>` (invoked over SSH). |
| `agr status <state>` | remote | Hook entry point: resolve this tmux session's target, then relay. |
| `agr relay …` | remote | Pure transport: one line of JSON to the forwarded socket. |

State files: `~/.cache/agterm/agterm.sock` (forwarded socket) and
`~/.cache/agterm/targets/<name>` (per-session row id) on the remote;
`~/.cache/agr/` (pidfiles, cached remote `$HOME`) on the Mac.

---

## Requirements

- **Mac:** macOS + [agterm](https://github.com/umputun/agterm). `autossh` and
  `mosh` recommended (`brew install autossh mosh`) but optional.
- **Remote:** `tmux ≥ 3.x`, `python3`, SSH access. Any Linux/BSD/WSL host.
  `mosh` optional (recommended) for drop-tolerant interactive sessions.
- SSH that supports Unix-domain socket forwarding (OpenSSH ≥ 6.7) and
  `StreamLocalBindUnlink`.

## Install

```sh
git clone https://github.com/k0nsta/agterm-remote ~/github.com/agterm-remote
cd ~/github.com/agterm-remote
./install.sh                     # symlinks ./agr into ~/.local/bin

agr install <host>               # push agr to the remote + wire Claude Code hooks
agr doctor  <host>               # verify both sides
```

`<host>` is any alias in your `~/.ssh/config`.

## Usage

```sh
# In a fresh agterm session on the Mac:
agr open homelab api             # row becomes "api"; you're in remote tmux "api"

# Later, from any Mac — same command re-attaches the same running agent:
agr open homelab api
```

Open several agterm sessions, each `agr open homelab <name>` with a different
name, to run independent agents with independent colored rows.

### Remote-aware quick terminal

agterm's built-in quick terminal (`ctrl+\``) always opens a *local* shell. Bind
it to `agr quick` instead and it becomes context-aware: in an `agr` session it
drops a floating **remote** shell overlay on that host; in a local session it
falls back to the built-in local quick terminal. `agr open` records which host
each row is bound to, and `agr quick` opens the overlay via
`agtermctl session overlay`.

Add to `~/.config/agterm/keymap.conf`, then run `agtermctl keymap reload`:

```
command "Quick shell (remote-aware)" ctrl+` $HOME/.local/bin/agr quick "$AGT_SESSION_ID"
```

### From other clients (iPhone, Windows, another Linux box)

The sessions `agr` creates are plain tmux sessions, reachable from **any** SSH
client by name:

```sh
ssh homelab
tmux ls                 # api, infra, …
tmux attach -t api      # full agent interaction; colors pause (no agterm here)
```

Colors/pushes are macOS-agterm-only and resume the next time you
`agr open homelab api` from a Mac (which rewrites the target row).

### Don't force everything into one tmux session

Per-session colors rely on each agent living in its **own named session** — the
hook resolves the row from `tmux display-message -p '#S'`. A common footgun is a
shell-rc rule that auto-attaches every SSH login to a single shared session
(e.g. `main`): agents run as windows there all report the same `#S`, so their
colors collide. Prefer a **bare login** (land at a shell; attach by name) so
`agr open <host> <name>` is the only thing that creates sessions. Example rc
snippet:

```zsh
if command -v tmux >/dev/null && [ -n "$SSH_CONNECTION" ] && [ -z "$TMUX" ] && [[ $- == *i* ]]; then
  tmux ls 2>/dev/null && echo "attach with: tmux attach -t <name>"
fi
```

(`agr` itself is unaffected either way — it connects with a non-interactive
command that bypasses login-shell auto-attach — but a bare login keeps the
named-session model clean.)

## Claude Code hook mapping

`agr install` writes these into the remote `~/.claude/settings.json`
(mirroring agterm's local Claude Code integration):

| Event | State |
|---|---|
| `UserPromptSubmit` | `active --blink` |
| `PostToolUse` | `active --blink` |
| `Notification` (`permission_prompt`) | `blocked` |
| `Stop` | `completed --auto-reset` |

Other agents (Codex, etc.) can call `agr status <state>` from their own notify
hooks the same way.

---

## Reliability & failure modes

The design separates two lifetimes so only one needs to be robust:

- **Agent survival = tmux.** `new-session -A` means a dropped network never
  touches the agent; you reconnect and you're back on it.
- **Status bridge = best-effort, self-healing.** `ServerAlive*` detects dead
  links, `StreamLocalBindUnlink` reclaims a stale remote socket on reconnect,
  and autossh (or the fallback loop) re-establishes the tunnel.
- **Interactive resilience = mosh (optional).** SSH treats one corrupted packet
  (Wi-Fi glitch, sleep/wake, VPN roam) as fatal — `Bad packet length … Connection
  corrupted`. If `mosh` is present on both ends, `agr open` uses it instead of
  SSH, so the view survives drops and just resyncs. `agr open` also resets local
  terminal modes on exit, so an abrupt drop never dumps escape-code garbage into
  your shell.

| Situation | Behavior |
|---|---|
| Bridge down (network drop) | `agr status` no-ops; agent unaffected; tunnel auto-restores; next event re-syncs the color. |
| Mac app quit, tunnel still up | Relay's 0.3s timeout → no-op. |
| Switch Macs | Newest `agr up` reclaims the socket; next `agr open` rewrites the target files to the new Mac. |
| Non-agterm client only | No socket → relay no-ops; plain tmux, no colors, no errors. |
| State changed while fully offline | Not retro-pushed; you see it on reattach, next event re-syncs. |

## Security

The reverse-forwarded socket is agterm's **full control API** (including
`session type`, which injects text into terminals). So the trust boundary
becomes "the remote host is as trusted as a local terminal on your Mac." The
socket is a Unix file in your home directory (user-only perms), never a TCP
port, and rides your existing authenticated SSH. For a single-user host you
reach over LAN/VPN this is an accepted trade-off; don't point `agr` at a host
you don't fully trust.

## License

MIT. Not affiliated with agterm; built to complement it.
