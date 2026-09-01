---
worth: later
where: agr:cmd_doctor
added: 2026-09-01
---
# `doctor` has no end-to-end relay probe

`agr doctor <host>` checks versions and bridge-socket presence but never round-trips a real
message through `agr relay` to confirm the sidebar actually updates. Deliberately left out of the
session-ownership-and-hardening plan (2026-09-01): the relay currently swallows every error, so a
probe cannot distinguish success from failure, and sending one produces a visible sidebar side
effect on every `doctor` run. Revisit if the relay ever gains real failure reporting.
