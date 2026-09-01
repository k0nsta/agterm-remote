---
worth: later
where: agr:cmd_up
added: 2026-09-01
---
# `$host`/`$sid` used unsanitised in cache filenames

Bridge state (`bridge-<host>.log`, `bridge-<host>.lock`, cached remote `$HOME`) is filed under
`~/.cache/agr/` using `$host`/`$sid` directly in the filename, with no sanitisation against path
separators or other filename-hostile characters. Deferred out of the session-ownership-and-
hardening plan (2026-09-01) — `$host` is normally an `~/.ssh/config` alias the user typed
themselves and `$sid` already passes `valid_token` at the call sites that set `@agr_target`, so
the unknown is whether any path actually lets an untrusted value reach these filenames before
sanitising them.
