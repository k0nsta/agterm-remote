---
worth: later
where: agr:agr relay
added: 2026-09-01
---
# relay transport is python, not nc/socat

`agr relay` shells out to a python process per invocation to speak agterm's JSON control protocol
to the forwarded socket. A `nc -U`/`socat` transport would cut per-tool-call hook latency by
skipping python startup. Deferred out of the session-ownership-and-hardening plan (2026-09-01)
because latency wasn't measured — the unknown is whether it's actually noticeable in practice
before redesigning the transport.
