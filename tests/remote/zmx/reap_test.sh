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

zmx attach plain </dev/null
zmx set plain note=x
zmx attach api </dev/null
zmx set api agr=1

set +e
unowned_output=$(run_agr reap plain 2>&1)
unowned_rc=$?
set -e
if [ "$unowned_rc" -eq 0 ]; then
	printf '%s\n' 'reap unexpectedly accepted an unlabeled session' >&2
	exit 1
fi
assert_contains "agr${tab}@VERSION@" "$unowned_output" 'unowned reap header'
assert_contains "agr: 'plain' exists but is not agr-managed" "$unowned_output" 'reap refusal'

owned_output=$(run_agr reap api)
assert_eq "agr${tab}@VERSION@" "$(printf '%s\n' "$owned_output" | sed -n '1p')" 'owned reap header'
assert_contains 'killed api' "$owned_output" 'reap result'
if [ -f "$ZMX_FAKE_DIR/api" ]; then
	printf '%s\n' 'reap did not kill the exact session' >&2
	exit 1
fi
