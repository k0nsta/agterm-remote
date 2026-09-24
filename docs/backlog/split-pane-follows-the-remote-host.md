---
worth: yes
added: 2026-09-14
---
# a keymap chord that splits onto the same remote host as the row

A split pane (⌘D) starts a local shell even when the row's other pane is an agr session, so running a
second agent (codex next to claude) means typing `agr open <host>` again by hand.

Decided 2026-09-14/17: a new `agr split <row>` verb bound as a keymap custom action, so ⌘D stays a
local split and the chord is the opt-in:

    command "Split onto remote host" ctrl+a>s $HOME/go/bin/agr split "$AGT_SESSION_ID"

(an absolute path: custom commands run with agterm's GUI PATH). It looks up the row's left-pane binding,
runs `agtermctl session split on`, waits for the right pane's prompt, and types
`agr open <host> <left-name>-2` there. The new session starts in the left pane's current remote
directory (tmux `new-session -c`, zmx by changing directory before attach), which is what "same
session" was meant to give. An existing `-2` session is simply re-attached; an unbound row gets a
plain local split.

Rejected: a second tmux window of the same session via session groups. zmx has no windows, and a
window shared by two grouped sessions reports whichever session last showed it (checked on tmux 3.5a),
so status would land on the wrong pane. Blocked on per-pane bindings (PR #8), or the typed open
clobbers the left pane's binding.
