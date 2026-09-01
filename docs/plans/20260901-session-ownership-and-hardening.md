# agr 0.4.0 — session ownership (ls / kill / picker), version handshake, bridge hardening

## Overview

`agr open` is create-or-attach with no discovery: once you detach from a remote tmux
session and forget its name, nothing in `agr` will ever show it again, and forgotten
sessions pile up on the host. Separately, the Mac-side and remote-side copies of `agr`
drift apart silently.

This plan adds:

- an **ownership marker** so `agr` sees exactly the sessions it made — a tmux session
  user-option `@agr_target` set on every `agr open` — and drops the write-only
  `~/.cache/agterm/targets/` directory it replaces;
- `agr ls <host>` (owned sessions with pane command, idle time, and whether the bound
  agterm row still exists), `agr kill <host> <name>…`, and a native fuzzy picker when
  `agr open <host>` is run without a name;
- a **version handshake**: `agr\t<version>` header on the data-returning remote commands
  (`sessions`, `reap`), a version probe folded into the ssh call `agr up` already makes
  when it starts a bridge (so every `open` is covered once per bridge lifetime with no
  extra round-trip), and a `doctor` that reports both versions and the bridge socket;
- five bug fixes (B1–B5) plus one one-liner (B6): `agr down` orphaning the fallback
  tunnel, that tunnel spamming the tty, `AGTERM_SESSION_ID` interpolated unvalidated into
  a remote shell string, `agr up` racing itself, non-atomic rewrites of the remote
  `~/.claude/settings.json`, and the escape-reset printed to a non-tty;
- one hole found during plan review: the `status` hook resolves its session with a
  bare `tmux display-message`, which — when run *outside* tmux while a server is up —
  happily returns some other session's value. Fixed by targeting `$TMUX_PANE`.

Deliberately **not** in scope (backlog): python→nc relay for hook latency,
`agr uninstall`, bats + GitHub Actions, `valid_token` rejecting leading `-` / `.` / `..`,
cache-filename sanitisation, zombie heuristics / prune / idle TTL / `kill --force`,
an end-to-end relay probe in `doctor` (cannot fail — the relay swallows every error —
and has a visible sidebar side effect).

## Context (from discovery)

- Single bash script `agr` (330 lines, `VERSION=0.3.0`), installed on both sides via
  `install.sh` (Mac symlink) and `agr install <host>` (scp to remote). `README.md`
  documents architecture; no tests, no CI.
- Naming convention: Mac verbs are user-facing (`open up down install quick doctor`),
  remote verbs are mechanical (`attach status relay`), paired 1:1 (`open → attach`).
  New pairs follow it: `ls → sessions`, `kill → reap`.
- `cmd_quick` (`agr:196-207`) already has a python `agtermctl tree --json` walker — reuse
  the pattern for the `ROW` column.
- agterm session ids are uppercase UUIDs (`A69B0B26-…`) — inside `[A-Za-z0-9_.-]`.
- **Verified tmux facts (3.7b):** `has-session`, `kill-session`, `list-panes -s` accept
  `-t "=name"` (exact match); `set-option` / `show-option` **reject** it and need
  `-t "=name:"` (trailing colon); `list-sessions -F '#{@agr_target}'` resolves the
  option per session; `display-message -p -t "$TMUX_PANE" '#{@agr_target}'` resolves it
  from inside a pane; `new-session -Ad` on an *existing* session still tries to attach
  (needs a tty) — not usable as create-if-missing.
- **Verified shell facts:** `/usr/bin/env bash` is **3.2** on macOS — no `mapfile`,
  `declare -A`, `${x,,}`; `[[ … ]] && main` at file end under `set -e` returns 1 and
  **kills a sourcing shell** — use `if …; then main; fi`; `IFS=$'\t' read` collapses
  empty middle fields; `printf %q` escapes a literal `$HOME`.
- macOS ships neither `setsid` nor `flock`; lock and process handling use `mkdir`/`trap`.
- The current `shellcheck -S style` baseline is **not** clean (three SC2029 infos at
  agr:104/121/145); the new `remote_cmd` intentionally trips SC2016.

## Development Approach

- **testing approach**: Regular (code first, then verification). No test framework in
  this pass (bats/CI are backlog). Every task ends with a concrete, repeatable check:
  - **shellcheck gate per task:** `shellcheck -S warning agr` clean (info-level SC2029
    is tolerated until Task 8 removes the last interpolated ssh string). Final gate in
    Task 11: `shellcheck -S style -e SC2016 agr` clean.
  - remote-side logic is exercised against a throwaway tmux server (`tmux -L agr-test`)
    through a `tmux` shim on `PATH`; bridge logic through an `ssh` shim (Technical Details).
  - Mac-side rendering is exercised by sourcing the script and overriding `remote_agr`
    with canned TSV.
  - **each task writes its check as `$SCRATCH/checks/NN-<task>.sh`** so Task 11 can
    re-run them all in one pass. Scratchpad only — not committed.
- Task 1 makes the script **sourceable** so the above is possible without a framework.
- **bash 3.2 compatible** on the Mac side (see Context).
- complete each task fully before moving to the next; the script must stay runnable
  after every task.
- **CRITICAL: update this plan file when scope changes during implementation**
- against a 0.3.0 remote, behaviour is *fail or warn clearly* ("run agr install") — no
  compatibility shims.

## Testing Strategy

- **unit-level**: sourced-function and shim checks per task.
- **e2e**: manual checklist against the real host (Task 11). The picker (Task 6) and the
  `ROW` column can only be verified inside agterm.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope

## What Goes Where

- **Implementation Steps** (`[ ]`): changes to `agr`, `README.md`, this plan.
- **Post-Completion**: real-host items that need a remote box, and the backlog list.

## Implementation Steps

### Task 1: Foundations — version, `valid_token`, `remote_cmd`/`remote_agr`, sourceable script

**Files:**
- Modify: `agr`

- [x] bump `VERSION="0.4.0"`
- [x] rename `valid_name` → `valid_token` (same charset `[A-Za-z0-9_.-]`); update call
      sites; fix its comment (`agr:27-29` still says "target-file keys"); error text:
      `invalid <what> '<value>' (use [A-Za-z0-9_.-])`
- [x] add `remote_cmd <args…>` → prints `"$HOME/.local/bin/agr" <%q-quoted args>`; the
      `$HOME` is a **literal** for the remote shell (single-quoted constant + `$REMOTE_BIN`);
      args via `printf '%q '`; `# shellcheck disable=SC2016` on that line
- [x] add `remote_agr <host> <args…>` → `ssh "$host" -- "$(remote_cmd "$@")"` (no `-t`)
- [x] guard the escape-reset in `cmd_open` (`agr:88`) with `[ -t 1 ]` (B6)
- [x] make the script sourceable: replace bare `main "$@"` with
      `if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi` (the `&&` form returns 1
      and, with `set -e` active, exits the sourcing shell — verified)
- [x] write `$SCRATCH/checks/01-foundations.sh`: `shellcheck -S warning agr`; `bash -n agr`;
      `./agr --version` = `agr 0.4.0`; `bash -c 'source ./agr; remote_cmd attach "a b" ""; echo ALIVE'`
      prints `"$HOME/.local/bin/agr" attach a\ b ''` then `ALIVE`

### Task 2: Ownership marker — `attach` sets `@agr_target`, `status` reads it, `targets/` removed

**Files:**
- Modify: `agr`

- [x] rewrite `cmd_attach`: validate `name` (and `sid` when non-empty) with `valid_token`;
      `tmux has-session -t "=$name" 2>/dev/null || tmux new-session -d -s "$name"`;
      `tmux set-option -t "=$name:" @agr_target "${sid:--}"` (**colon required**);
      `exec tmux attach -t "=$name"`. (Two simultaneous `open`s of the same *new* name can
      race `new-session`; the loser gets tmux's "duplicate session" error and a retry works — accepted.)
- [x] rewrite `cmd_status` target lookup, keeping the hook contract *always exit 0*:
      `[ -n "${TMUX_PANE:-}" ] || exit 0`;
      `target="$(tmux display-message -p -t "$TMUX_PANE" '#{@agr_target}' 2>/dev/null)" || exit 0`;
      empty **or `-`** → exit 0. Drop the `#S` lookup.
- [x] remove `REMOTE_TARGETS` and its uses — but **keep `mkdir -p ~/.cache/agterm`** in
      `cmd_up` (`agr:104`) and `cmd_install` (`agr:143`): it is the parent of the forwarded
      socket, and with `ExitOnForwardFailure=yes` a missing parent kills the bridge. Only the
      `/targets` component and the `cmd_attach` write (`agr:249-250`) go.
- [x] `cmd_open`: validate `sid` with `valid_token` when non-empty (B3); ssh path becomes
      `ssh -t "$host" -- "$(remote_cmd attach "$name" "$sid")"`; mosh path keeps
      `remote_home` + `"$ragr"` argv (mosh-server execs without a shell) with validated args
- [x] write `$SCRATCH/checks/02-ownership.sh` (tmux shim): run the has-session/new-session/
      set-option lines against a new and an existing session; `tmux show-option -t "=a:" -qv @agr_target`
      prints the sid / `-`; `bash -c './agr status active'` outside tmux exits 0 with no
      output (`cmd_status` calls `exit`, so never in the sourced shell); `grep -n targets agr`
      → nothing; `shellcheck -S warning agr`

### Task 3: Remote producer `agr sessions`

**Files:**
- Modify: `agr`

- [x] add `cmd_sessions`: `have tmux || die "tmux not found in non-interactive PATH"`
      (`remote_agr` runs a non-login shell; a Homebrew/`/usr/local` tmux may be absent —
      must not masquerade as "no sessions"); print header `agr<TAB>$VERSION`; if
      `tmux list-sessions` fails (no server) exit 0 after the header
- [x] loop `tmux list-sessions -F '#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_activity}\t#{@agr_target}'`
      (`while IFS=$'\t' read -r …`); skip rows with empty `@agr_target` (last field — safe
      against `read`'s empty-field collapsing) — ⚠️ the `-F` format needs a **real tab
      character** (`$'...\t...'` ANSI-C quoting), not a literal backslash-`t`; tmux 3.7b
      does not interpret `\t` inside a plain single-quoted format string (verified against
      the throwaway server — confirmed with `od -c`)
- [x] per session: `cmds="$(tmux list-panes -s -t "=$name" -F '#{pane_current_command}' 2>/dev/null | sort -u)" || true`;
      drop `sh|bash|zsh|fish|dash`; if nothing left keep the shell names; join with `,`;
      **empty → `-`** (a middle field must never be empty on the wire)
- [x] `idle_secs=$(( $(date +%s) - activity ))` computed on the remote
- [x] emit `name\twindows\tattached\tidle_secs\tcmds\tbound_sid`; wire `sessions)` in `main`
- [x] write `$SCRATCH/checks/03-sessions.sh` (tmux shim): (a) no server → header only, exit 0;
      (b) `owned` with `@agr_target X` + `plain` → exactly one data row, `bound_sid=X`,
      `cmds` = shell name; (c) `sleep 999` running in `owned` → `cmds=sleep`; (d) every row
      has exactly 6 tab-separated fields and `idle_secs` ≥ 0; `shellcheck -S warning agr`

### Task 4: Mac renderer `agr ls <host>`

**Files:**
- Modify: `agr`

- [x] add `remote_data <host> <args…>`: run `remote_agr` capturing stdout and rc
      (`local out; out="$(remote_agr "$@")" || rc=$?` — separate `local` from assignment so
      the status isn't masked); rc 255 → `die "cannot reach '$host'"`; first line not
      `agr<TAB>…` → `die "remote agr on '$host' is missing or outdated — run: agr install $host"`;
      version ≠ `$VERSION` → `log "agr: version mismatch (local $VERSION, $host $rv) — run: agr install $host"`;
      print the remaining lines. Callers: `local rows; rows="$(remote_data "$host" sessions)" || exit $?`
      (the `die` runs in the subshell; the caller must propagate)
- [x] add `humanize_secs`: `<60 → Ns`, `<3600 → Nm`, `<86400 → Nh`, else `Nd`
- [x] add `row_state <sid>`: `-` → `-`; no `agtermctl` → `-`; else look the id up in
      `$AGR_TREE` (a variable `cmd_ls` fills **once** with `agtermctl tree --json`; python
      walk as in `cmd_quick`, printing all session ids) → `bound` if present, `stale` otherwise
- [x] add `cmd_ls <host>`: rows via `remote_data`; none → `echo "no agr sessions on '$host'"`;
      else `printf` table `NAME WIN ATT IDLE CMD ROW`, `ATT` = count or `-`; iterate with
      `while IFS=$'\t' read -r …; done <<< "$rows"` (bash 3.2 — no `mapfile`). `ls` never mutates.
- [x] wire `ls)` in `main`
- [x] write `$SCRATCH/checks/04-ls.sh` (sourced): stub `remote_agr` with canned TSV
      (`api 2 1 180 claude A69B…`, `infra 1 0 172800 zsh DEADBEEF`, `scratch 1 0 18000 node,vim -`)
      and stub `agtermctl` printing a tree containing only `A69B…` → rows show `3m bound`,
      `2d stale`, `5h -`; header `agr\t0.3.0` → warning on stderr, table still printed; no
      header → die with the install hint, non-zero; `remote_agr` returning 255 → "cannot reach";
      header only → "no agr sessions"; `shellcheck -S warning agr`

### Task 5: `agr reap <name>` (remote) and `agr kill <host> <name>…` (Mac)

**Files:**
- Modify: `agr`

- [x] add `cmd_reap <name>`: `have tmux || die …` (as Task 3); print the `agr<TAB>$VERSION`
      header first (it returns data → handshake applies); `valid_token`;
      `tmux has-session -t "=$name"` fails → `die "no session '$name'"`;
      `tmux show-option -t "=$name:" -qv @agr_target` empty →
      `die "'$name' exists but is not agr-managed (use tmux kill-session)"`;
      else `tmux kill-session -t "=$name"` and `echo "killed $name"`
- [x] add `cmd_kill <host> <name>…`: require ≥1 name; each through `valid_token`, then
      `remote_data "$host" reap "$name"` (header check + prints `killed …`); continue on
      per-name failure, exit non-zero if any failed
- [x] wire `reap)` and `kill)` in `main`
- [x] write `$SCRATCH/checks/05-reap.sh` (tmux shim): sessions `owned`/`plain`/`owned2` —
      `reap plain` refused with the not-managed message and `plain` still exists;
      `reap nope` → "no session"; `reap owned` → header + `killed owned`, `has-session`
      fails, **`owned2` untouched** (proves the `=` exact match); `shellcheck -S warning agr`

### Task 6: Picker — `agr open <host>` without a name

**Files:**
- Modify: `agr`

- [x] in `cmd_open`, when `name` is empty and `host` is set: if no `agtermctl` → run
      `cmd_ls "$host"` then `die "usage: agr open <host> <name>"`
- [x] otherwise `rows="$(remote_data "$host" sessions)" || exit $?`; build one line per
      session `name  cmd  row  idle` (extract as `pick_lines`, sourced-testable); pipe to
      `agtermctl pick open --allow-custom --prompt "tmux session on $host — or type a new name"`
      captured with `|| true` (cancel exit code unspecified; `set -e` must not fire first)
- [x] empty result → `exit 1` silently; else `name=${result%% *}`, `valid_token` or die
- [x] fall through into the existing `cmd_open "$host" "$name"` body — no second code path
- [x] write `$SCRATCH/checks/06-picker.sh` (sourced): `pick_lines` on the canned TSV yields
      `api  claude  bound  3m`; `shellcheck -S warning agr`. The picker UI itself is Task 11.

### Task 7: Bridge — fallback loop owns its ssh, logs to a file, backs off; `up` takes a lock and probes the version

**Files:**
- Modify: `agr`

- [x] restructure `cmd_up`: everything that can `die` or needs the network happens
      **before** the lock — `remote_home`, and the existing ssh call, now
      `ssh "$host" -- 'mkdir -p ~/.cache/agterm; "$HOME/.local/bin/agr" --version 2>/dev/null || echo MISSING'`
      (constant string) whose output feeds the version probe: `MISSING` →
      `die "agr not installed on '$host' — run: agr install $host"`; mismatch → `log` warning.
      Runs only when a bridge is actually being started, i.e. once per bridge lifetime.
- [x] lock (B4): `lock="$CACHE_DIR/bridge-$host.lock"`; `mkdir "$lock"` and write `$$` to
      `$lock/pid`. ⚠️ deviation: a losing `mkdir` polls the **bridge's** pidfile (the thing
      actually being waited on) for up to 5s *before* treating the lock as stale, instead of
      checking the lock-holder's own pid first — a real race showed `agr up`'s own pid is a
      poor liveness signal: that invocation normally exits within milliseconds of grabbing
      the lock (it only forks the loop/autossh and returns), so a loser almost always found
      it "dead" despite it having just succeeded, causing a duplicate bridge. Only once the
      5s poll finds no live bridge does a dead recorded holder pid make the lock reclaimable
      (retry once); still no luck → `log "agr: bridge for '$host' not up yet"` and
      **return 0** (bridge is best-effort; `open` must still attach). Inside the critical
      section `trap 'rm -rf "$lock"' EXIT`, and on release `rm -rf "$lock"; trap - EXIT` —
      `cmd_open` continues after `cmd_up`.
- [x] fallback loop (B1, B2): subshell with `trap '' HUP` (closing the row that ran `open`
      must not take the shared bridge down) and
      `trap 'kill "${pid:-}" 2>/dev/null || true; exit 0' TERM INT`;
      `ssh "${opts[@]}" "$host" & pid=$!; wait "$pid" || true` (`|| true` — the subshell
      inherits `set -e`); backoff `delay` 3→6→…→60 capped, reset to 3 when `SECONDS - t0 > 30`;
      redirected `>"$CACHE_DIR/bridge-$host.log" 2>&1 </dev/null` (truncated on each fresh
      `up`; the log covers one bridge lifetime — no rotation)
- [x] `cmd_down`: unchanged plus `rm -rf "$lock"` (a directory — `rm -f` cannot remove it
      and would abort under `set -e`)
- [x] write `$SCRATCH/checks/07-bridge.sh` (ssh shim: `case "$*" in *-N*) exec sleep 300;; *) echo 'agr 0.4.0';; esac`;
      pre-seed `$CACHE_DIR/home-<host>`; `PATH` without `autossh`): `agr up h` →
      `pgrep -f 'sleep 300'` shows one; `agr down h` → `pgrep` empty **and** the subshell gone,
      no lock dir; `bridge-h.log` exists; `agr up h & agr up h; wait` → exactly one `sleep`,
      one valid pidfile, no lock dir; shim printing `agr 0.3.0` → warning, bridge still starts;
      shim printing `MISSING` → die with install hint; `shellcheck -S warning agr`

### Task 8: `install` hardening — atomic binary swap, atomic settings.json, version echo

**Files:**
- Modify: `agr`

- [ ] `scp` to `"$host:$REMOTE_BIN.tmp"`, then
      `ssh "$host" -- 'chmod +x "$HOME/.local/bin/agr.tmp" && mv -f "$HOME/.local/bin/agr.tmp" "$HOME/.local/bin/agr"'`
      (constant string; this removes the last SC2029 site together with Task 2's mkdir change)
- [ ] python merge (B5): wrap `json.load` in try/except → print
      `agr: cannot parse ~/.claude/settings.json: <err>` to stderr and `sys.exit(1)` before
      any write; compute the change first and **only if something changes** copy to
      `settings.json.bak-agr` (so a no-op re-run never overwrites the good backup), write
      `settings.json.tmp` in the same dir, `os.replace`; drop the redundant
      `"/agr status" in flat or` clause
- [ ] final log line: `agr: installed $VERSION on '$host'`
- [ ] write `$SCRATCH/checks/08-install.sh`: copy the heredoc body to `$SCRATCH/merge.py`
      (`sed -n '/<<.PY.$/,/^PY$/p' agr | sed '1d;$d'`) and run it with `HOME=<tmpdir>`:
      (a) no settings.json → created with the four hooks, no `.bak-agr`; (b) valid file with
      unrelated keys → keys preserved, hooks appended, `.bak-agr` equals the original;
      (c) run again → file unchanged, `.bak-agr` unchanged; (d) malformed JSON → one-line
      error, exit 1, file byte-identical; `shellcheck -S warning agr` (SC2029 count now 0)

### Task 9: `doctor` reports both versions and the bridge socket

**Files:**
- Modify: `agr`

- [ ] local block unchanged; add `agr local : $VERSION`
- [ ] remote block via one `ssh` with a constant script: `agr remote : <version|MISSING>`
      (`"$HOME/.local/bin/agr" --version`), `bridge socket : present|MISSING`
      (`[ -S ~/.cache/agterm/agterm.sock ]`), `legacy targets : <names|none>`
      (`ls ~/.cache/agterm/targets 2>/dev/null`)
- [ ] compare versions on the Mac side; on mismatch/MISSING append `← run: agr install $host`
- [ ] legacy hint: `legacy targets: api infra — agr open each to adopt, then rm -r ~/.cache/agterm/targets`
- [ ] no relay probe (see Overview — out of scope)
- [ ] write `$SCRATCH/checks/09-doctor.sh` (ssh shim printing a canned remote block):
      matching version → no hint; `0.3.0` / `MISSING` → hint present; `shellcheck -S warning agr`

### Task 10: `usage()`, header comment, README

**Files:**
- Modify: `agr`
- Modify: `README.md`

- [ ] `usage()`: add `ls`, `kill`, `open <host>` (no name → picker) to the Mac block;
      `sessions`, `reap` to the remote block; remove the `idle` state from the `status` line
      (nothing emits it)
- [ ] file header comment (`agr:8-9`): update the Mac / remote verb lists
- [ ] README "How it works" diagram + "Components" table: replace `targets/<name>` with the
      `@agr_target` session option; add `ls`/`kill`/`sessions`/`reap` rows;
      `agr up <host>` / `agr down <host>` with the argument
- [ ] README "State files" paragraph: remove `targets/`, add `~/.cache/agr/bridge-<host>.log`
      and `.lock`
- [ ] README "Usage": add the discovery loop — `agr ls`, `agr open <host>` picker, `agr kill`;
      and a short "Upgrading from 0.3" note (re-run `agr install`; `doctor` lists legacy
      sessions to re-open once; then `rm -r ~/.cache/agterm/targets` on the host)
- [ ] README "Reliability" table: add *Mac and remote agr versions differ* →
      `up`/`ls`/`kill` warn, `doctor` says which side to fix
- [ ] verify: `./agr --help` reads correctly; README code blocks match `usage()` verbatim

### Task 11: Verify acceptance criteria

- [ ] `for c in $SCRATCH/checks/*.sh; do bash "$c" || exit 1; done` green in one pass
- [ ] `shellcheck -S style -e SC2016 agr` clean (final gate); `bash -n agr`
- [ ] real host: `agr install <host>` prints `installed 0.4.0`; `agr doctor <host>` shows
      local == remote, socket present, legacy targets listed
- [ ] real host: `agr open <host> api` → in another row `agr ls <host>` shows `api … bound`;
      close the `api` row → `stale`
- [ ] real host: `agr open <host>` with no name → picker lists `api`; typing `new1` creates
      it and it appears in `ls`; cancel exits quietly
- [ ] real host: `tmux new -d -s phone` on the host → absent from `ls`; `agr kill <host> phone`
      refused with the not-managed message
- [ ] real host: `agr kill <host> new1` → gone from `agr ls` and from remote `tmux ls`
- [ ] real host, no autossh on `PATH`: `agr up` → `pgrep -f 'ssh.*agterm.sock'` shows the
      forward; force a disconnect (toggle Wi-Fi) → tmux view stays clean, log grows, tunnel
      returns; `agr down` → `pgrep` empty
- [ ] real host: agent in `api` produces sidebar colors (status hook path with `$TMUX_PANE`)

### Task 12: [Final] Update documentation

- [ ] README reflects everything shipped (re-read once end to end)
- [ ] add `docs/backlog/` items for the follow-ups listed in Overview (via the backlog skill)
- [ ] move this plan to `docs/plans/completed/`

## Technical Details

**Ownership marker.** tmux session user-option `@agr_target`. Values: an agterm session
id (uppercase UUID) when opened inside agterm, the literal `-` when opened elsewhere. Empty
/ unset = not agr-managed. Set by `attach` on every open (adopt semantics); dies with the
session; readable from any client via `#{@agr_target}` in formats or
`show-option -t "=name:" -qv @agr_target` — **the option commands need the trailing
colon**; `has-session`/`kill-session`/`list-panes` take plain `=name`.

**Hook lookup.** `cmd_status`: `[ -n "${TMUX_PANE:-}" ] || exit 0`, then
`tmux display-message -p -t "$TMUX_PANE" '#{@agr_target}'`. Without the explicit pane
target, a hook running outside tmux while a server is up resolves *some* session and
colors the wrong row.

**Wire format (`sessions`, `reap`).** Line 1: `agr<TAB><version>`. `sessions` then emits
per owned session `name<TAB>windows<TAB>attached<TAB>idle_secs<TAB>cmds<TAB>bound_sid`;
`cmds` is comma-joined unique `pane_current_command` minus `sh|bash|zsh|fish|dash`
(shells kept only when nothing else remains), `-` when empty — no middle field is ever
empty because `IFS=$'\t' read` collapses empty fields. `idle_secs` is
`now - session_activity` computed on the remote. Header-only = no owned sessions, exit 0.
`reap` emits `killed <name>` after the header.

**Remote invocation.** `remote_cmd` emits `"$HOME/.local/bin/agr" a1 a2 …` where the
`$HOME` is literal for the remote login shell and each arg is `printf %q`-quoted; every
arg that originates from user input or env also passes `valid_token`, so quoting is
belt-and-braces. Never pass a path containing `$HOME` as an *argument* — `%q` escapes
the `$`. mosh keeps the resolved `remote_home` path because `mosh-server` execs argv
without a shell. `remote_agr` runs a **non-login** remote shell: `sessions`/`reap` check
`have tmux` and die explicitly rather than report an empty box.

**Version handshake.** Data-returning remote commands (`sessions`, `reap`) start with the
header; `remote_data` on the Mac parses it (missing → install hint; mismatch → warning).
`agr open <host> <name>` is covered by the probe in `cmd_up`, which runs once per bridge
start with no extra round-trip. `doctor` reports both versions explicitly.

**Bridge.** Lock: `mkdir $CACHE_DIR/bridge-<host>.lock` (+ `pid` file inside) — portable,
no `flock`. Stale = holder pid dead. Loser waits ≤5 s for a live pidfile, then returns 0
with a warning. Fallback loop = subshell ignoring HUP, trapping TERM/INT to kill its own
`ssh`, logging to `bridge-<host>.log` (truncated per bridge start), backoff 3→60 s reset
after any connection >30 s. `stat -f %m` is the macOS mtime form if ever needed
(`cmd_up` is Mac-only).

**Shims (verification only, `$SCRATCH`, never committed).** Resolve the real binaries
**before** putting `$SCRATCH/bin` on `PATH`:
```sh
REAL_TMUX="$(command -v tmux)"; mkdir -p "$SCRATCH/bin"
printf '#!/bin/sh\nexec %s -L agr-test "$@"\n' "$REAL_TMUX" > "$SCRATCH/bin/tmux"
printf '#!/bin/sh\ncase "$*" in *-N*) exec sleep 300;; *) echo "agr 0.4.0";; esac\n' > "$SCRATCH/bin/ssh"
chmod +x "$SCRATCH/bin/"*
PATH="$SCRATCH/bin:$PATH" ./agr sessions
"$REAL_TMUX" -L agr-test kill-server   # cleanup
```

**Sourced checks.**
```sh
bash -c 'source ./agr; remote_agr() { printf "agr\t0.4.0\napi\t2\t1\t180\tclaude\tA69B0B26-X\n"; }; cmd_ls homelab'
```
(`source` inside `bash -c` so a `die` never takes the interactive shell with it.)

## Post-Completion

**Manual verification** — Task 11's real-host items require an SSH-reachable host with
tmux and, for the picker and `ROW` column, running inside agterm on the Mac.

**Upgrade note for the user's existing hosts** — after `agr install`, sessions created by
0.3.0 are invisible to `ls` until re-opened once (`doctor` lists them); then
`rm -r ~/.cache/agterm/targets` on the host.

**Backlog (not this plan)** — python→`nc -U`/`socat` relay to cut per-tool-call hook
latency; `agr uninstall <host>`; bats + GitHub Actions running `shellcheck` and the
`$SCRATCH/checks`; `valid_token` rejecting leading `-` and `.`/`..`; sanitise
`$host`/`$sid` before using them as cache filenames; `doctor` end-to-end relay probe if
the relay ever learns to report failure.
