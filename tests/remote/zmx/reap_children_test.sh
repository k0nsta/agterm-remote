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

owned() {
	zmx attach "$1" </dev/null
	zmx set "$1" agr=1
	[ -z "${2:-}" ] || zmx set "$1" "agr_parent=$2"
}

owned a1
owned a1-2 a1
owned b1 b
zmx attach squatter </dev/null
zmx set squatter agr_parent=a1

output=$(run_agr reap a1)
assert_contains 'killed a1' "$output" 'parent reaped'
assert_contains 'killed a1-2' "$output" 'child reaped with its parent'
[ ! -f "$ZMX_FAKE_DIR/a1-2" ] || { printf '%s\n' 'child survived' >&2; exit 1; }
[ -f "$ZMX_FAKE_DIR/b1" ] || { printf '%s\n' 'unrelated session killed' >&2; exit 1; }
[ -f "$ZMX_FAKE_DIR/squatter" ] || { printf '%s\n' 'unowned child-labelled session killed' >&2; exit 1; }

owned orphan-2 orphan
assert_contains 'killed orphan-2' "$(run_agr reap orphan)" 'children of a gone parent are reaped'

set +e
missing=$(run_agr reap nosuch 2>&1)
missing_rc=$?
set -e
assert_eq 3 "$missing_rc" 'a missing session exits 3, not a refusal'
assert_contains "agr: no session 'nosuch'" "$missing" 'missing-session message'

broken=$REMOTE_TEST_TMP/broken-bin
mkdir -p "$broken"
printf '#!/bin/sh\necho "zmx: cannot open socket dir" >&2\nexit 1\n' > "$broken/zmx"
chmod +x "$broken/zmx"
set +e
broken_out=$(PATH="$broken:$PATH" run_agr reap b1 2>&1)
broken_rc=$?
set -e
assert_eq 1 "$broken_rc" 'a failed zmx listing exits 1, not 3'
assert_contains 'zmx list failed' "$broken_out" 'the listing failure is reported'
