---
worth: later
where: agr:valid_token
added: 2026-09-01
---
# `valid_token` allows leading `-`, `.`, `..`

`valid_token` accepts the charset `[A-Za-z0-9_.-]` without rejecting names that are just `-`,
start with `-` (ambiguous with a flag once interpolated into a command line), or are `.`/`..`
(ambiguous with path components when a token is ever used to build a filename). Deferred out of
the session-ownership-and-hardening plan (2026-09-01) — no current call site builds a path or
flag directly from an unquoted token, so the unknown is whether it's worth tightening before a
call site actually needs it.
