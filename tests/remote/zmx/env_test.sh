#!/bin/sh
set -eu

test_dir=$(CDPATH="" cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

export AGR_MUX=zmx
# The caller may itself run inside agterm; the fake reports what attach inherits.
unset AGTERM_SESSION_ID AGTERM_ENABLED
export AGR_ZMX_LABELS=1
export ZMX_FAKE_DIR="$REMOTE_TEST_TMP"/zmx
export ZMX_FAKE_CALLS="$REMOTE_TEST_TMP"/zmx.calls
export ZMX_FAKE_LABELS=1
mkdir -p "$ZMX_FAKE_DIR"
: > "$ZMX_FAKE_CALLS"

run_agr attach fresh --env AGTERM_SESSION_ID=row-1 --env AGTERM_ENABLED=1 --command 'make dev' </dev/null
calls=$(cat "$ZMX_FAKE_CALLS")
assert_contains 'attach-env fresh id=row-1 enabled=1' "$calls" 'a new session is created with the env'
# shellcheck disable=SC2016
assert_contains 'attach-cmd fresh sh -c make dev
exec "${SHELL:-/bin/sh}" -l' "$calls" 'a new session runs CMD, then a login shell'

: > "$ZMX_FAKE_CALLS"
run_agr attach fresh --env AGTERM_SESSION_ID=row-2 --command 'make dev' </dev/null
calls=$(cat "$ZMX_FAKE_CALLS")
assert_contains 'attach-env fresh id=<unset> enabled=<unset>' "$calls" 're-attach passes no env'
assert_not_contains 'attach-cmd' "$calls" 're-attach does not run CMD again'
assert_contains 'attach --labels agr=1 fresh' "$calls" 're-attach still attaches by name'

: > "$ZMX_FAKE_CALLS"
set +e
# shellcheck disable=SC2016
run_agr attach bad --env 'AGTERM_X=$(id)' </dev/null >/dev/null 2>&1
rc=$?
set -e
assert_eq 2 "$rc" 'an invalid --env is a usage error'
assert_eq '' "$(cat "$ZMX_FAKE_CALLS")" 'an invalid --env never reaches zmx'

# Without label support the plain form still takes env and CMD.
export AGR_ZMX_LABELS=0
run_agr attach plain --env AGTERM_ENABLED=1 --command 'true' </dev/null
calls=$(cat "$ZMX_FAKE_CALLS")
assert_contains 'attach-env plain id=<unset> enabled=1' "$calls" 'the unlabelled form carries the env'
assert_contains 'attach-cmd plain sh -c true' "$calls" 'the unlabelled form carries CMD'

# A CMD that already ends in a semicolon must still make a valid wrapper.
: > "$ZMX_FAKE_CALLS"
run_agr attach semi --command 'true;' </dev/null
assert_contains 'attach-syntax semi ok' "$(cat "$ZMX_FAKE_CALLS")" 'a CMD ending in ; still parses'
