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

zmx attach work </dev/null
zmx set work agr=1
export ZMX_SESSION=work
reset_capture
run_agr status active --blink --auto-reset
work_state=$(zmx get work agr_state)
case "$work_state" in
	active@*) ;;
	*) printf 'wrong zmx state: %s\n' "$work_state" >&2; exit 1 ;;
esac
captured=$(cat "$NC_CAPTURE")
assert_contains '"session":"work"' "$captured" 'status relays the zmx session name'
assert_contains '"state":"active"' "$captured" 'status relays the state'
assert_contains '"args":["--blink","--auto-reset"]' "$captured" 'status relays hook arguments'

zmx attach vanish </dev/null
zmx set vanish agr=1
export ZMX_FAKE_VANISH_ON_SET=vanish
export ZMX_SESSION=vanish
reset_capture
run_agr status blocked
assert_eq '' "$(cat "$NC_CAPTURE")" 'vanished zmx session is a no-op'
