---
worth: yes
added: 2026-09-01
---
# no bats tests or CI

`agr` has no test framework and no CI. The session-ownership-and-hardening plan
(docs/plans/completed/20260901-session-ownership-and-hardening.md) built a `$SCRATCH/checks/*.sh`
suite (tmux/ssh shims, sourced-function checks) per task, deliberately scratchpad-only and never
committed — it was written expecting to be ported to bats and run in GitHub Actions alongside
`shellcheck`. The suite already exists in the plan's task history as a reference for what the
bats port should cover.
