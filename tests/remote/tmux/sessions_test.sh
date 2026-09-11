#!/bin/sh
set -eu

test_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

"$REAL_TMUX" -L agr-test new-session -d -s api
"$REAL_TMUX" -L agr-test set-option -t '=api:' @agr 1
"$REAL_TMUX" -L agr-test set-option -t '=api:' @agr_state "active@$(date +%s)"
"$REAL_TMUX" -L agr-test new-session -d -s api2
"$REAL_TMUX" -L agr-test new-session -d -s legacy
"$REAL_TMUX" -L agr-test set-option -t '=legacy:' @agr_target old-row

output=$(run_agr sessions)
header=$(printf '%s\n' "$output" | sed -n '1p')
body=$(printf '%s\n' "$output" | sed -n '2,$p')
assert_eq 'agr	@VERSION@' "$header" 'sessions header'
assert_contains 'api	' "$body" 'new marker is listed'
assert_contains 'legacy	' "$body" 'legacy marker is listed'
assert_not_contains 'api2	' "$body" 'unowned session is hidden'

body_rows=$(printf '%s\n' "$body" | sed '/^$/d')
if ! printf '%s\n' "$body_rows" | awk -F '\t' 'NF != 5 { exit 1 }'; then
	printf '%s\n' "sessions output does not contain exactly five fields: $body_rows" >&2
	exit 1
fi
