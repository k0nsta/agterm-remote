#!/bin/sh
set -eu

test_dir=$(CDPATH="" cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

# A Claude Code background session (/background) runs the conversation it
# forked outside any pane, with the daemon's environment: its hooks find their
# session through the forked conversation's "continued-in" record.
proj=$REMOTE_TEST_TMP/proj
transcripts=$REMOTE_TEST_TMP/transcripts
state_dir=$HOME/.cache/agr/sessions
mkdir -p "$proj" "$transcripts" "$state_dir"

for s in work other; do
	"$REAL_TMUX" -L agr-test new-session -d -s "$s"
	"$REAL_TMUX" -L agr-test set-option -t "=$s:" @agr 1
done
other_pane=$("$REAL_TMUX" -L agr-test list-panes -s -t '=other' -F '#{pane_id}')
work_pane=$("$REAL_TMUX" -L agr-test list-panes -s -t '=work' -F '#{pane_id}')
printf 'cwd=%s\nagent=claude\nclaude_session=aaaa-1111\n' "$proj" > "$state_dir/work"
printf 'cwd=%s\nagent=claude\nclaude_session=bbbb-2222\n' "$proj" > "$state_dir/other"
printf '%s\n' '{"type":"user"}' > "$transcripts/aaaa-1111.jsonl"
printf '%s\n' '{"type":"user"}' > "$transcripts/bbbb-2222.jsonl"

# fork FROM TO writes the record Claude leaves when /background forks FROM.
fork() {
	printf '{"type":"continued-in","continuedInSessionId":"%s","timestamp":"2026-01-01T00:00:00Z"}\n' "$2" >> "$transcripts/$1.jsonl"
}

# hook ID ARGS... runs agr as a Claude hook of conversation ID.
hook() {
	id=$1
	shift
	printf '{"session_id":"%s","transcript_path":"%s/%s.jsonl","hook_event_name":"Stop"}\n' "$id" "$transcripts" "$id" |
		CLAUDE_PROJECT_DIR=$proj run_agr "$@"
}

state_of() {
	"$REAL_TMUX" -L agr-test show-option -t "=$1:" -qv @agr_state
}

memory() {
	sed -n "s/^$2=//p" "$state_dir/$1"
}

# The daemon was started from another row: its pane variable names "other".
export CLAUDE_CODE_SESSION_KIND=bg
export TMUX_PANE="$other_pane"

reset_capture
hook cccc-3333 status active
assert_eq '' "$(cat "$NC_CAPTURE")" 'an unlinked background conversation relays nothing'
assert_eq '' "$(state_of other)" 'a background hook ignores the inherited pane'

fork aaaa-1111 ffff-6666
fork bbbb-2222 ffff-6666
reset_capture
hook ffff-6666 status active
assert_eq '' "$(cat "$NC_CAPTURE")" 'a conversation forked from two sessions is ambiguous'

fork aaaa-1111 cccc-3333
reset_capture
hook cccc-3333 status active --auto-reset
assert_contains '"session":"work"' "$(cat "$NC_CAPTURE")" 'a forked conversation reaches the session it left'
case "$(state_of work)" in
	active@*) ;;
	*) printf 'wrong work state: %s\n' "$(state_of work)" >&2; exit 1 ;;
esac
assert_eq '' "$(state_of other)" 'the session sharing the directory stays dark'
assert_eq cccc-3333 "$(memory work claude_session)" 'the session remembers the forked conversation'
assert_eq bbbb-2222 "$(memory other claude_session)" 'the other session keeps its conversation'

reset_capture
hook cccc-3333 status completed
assert_contains '"session":"work"' "$(cat "$NC_CAPTURE")" 'later hooks find the adopted conversation'

# The Claude left in the pane still runs the old conversation.
unset CLAUDE_CODE_SESSION_KIND
TMUX_PANE=$work_pane
hook aaaa-1111 status active
assert_eq cccc-3333 "$(memory work claude_session)" 'the pane Claude does not take the session back'
hook aaaa-1111 agent end
assert_eq cccc-3333 "$(memory work claude_session)" 'the pane Claude quitting keeps the forked conversation'
assert_eq claude "$(memory work agent)" 'the pane Claude quitting keeps the agent'

export CLAUDE_CODE_SESSION_KIND=bg
TMUX_PANE=$other_pane
hook cccc-3333 agent end
assert_eq '' "$(memory work claude_session)" 'the background Claude quitting forgets its conversation'
assert_eq bbbb-2222 "$(memory other claude_session)" 'and only its own session'
