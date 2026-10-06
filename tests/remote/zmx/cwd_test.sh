#!/bin/sh
set -eu

test_dir=$(CDPATH="" cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

export AGR_MUX=zmx
export AGR_ZMX_LABELS=1
export ZMX_FAKE_DIR="$REMOTE_TEST_TMP"/zmx
export ZMX_FAKE_CALLS="$REMOTE_TEST_TMP"/zmx.calls
export ZMX_FAKE_LABELS=1
mkdir -p "$ZMX_FAKE_DIR"
: > "$ZMX_FAKE_CALLS"
tab=$(printf '\t')
work=$REMOTE_TEST_TMP/work
mkdir -p "$work"
work_real=$(CDPATH="" cd -- "$work" && pwd)

run_agr attach child --cwd "$work" --parent api </dev/null
assert_eq 1 "$(zmx get child agr)" 'attach with options labels the session owned'
assert_eq api "$(zmx get child agr_parent)" 'attach labels the parent session'
calls=$(cat "$ZMX_FAKE_CALLS")
assert_contains 'attach --labels agr=1 --labels agr_parent=api child' "$calls" 'labels form carries the parent'
assert_contains "attach-cwd child $work_real" "$calls" 'zmx attach runs from --cwd'

export AGR_ZMX_LABELS=0
run_agr attach late --parent api </dev/null
i=0
label=
while [ "$i" -lt 20 ]; do
	if label=$(zmx get late agr_parent 2>/dev/null); then
		[ "$label" = api ] && break
	fi
	sleep 0.1
	i=$((i + 1))
done
assert_eq api "$label" 'background retry labels the parent too'

# /proc exists only on Linux; elsewhere cwd answers with an empty line so the
# Mac falls back to the remote home.
output=$(run_agr cwd child)
assert_eq "agr${tab}@VERSION@" "$(printf '%s\n' "$output" | sed -n '1p')" 'cwd prints the handshake header'
if [ -d /proc/self ]; then
	[ -n "$(printf '%s\n' "$output" | sed -n '2p')" ] || { printf '%s\n' 'cwd empty on a /proc host' >&2; exit 1; }
else
	assert_eq '' "$(printf '%s\n' "$output" | sed -n '2p')" 'cwd is empty without /proc'
fi
assert_eq "agr${tab}@VERSION@" "$(run_agr cwd nosuch)" 'cwd of a missing session prints only the header'
