#!/bin/sh
set -eu

test_dir=$(CDPATH="" cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

export AGR_MUX=zmx
export AGR_ZMX_LABELS=1
export ZMX_FAKE_DIR="$REMOTE_TEST_TMP"/zmx
export ZMX_FAKE_CALLS="$REMOTE_TEST_TMP"/zmx.calls
mkdir -p "$ZMX_FAKE_DIR"
: > "$ZMX_FAKE_CALLS"

zmx attach adopted </dev/null
zmx set adopted old=1
export ZMX_FAKE_LABELS=1
help=$(zmx attach --help)
assert_contains --labels "$help" 'zmx help advertises labels when supported'
export ZMX_FAKE_LABELS=0
help=$(zmx attach --help)
assert_not_contains --labels "$help" 'zmx help omits labels when unsupported'

export ZMX_FAKE_LABELS=1
run_agr attach adopted </dev/null
assert_eq 1 "$(zmx get adopted agr)" 'attach adopts an existing session'
calls=$(cat "$ZMX_FAKE_CALLS")
assert_contains 'attach --labels agr=1 adopted' "$calls" 'attach uses zmx labels when enabled'

export AGR_ZMX_LABELS=0
run_agr attach race </dev/null
calls=$(cat "$ZMX_FAKE_CALLS")
assert_contains 'attach race' "$calls" 'attach uses the regular zmx form without labels'

i=0
label=
while [ "$i" -lt 20 ]; do
	if label=$(zmx get race agr 2>/dev/null); then
		[ "$label" = 1 ] && break
	fi
	sleep 0.1
	i=$((i + 1))
done
assert_eq 1 "$label" 'background retry labels the newly created session'
