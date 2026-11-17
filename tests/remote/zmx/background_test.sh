#!/bin/sh
set -eu

test_dir=$(CDPATH="" cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

export AGR_MUX=zmx
export AGR_ZMX_LABELS=0
export ZMX_FAKE_DIR="$REMOTE_TEST_TMP"/zmx
export ZMX_FAKE_CALLS="$REMOTE_TEST_TMP"/zmx.calls
mkdir -p "$ZMX_FAKE_DIR"
: > "$ZMX_FAKE_CALLS"

proj=$REMOTE_TEST_TMP/proj
transcripts=$REMOTE_TEST_TMP/transcripts
state_dir=$HOME/.cache/agr/sessions
mkdir -p "$proj" "$transcripts" "$state_dir"
for s in work other; do
	zmx attach "$s" </dev/null
	zmx set "$s" agr=1
done
printf 'cwd=%s\nagent=claude\nclaude_session=aaaa-1111\n' "$proj" > "$state_dir/work"
printf '%s\n' '{"type":"continued-in","continuedInSessionId":"cccc-3333"}' > "$transcripts/aaaa-1111.jsonl"

# A background Claude inherits the daemon's ZMX_SESSION, here the wrong one.
export CLAUDE_CODE_SESSION_KIND=bg
export ZMX_SESSION=other
reset_capture
printf '{"session_id":"cccc-3333","transcript_path":"%s/cccc-3333.jsonl"}\n' "$transcripts" |
	CLAUDE_PROJECT_DIR=$proj run_agr status active
assert_contains '"session":"work"' "$(cat "$NC_CAPTURE")" 'a forked zmx conversation reaches the session it left'
assert_eq '' "$(zmx get other agr_state || :)" 'the inherited zmx session stays dark'
assert_eq cccc-3333 "$(sed -n 's/^claude_session=//p' "$state_dir/work")" 'the zmx session remembers the forked conversation'
