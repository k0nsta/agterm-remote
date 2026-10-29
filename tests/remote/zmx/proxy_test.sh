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

start_ctl_server

zmx attach work </dev/null
zmx set work agr=1
zmx attach plain </dev/null

# Outside zmx, or in a session agr does not own: refused before any connection.
unset ZMX_SESSION
ctl_call version
assert_eq 1 "$CTL_RC" 'outside zmx is refused'
assert_contains 'agtermctl (via agr): not inside a row opened with agr open' "$(cat "$CTL_DIR/err")" 'outside a session message'
ZMX_SESSION=plain
export ZMX_SESSION
ctl_call version
assert_eq 1 "$CTL_RC" 'an unowned zmx session is refused'
assert_contains 'not inside a row opened with agr open' "$(cat "$CTL_DIR/err")" 'unowned session message'
ZMX_SESSION=gone
ctl_call version
assert_eq 1 "$CTL_RC" 'an unknown zmx session is refused'
[ ! -e "$CTL_DIR/conns" ] || { printf '%s\n' 'a refused call reached the bridge' >&2; exit 1; }

# The session name in the header comes from the zmx lookup.
ZMX_SESSION=work
printf 'o 3\nzmxe 4\nbad\nx 2\n' > "$CTL_DIR/reply"
: > "$CTL_DIR/mode"
ctl_call tree --json
assert_eq 2 "$CTL_RC" 'exit code passes through'
assert_eq zmx "$(cat "$CTL_DIR/out")" 'stdout frame'
assert_eq "$(printf 'bad\n')" "$(cat "$CTL_DIR/err")" 'stderr frame'
header=$(cat "$CTL_DIR/header")
assert_contains '"session":"work"' "$header" 'header session'
assert_contains '"argv":["tree","--json"]' "$header" 'header argv'
