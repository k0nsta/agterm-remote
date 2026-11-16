#!/bin/sh
set -eu

test_dir=$(CDPATH="" cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

export AGR_MUX=zmx
unset AGTERM_SESSION_ID AGTERM_ENABLED
export AGR_ZMX_LABELS=1
export ZMX_FAKE_DIR="$REMOTE_TEST_TMP"/zmx
export ZMX_FAKE_CALLS="$REMOTE_TEST_TMP"/zmx.calls
export ZMX_FAKE_LABELS=1
mkdir -p "$ZMX_FAKE_DIR"
: > "$ZMX_FAKE_CALLS"

# What the agent hooks left behind before a reboot killed the session.
proj=$REMOTE_TEST_TMP/proj
mkdir -p "$proj" "$HOME/.cache/agr/sessions"
printf 'cwd=%s\nagent=claude\nclaude_session=0a1b-2c3d\n' "$proj" > "$HOME/.cache/agr/sessions/work"

run_agr attach work </dev/null 2>"$REMOTE_TEST_TMP/err"
assert_eq "agr: restoring work in $proj, resuming Claude" "$(cat "$REMOTE_TEST_TMP/err")" 'a recreated zmx session says what it restores'
assert_contains 'claude --resume 0a1b-2c3d' "$(cat "$ZMX_FAKE_CALLS")" 'a recreated zmx session resumes the conversation'

: > "$ZMX_FAKE_CALLS"
run_agr attach work </dev/null 2>"$REMOTE_TEST_TMP/err"
assert_eq '' "$(cat "$REMOTE_TEST_TMP/err")" 'a live zmx session is attached as it is'
assert_not_contains 'attach-cmd' "$(cat "$ZMX_FAKE_CALLS")" 'a live zmx session runs nothing'

run_agr attach other --fresh </dev/null 2>"$REMOTE_TEST_TMP/err"
assert_eq '' "$(cat "$REMOTE_TEST_TMP/err")" '--fresh restores nothing'
