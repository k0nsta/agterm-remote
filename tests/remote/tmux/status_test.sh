#!/bin/sh
set -eu

test_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

reset_capture
if ! run_agr status active >/dev/null 2>&1; then
	printf '%s\n' 'status outside tmux returned non-zero' >&2
	exit 1
fi
assert_eq '' "$(cat "$NC_CAPTURE")" 'status outside tmux is silent'

"$REAL_TMUX" -L agr-test new-session -d -s owned
"$REAL_TMUX" -L agr-test set-option -t '=owned:' @agr 1
pane=$("$REAL_TMUX" -L agr-test list-panes -s -t '=owned' -F '#{pane_id}')
reset_capture
TMUX_PANE=$pane
export TMUX_PANE
run_agr status active --blink --auto-reset
owned_state=$(tmux show-option -t '=owned:' -qv @agr_state)
case "$owned_state" in
	active@*) ;;
	*) printf 'wrong owned state: %s\n' "$owned_state" >&2; exit 1 ;;
esac
captured=$(cat "$NC_CAPTURE")
assert_contains '"session":"owned"' "$captured" 'status relays the session name'
assert_contains '"state":"active"' "$captured" 'status relays the state'
assert_contains '"args":["--blink","--auto-reset"]' "$captured" 'status relays hook arguments'

"$REAL_TMUX" -L agr-test new-session -d -s plain
plain_pane=$("$REAL_TMUX" -L agr-test list-panes -s -t '=plain' -F '#{pane_id}')
reset_capture
TMUX_PANE=$plain_pane
export TMUX_PANE
run_agr status blocked
assert_eq '' "$(cat "$NC_CAPTURE")" 'unowned status does not relay'

"$REAL_TMUX" -L agr-test set-option -t '=owned:' @agr_state idle@123
reset_capture
TMUX_PANE=%99
export TMUX_PANE
run_agr status blocked
assert_eq '' "$(cat "$NC_CAPTURE")" 'stale pane does not relay'
assert_eq idle@123 "$(tmux show-option -t '=owned:' -qv @agr_state)" 'stale pane does not alter a neighbour'

"$REAL_TMUX" -L agr-test kill-server
reset_capture
TMUX_PANE=%1
export TMUX_PANE
run_agr status active
assert_eq '' "$(cat "$NC_CAPTURE")" 'dead tmux server is a no-op'
