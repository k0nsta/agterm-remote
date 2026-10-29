---
worth: yes
where: internal/daemon/pageshim.go:328
added: 2026-10-23
---
# host page is dismissed when its row is in another window

The shim's page watcher asks `agtermctl tree`, which lists only the frontmost window. If you switch to
another agterm window for ~2 s (8 polls), the row counts as missing and an open host page is dismissed:
a blocking `overlay open --block` (a remote panel) returns exit 2 while the page is still on screen.
`driveShimPage`'s current-page check has the same blind spot. Fix: look the row up across windows
(`tree --window` per window) before counting it missing. Deferred from review finding f-0021.
