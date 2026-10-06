#!/bin/sh
set -eu

test_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

owned() {
	"$REAL_TMUX" -L agr-test new-session -d -s "$1"
	"$REAL_TMUX" -L agr-test set-option -t "=$1:" @agr 1
	[ -z "${2:-}" ] || "$REAL_TMUX" -L agr-test set-option -t "=$1:" @agr_parent "$2"
}

owned a1
owned a1-2 a1
owned a10 a1x
owned b1
"$REAL_TMUX" -L agr-test new-session -d -s squatter
"$REAL_TMUX" -L agr-test set-option -t '=squatter:' @agr_parent a1

output=$(run_agr reap a1)
assert_contains 'killed a1' "$output" 'parent reaped'
assert_contains 'killed a1-2' "$output" 'child reaped with its parent'
for gone in a1 a1-2; do
	if tmux has-session -t "=$gone" 2>/dev/null; then
		printf 'reap left %s\n' "$gone" >&2
		exit 1
	fi
done
for kept in a10 b1 squatter; do
	tmux has-session -t "=$kept" 2>/dev/null || { printf 'reap killed %s\n' "$kept" >&2; exit 1; }
done

owned orphan-2 orphan
orphan_output=$(run_agr reap orphan)
assert_contains 'killed orphan-2' "$orphan_output" 'children of a parent that is already gone are reaped'

set +e
missing=$(run_agr reap nosuch 2>&1)
missing_rc=$?
set -e
assert_eq 3 "$missing_rc" 'a missing session exits 3'
assert_contains "agr: no session 'nosuch'" "$missing" 'missing-session message'

set +e
run_agr reap squatter >/dev/null 2>&1
unowned_rc=$?
set -e
assert_eq 1 "$unowned_rc" 'an unowned session is still a refusal, not a miss'

# A query that fails for any other reason (here: a client newer than the
# running server) is a failure, never the missing-session exit that lets
# `agr end` close a row whose agent is still running.
broken=$REMOTE_TEST_TMP/broken-bin
mkdir -p "$broken"
printf '#!/bin/sh\necho "protocol version mismatch (client 8, server 9)" >&2\nexit 1\n' > "$broken/tmux"
chmod +x "$broken/tmux"
set +e
broken_out=$(PATH="$broken:$PATH" run_agr reap b1 2>&1)
broken_rc=$?
set -e
assert_eq 1 "$broken_rc" 'a failed tmux query exits 1, not 3'
assert_contains 'tmux query failed' "$broken_out" 'the query failure is reported'
tmux has-session -t '=b1' 2>/dev/null || { printf '%s\n' 'b1 vanished' >&2; exit 1; }
