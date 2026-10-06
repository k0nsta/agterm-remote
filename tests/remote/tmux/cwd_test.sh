#!/bin/sh
set -eu

test_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

tab=$(printf '\t')
work="$REMOTE_TEST_TMP/work dir"
mkdir -p "$work"
# tmux reports the resolved path, which on macOS is under /private.
work_real=$(CDPATH= cd -- "$work" && pwd -P)

if run_agr attach child --cwd "$work" --parent api </dev/null >/dev/null 2>&1; then
	:
fi
assert_eq 1 "$(tmux show-option -t '=child:' -qv @agr)" 'attach with options still marks the session owned'
assert_eq api "$(tmux show-option -t '=child:' -qv @agr_parent)" 'attach records the parent session'

output=$(run_agr cwd child)
assert_eq "agr${tab}@VERSION@" "$(printf '%s\n' "$output" | sed -n '1p')" 'cwd prints the handshake header'
assert_eq "$work_real" "$(printf '%s\n' "$output" | sed -n '2p')" 'a new session starts in --cwd and cwd reports it'

if run_agr attach child --cwd /nonexistent </dev/null >/dev/null 2>&1; then
	:
fi
assert_eq "$work_real" "$(run_agr cwd child | sed -n '2p')" 're-attaching an existing session does not move it'

if run_agr attach homeless --cwd relative/dir </dev/null >/dev/null 2>&1; then
	:
fi
assert_eq 1 "$(tmux show-option -t '=homeless:' -qv @agr)" 'a relative --cwd is dropped, not fatal'
assert_eq '' "$(tmux show-option -t '=homeless:' -qv @agr_parent)" 'no --parent leaves no parent'

hash_dir="$REMOTE_TEST_TMP/a#{session_name}#(true)"
mkdir -p "$hash_dir"
hash_real=$(CDPATH= cd -- "$hash_dir" && pwd -P)
if run_agr attach hashed --cwd "$hash_dir" </dev/null >/dev/null 2>&1; then
	:
fi
assert_eq "$hash_real" "$(run_agr cwd hashed | sed -n '2p')" 'a # in --cwd is literal, not a tmux format'

missing=$(run_agr cwd nosuch)
assert_eq "agr${tab}@VERSION@" "$missing" 'cwd of a missing session prints only the header and an empty line'

set +e
bad=$(run_agr attach child --parent '../x' 2>&1)
bad_rc=$?
set -e
[ "$bad_rc" -ne 0 ] || { printf '%s\n' 'attach accepted an invalid parent' >&2; exit 1; }
assert_contains 'invalid parent name' "$bad" 'invalid parent refusal'

set +e
run_agr attach child --bogus x </dev/null >/dev/null 2>&1
unknown_rc=$?
set -e
assert_eq 2 "$unknown_rc" 'an unknown attach option is a usage error'
