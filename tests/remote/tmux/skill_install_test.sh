#!/bin/sh
set -eu

test_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

# The script agr install sends over ssh, taken from its Go constant.
# shellcheck disable=SC2016
script=$(sed -n 's/^const skillWriteScript = `\(.*\)`$/\1/p' "$REMOTE_TEST_ROOT/internal/remote/hooks.go")
[ -n "$script" ] || { printf '%s\n' 'skillWriteScript not found in hooks.go' >&2; exit 1; }

target=$HOME/.claude/skills/agr/SKILL.md
new='<!-- agr-skill 2.0.0 -->
new body'

# Missing: written, with its directory.
out=$(printf '%s\n' "$new" | sh -c "$script")
assert_eq written "$out" 'a missing file is written'
assert_eq "$new" "$(cat "$target")" 'written content'

# Marked: replaced.
printf '<!-- agr-skill 1.0.0 -->\nold body\n' > "$target"
out=$(printf '%s\n' "$new" | sh -c "$script")
assert_eq written "$out" 'a marked file is replaced'
assert_eq "$new" "$(cat "$target")" 'replaced content'

# Unmarked: left alone, and the stdin is drained.
printf 'my own skill\n' > "$target"
out=$(printf '%s\n' "$new" | sh -c "$script")
assert_eq kept "$out" 'an unmarked file is kept'
assert_eq 'my own skill' "$(cat "$target")" 'unmarked content'
[ ! -e "$target.tmp" ] || { printf '%s\n' 'a temp file was left behind' >&2; exit 1; }
