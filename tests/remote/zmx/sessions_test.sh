#!/bin/sh
set -eu

test_dir=$(CDPATH="" cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

export AGR_MUX=zmx
export ZMX_FAKE_DIR="$REMOTE_TEST_TMP"/zmx
export ZMX_FAKE_CALLS="$REMOTE_TEST_TMP"/zmx.calls
mkdir -p "$ZMX_FAKE_DIR"
: > "$ZMX_FAKE_CALLS"
tab=$(printf '\t')

printf 'export ZMX_DIR=%s/pinned\n' "$ZMX_FAKE_DIR" > "$HOME/.config/agr/env"
zmx attach first </dev/null
zmx set first agr=1
zmx set first "agr_state=active@$(date +%s)"
zmx attach second </dev/null
zmx set second agr=1
zmx attach unowned </dev/null
zmx set unowned note=ignored
: > "$ZMX_FAKE_CALLS"
export ZMX_SESSION=second

output=$(run_agr sessions)
assert_eq "agr${tab}@VERSION@" "$(printf '%s\n' "$output" | sed -n '1p')" 'zmx sessions header'
assert_contains "first${tab}" "$output" 'owned session is listed'
assert_contains '→ name=second' "$(ZMX_DIR="$ZMX_FAKE_DIR/pinned" zmx list)" 'current zmx session has the arrow marker'
assert_contains "second${tab}" "$output" 'arrow-prefixed owned session is listed'
assert_not_contains "unowned${tab}" "$output" 'unowned session is hidden'

body=$(printf '%s\n' "$output" | sed '1d')
if ! printf '%s\n' "$body" | awk -F '\t' 'NF != 5 { exit 1 }'; then
	printf '%s\n' "zmx sessions output does not contain exactly five fields: $body" >&2
	exit 1
fi
assert_contains "first${tab}0${tab}" "$body" 'session without descendants still lists'
assert_contains 'active' "$body" 'state and idle fields are present'
calls=$(cat "$ZMX_FAKE_CALLS")
assert_not_contains 'ZMX_DIR=<unset>' "$calls" 'ZMX_DIR is exported before zmx calls'
assert_contains "ZMX_DIR=$ZMX_FAKE_DIR/pinned argv= list" "$calls" 'zmx sees the configured ZMX_DIR'
