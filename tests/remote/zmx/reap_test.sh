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

# A session labelled with a non-1 agr value is hidden from `agr ls`, because
# zmx_sessions tests for equality with 1. reap must apply the same test: a
# non-empty check let `agr kill` destroy a session the tool never claimed.
zmx attach decoy </dev/null
zmx set decoy agr=0

set +e
decoy_output=$(run_agr reap decoy 2>&1)
decoy_rc=$?
set -e
if [ "$decoy_rc" -eq 0 ]; then
	printf '%s\n' 'reap accepted a session labelled agr=0' >&2
	exit 1
fi
assert_contains "agr: 'decoy' exists but is not agr-managed" "$decoy_output" 'reap refuses agr=0'
if [ ! -f "$ZMX_FAKE_DIR/decoy" ]; then
	printf '%s\n' 'reap killed a session labelled agr=0' >&2
	exit 1
fi

# The same equality applies to the status hook: a non-1 label must not relay.
reset_capture
ZMX_SESSION=decoy run_agr status active
assert_eq '' "$(cat "$NC_CAPTURE")" 'status relayed for a non-1 agr label'
