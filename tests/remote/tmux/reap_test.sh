#!/bin/sh
set -eu

test_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

"$REAL_TMUX" -L agr-test new-session -d -s api
"$REAL_TMUX" -L agr-test set-option -t '=api:' @agr 1
"$REAL_TMUX" -L agr-test new-session -d -s api2

set +e
unowned_output=$(run_agr reap api2 2>&1)
unowned_rc=$?
set -e
if [ "$unowned_rc" -eq 0 ]; then
	printf '%s\n' 'reap unexpectedly accepted an unowned session' >&2
	exit 1
fi
assert_contains "agr: 'api2' exists but is not agr-managed" "$unowned_output" 'reap refusal'

owned_output=$(run_agr reap api)
assert_eq 'agr	@VERSION@' "$(printf '%s\n' "$owned_output" | sed -n '1p')" 'reap header'
assert_contains 'killed api' "$owned_output" 'reap result'
if tmux has-session -t '=api' 2>/dev/null; then
	printf '%s\n' 'reap did not kill the exact api session' >&2
	exit 1
fi
if ! tmux has-session -t '=api2' 2>/dev/null; then
	printf '%s\n' 'reap touched neighbouring api2 session' >&2
	exit 1
fi
