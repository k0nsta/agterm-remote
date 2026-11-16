#!/bin/sh
set -eu

test_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

state=$HOME/.cache/agr/sessions/work
proj=$REMOTE_TEST_TMP/proj
mkdir -p "$proj"

pane_of() {
	"$REAL_TMUX" -L agr-test list-panes -s -t "=$1" -F '#{pane_id}' | head -n 1
}

# reboot kills every session, as a host reboot does.
reboot() {
	"$REAL_TMUX" -L agr-test kill-server >/dev/null 2>&1 || :
}

attach() {
	run_agr attach work "$@" </dev/null >/dev/null 2>"$REMOTE_TEST_TMP/err" || :
}

# restored prints attach's restore note (tmux's own "not a terminal" aside).
restored() {
	grep '^agr: restoring' "$REMOTE_TEST_TMP/err" || :
}

attach
TMUX_PANE=$(pane_of work)
export TMUX_PANE
printf '%s\n' '{"session_id":"0a1b-2c3d","hook_event_name":"Stop"}' |
	CLAUDE_PROJECT_DIR=$proj run_agr status completed --auto-reset
assert_eq "$proj" "$(sed -n 's/^cwd=//p' "$state")" 'a Claude hook records the project directory'
assert_eq claude "$(sed -n 's/^agent=//p' "$state")" 'a Claude hook records the agent'
assert_eq 0a1b-2c3d "$(sed -n 's/^claude_session=//p' "$state")" 'a Claude hook records the conversation id'

reboot
attach
assert_eq "agr: restoring work in $proj, resuming Claude" "$(restored)" 'a recreated session says what it restores'
assert_contains 'claude --resume 0a1b-2c3d' "$("$REAL_TMUX" -L agr-test display-message -p -t '=work:' '#{pane_start_command}')" 'the recreated session resumes the conversation'
assert_eq "$proj" "$("$REAL_TMUX" -L agr-test display-message -p -t '=work:' '#{pane_start_path}')" 'the recreated session starts in the project directory'

TMUX_PANE=$(pane_of work)
printf '%s\n' '{"session_id":"9f9f-0000","hook_event_name":"SessionStart"}' |
	CLAUDE_PROJECT_DIR=$proj run_agr agent start
assert_eq 9f9f-0000 "$(sed -n 's/^claude_session=//p' "$state")" 'SessionStart records the new conversation id'
run_agr agent end </dev/null
assert_eq '' "$(sed -n 's/^agent=//p' "$state")" 'SessionEnd forgets the agent'
assert_eq "$proj" "$(sed -n 's/^cwd=//p' "$state")" 'SessionEnd keeps the directory'
reboot
attach
assert_eq "agr: restoring work in $proj" "$(restored)" 'without an agent only the directory comes back'

reboot
attach --fresh
assert_eq '' "$(restored)" '--fresh restores nothing'
assert_eq '' "$("$REAL_TMUX" -L agr-test display-message -p -t '=work:' '#{pane_start_command}')" '--fresh runs no command'

reboot
other=$REMOTE_TEST_TMP/other
mkdir -p "$other"
attach --cwd "$other"
assert_eq "$other" "$("$REAL_TMUX" -L agr-test display-message -p -t '=work:' '#{pane_start_path}')" '--cwd wins over the remembered directory'

reboot
rm -rf "$proj"
attach
assert_eq '' "$(restored)" 'a deleted directory is not restored'

run_agr reap work >/dev/null
if [ -e "$state" ]; then
	printf '%s\n' 'reap left the session memory behind' >&2
	exit 1
fi
