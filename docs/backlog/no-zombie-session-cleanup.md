---
worth: later
added: 2026-09-01
---
# no zombie-session heuristics, prune, idle TTL, or `kill --force`

`agr ls`/`agr kill` show and remove agr-owned tmux sessions but nothing automatically flags or
reaps sessions that have gone idle for a long time or otherwise look abandoned (no idle-TTL
policy, no `--force` for a session `agr reap` refuses). Deferred out of the session-ownership-
and-hardening plan (2026-09-01) — the unknown is what a reasonable idle/zombie heuristic even
looks like without usage data on how sessions actually accumulate over weeks of real use.
