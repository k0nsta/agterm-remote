---
name: agr
description: Work with remote hosts through agr, the remote-session bridge for agterm. Use when the user says "on <host>", "from <host>", "remote session", "remote worker", "hand off to <row>", "spawn on <host>", "tear down on <host>", or names a host that `agr ls <host>` accepts; also when AGTERM_SOCKET=agr-proxy, which means this Claude runs in a remote row.
---
<!-- agr-skill @VERSION@ -->

# agr: agterm on a remote host

A remote row is an agterm session attached to a tmux or zmx session on a host, opened with
`agr open <host> <name>`. Keep your own spawn and teardown skills; wrap the program they start.

## Spawn on a host (from the Mac)

- `agtermctl session new --command X` becomes:
  `agtermctl session new --command "$(command -v agr) open <host> <name> --cwd <dir> --command 'X' --orchestrator $AGTERM_SESSION_ID:$AGTERM_PANE_ID"`
  Run it from the orchestrator's own shell so the variables expand to the orchestrator.
- The new row starts with the app's PATH, which may not include `agr`; `$(command -v agr)`
  expands to its absolute path in your shell before agterm sees it.
- `<dir>` is an absolute path on the host (`/home/me/repo`, not `~/repo`); leave out `--cwd`
  for the host's home.
- If X contains `'`, write X to a script on the host (`ssh <host>`) and pass `--command 'sh <script>'`.
- Filesystem steps (worktree, brief file) run over `ssh <host>`.
- Confirm it started with `agtermctl session text`, not `foreground`: a remote row's foreground is `agr`.

## Teardown

- `agr end <row>`: the user confirms, then the host's sessions are killed and the row closed.
- Or `agr kill <host> <name>` plus `agtermctl session close`; clean a remote worktree over `ssh <host>`.

## On a remote row (AGTERM_SOCKET=agr-proxy)

`agtermctl` here is agr's proxy to the Mac's agterm.

- Use full ids from `agtermctl tree --json`.
- Only an allowlist of commands works; a refused one says why.
- You may write only to rows bound to this host, including rows you created with `session new`.

## Show a page on the Mac

The command is `agtermctl session overlay open` (there is no top-level `overlay`); it covers your own pane.

- Interactive page (decide panel, form): `agtermctl session overlay open --html page.html --js --block --target "$AGTERM_SESSION_ID" --pane "$AGTERM_PANE"`.
  One self-contained file; it can only submit and close. Prints `{"outcome":"submitted","value":…}`.
- Web page: `agtermctl session overlay open --url https://… --target "$AGTERM_SESSION_ID" --pane "$AGTERM_PANE"`. No `--js`.
- Refused from a host: `--persistent`, and `--size-percent` together with `--pane`. `--follow` is ignored with a warning.
- A page behind a login (a claude.ai artifact) opens signed out. Show its HTML with `--html` instead, or give the user the link.

## Hand off to a local agent

- Find the target row by name in `agtermctl tree --json`, then
  `agtermctl session type --target <id> [--pane-id <p>] --stdin < message.txt`.
- Only a local pane running `claude` or `codex`; plain text, no control characters, at most 64 KiB.
- The user gets a dialog (Allow once / Allow this host here / Deny). On refusal say so, and fall
  back to the clipboard.

## Messages between sessions on the same host

Use tmux directly, not the proxy; it works for detached sessions and never touches the Mac.

- Send: `tmux send-keys -t <name> -l "text"`, then `tmux send-keys -t <name> Enter`.
- Read: `tmux capture-pane -p -t <name>`.
- Under zmx use the proxy's `session type`; both rows must be attached.

## From the Mac to a remote row

Plain `agtermctl session type` works.
