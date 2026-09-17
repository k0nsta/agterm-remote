#!/bin/sh
set -eu

test_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

script_version=$(run_agr --version)
assert_eq 'agr @VERSION@' "$script_version" 'source script keeps its build placeholder'

output=$(run_agr sessions)
assert_eq 'agr	@VERSION@' "$(printf '%s\n' "$output" | sed -n '1p')" 'sessions prints one handshake header'

if run_agr attach api </dev/null >/dev/null 2>&1; then
	:
fi
agr_mark=$(tmux show-option -t '=api:' -qv @agr)
assert_eq 1 "$agr_mark" 'attach marks the session as agr-owned'

ms='Ms=\E]52;%?%p1%l%t%p1%s%ec%;;%p2%s\007'
assert_contains "$ms" "$(tmux show-options -sv terminal-overrides)" 'attach fills an empty OSC 52 selection field with c, the only form mosh forwards'
if run_agr attach api </dev/null >/dev/null 2>&1; then
	:
fi
assert_eq 1 "$(tmux show-options -sv terminal-overrides | grep -c 'Ms=')" 'a second attach does not append the override again'
