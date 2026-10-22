#!/bin/sh
set -eu

test_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

start_ctl_server
page=$REMOTE_TEST_TMP/page.html
printf '<p>hi</p>' > "$page"

"$REAL_TMUX" -L agr-test new-session -d -s owned
"$REAL_TMUX" -L agr-test set-option -t '=owned:' @agr 1
owned_pane=$("$REAL_TMUX" -L agr-test list-panes -s -t '=owned' -F '#{pane_id}')
"$REAL_TMUX" -L agr-test new-session -d -s plain
plain_pane=$("$REAL_TMUX" -L agr-test list-panes -s -t '=plain' -F '#{pane_id}')

# Outside a session, or in one agr does not own: refused before any connection.
unset TMUX_PANE
ctl_call version
assert_eq 1 "$CTL_RC" 'outside tmux is refused'
assert_contains 'agtermctl (via agr): not inside a row opened with agr open' "$(cat "$CTL_DIR/err")" 'outside a session message'
TMUX_PANE=$plain_pane
export TMUX_PANE
ctl_call version
assert_eq 1 "$CTL_RC" 'an unowned session is refused'
assert_contains 'not inside a row opened with agr open' "$(cat "$CTL_DIR/err")" 'unowned session message'
[ ! -e "$CTL_DIR/conns" ] || { printf '%s\n' 'a refused call reached the bridge' >&2; exit 1; }

TMUX_PANE=$owned_pane
export TMUX_PANE

# Header content, and the body waiting for the ack.
printf 'o 2\nokx 0\n' > "$CTL_DIR/reply"
: > "$CTL_DIR/mode"
mkdir -p "$REMOTE_TEST_TMP/proj"
cd "$REMOTE_TEST_TMP/proj"
AGTERM_SESSION_ID=row-1
AGTERM_PANE_ID=pane-1
export AGTERM_SESSION_ID AGTERM_PANE_ID
ctl_call session overlay open --html "$page" --js --block
unset AGTERM_SESSION_ID AGTERM_PANE_ID
assert_eq 0 "$CTL_RC" 'exit 0 passes through'
assert_eq ok "$(cat "$CTL_DIR/out")" 'stdout frame'
header=$(cat "$CTL_DIR/header")
assert_contains '"cmd":"ctl"' "$header" 'header cmd'
assert_contains '"v":1' "$header" 'header version'
assert_contains '"session":"owned"' "$header" 'header session'
assert_contains '"self":{"row":"row-1","pane_id":"pane-1"}' "$header" 'header self'
assert_contains "\"cwd\":\"$PWD\"" "$header" 'header cwd'
assert_contains '"argv":["session","overlay","open","--html","@0","--js","--block"]' "$header" 'file option becomes @0'
assert_contains '"files":[9]' "$header" 'file size'
assert_contains '"stdin":0' "$header" 'no stdin'
assert_not_contains "$page" "$header" 'no host path is sent'
assert_eq 0 "$(cat "$CTL_DIR/early")" 'nothing but the header before the ack'
assert_eq '<p>hi</p>' "$(cat "$CTL_DIR/body")" 'file bytes follow the ack'

# stdin travels only with --stdin.
printf 'x 0\n' > "$CTL_DIR/reply"
printf 'hello' | ctl_call session type --stdin
assert_contains '"stdin":5' "$(cat "$CTL_DIR/header")" '--stdin forwards stdin'
assert_eq hello "$(cat "$CTL_DIR/body")" 'stdin bytes follow the ack'
printf 'hello' | ctl_call session text
assert_contains '"stdin":0' "$(cat "$CTL_DIR/header")" 'stdin is not read without --stdin'
assert_eq '' "$(cat "$CTL_DIR/body")" 'no stdin body'

# Stdout and stderr frames, and the exit codes.
for code in 0 1 2; do
	printf 'o 6\nhello\ne 5\noops\nx %s\n' "$code" > "$CTL_DIR/reply"
	ctl_call tree
	assert_eq "$code" "$CTL_RC" "exit code $code passes through"
	assert_eq "$(printf 'hello\n')" "$(cat "$CTL_DIR/out")" 'stdout is demuxed'
	assert_eq "$(printf 'oops\n')" "$(cat "$CTL_DIR/err")" 'stderr is demuxed'
done

# A refusal arrives before any ack: it is shown, and no body is sent.
printf 'e 40\nagtermctl (via agr): not allowed here\nx 1\n' > "$CTL_DIR/reply"
printf 'refuse' > "$CTL_DIR/mode"
ctl_call session overlay open --html "$page"
assert_eq 1 "$CTL_RC" 'a refusal exits 1'
assert_contains 'agtermctl (via agr): not allowed here' "$(cat "$CTL_DIR/err")" 'refusal text'
assert_eq '' "$(cat "$CTL_DIR/body")" 'a refused call sends no body'

# Frames that arrive in the same send as the ack are not lost.
printf 'o 2\nokx 0\n' > "$CTL_DIR/reply"
printf 'combined' > "$CTL_DIR/mode"
ctl_call tree
assert_eq 0 "$CTL_RC" 'ack and reply in one send exits 0'
assert_eq ok "$(cat "$CTL_DIR/out")" 'frames after the ack are kept'

# The ack, then the connection ends without an exit frame.
printf 'ackeof' > "$CTL_DIR/mode"
ctl_call tree
assert_eq 1 "$CTL_RC" 'ack then EOF exits 1'
assert_contains 'agtermctl (via agr): the bridge to the Mac dropped' "$(cat "$CTL_DIR/err")" 'ack then EOF message'

# No ack at all: five seconds, then the daemon-too-old message, and no body.
printf 'noack' > "$CTL_DIR/mode"
ctl_call session overlay open --html "$page"
assert_eq 1 "$CTL_RC" 'no ack exits 1'
assert_contains "agtermctl (via agr): the Mac's agr daemon is too old or not running" "$(cat "$CTL_DIR/err")" 'no ack message'
assert_eq 0 "$(cat "$CTL_DIR/early")" 'no body without an ack'

# No socket.
stop_ctl_server
AGR_SOCK=$REMOTE_TEST_TMP/missing.sock
export AGR_SOCK
CTL_DIR=$REMOTE_TEST_TMP
ctl_call tree
assert_eq 1 "$CTL_RC" 'no socket exits 1'
assert_contains 'agtermctl (via agr): the bridge to the Mac is down (run agr up <host> on the Mac)' "$(cat "$CTL_DIR/err")" 'no socket message'

# python3 is required.
empty=$REMOTE_TEST_TMP/empty
mkdir -p "$empty"
set +e
PATH=$empty "$REMOTE_SCRIPT" ctl tree >/dev/null 2>"$CTL_DIR/err"
rc=$?
set -e
assert_eq 1 "$rc" 'no python3 exits 1'
assert_contains 'python3 is required' "$(cat "$CTL_DIR/err")" 'no python3 message'
