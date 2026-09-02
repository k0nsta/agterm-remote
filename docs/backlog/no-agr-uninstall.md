---
worth: later
added: 2026-09-01
---
# no `agr uninstall <host>`

`agr install <host>` copies the binary and wires Claude Code hooks into the remote
`~/.claude/settings.json`; there's no inverse to remove the binary and unwire the hooks. Deferred
out of the session-ownership-and-hardening plan (2026-09-01) — the unknown is whether manual
cleanup (rm the binary, hand-edit settings.json) is good enough until someone actually asks for
a clean removal path.
