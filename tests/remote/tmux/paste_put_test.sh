#!/bin/sh
set -eu

test_dir=$(CDPATH="" cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

tab=$(printf '\t')
paste_dir=$HOME/.cache/agr/paste
mkdir -p "$paste_dir"

output=$(printf 'image bytes\n' | run_agr paste-put 0-image.png)
assert_eq "agr${tab}@VERSION@" "$(printf '%s\n' "$output" | sed -n '1p')" 'paste-put prints the handshake header'
path=$(printf '%s\n' "$output" | sed -n '2p')
case "$path" in
	"$paste_dir"/*-0-image.png) ;;
	*) printf 'unexpected paste path: %s\n' "$path" >&2; exit 1 ;;
esac
[ -f "$path" ] || { printf '%s\n' 'paste path was not written' >&2; exit 1; }
assert_eq "image bytes" "$(cat "$path")" 'paste-put round-trips stdin'

file_count() {
	find "$paste_dir" -type f | wc -l | tr -d ' '
}

before=$(file_count)
for bad in ../x .hidden; do
	set +e
	bad_output=$(printf 'rejected' | run_agr paste-put "$bad" 2>&1)
	bad_rc=$?
	set -e
	assert_eq 2 "$bad_rc" "$bad is rejected with exit 2"
	assert_contains 'invalid paste name' "$bad_output" "$bad refusal names the invalid paste"
	assert_eq "$before" "$(file_count)" "$bad does not write a file"
done

old=$paste_dir/old.txt
fresh=$paste_dir/fresh.txt
printf 'old' > "$old"
touch -t 202001010000 "$old"
printf 'fresh' > "$fresh"
printf 'new' | run_agr paste-put 1-new.txt >/dev/null
[ ! -e "$old" ] || { printf '%s\n' 'old paste was not pruned' >&2; exit 1; }
[ -f "$fresh" ] || { printf '%s\n' 'fresh paste was pruned' >&2; exit 1; }

if [ "$(id -u)" -ne 0 ]; then
	chmod 500 "$paste_dir"
	set +e
	failed_output=$(printf 'x' | run_agr paste-put 2-x.txt 2>/dev/null)
	failed_rc=$?
	set -e
	chmod 700 "$paste_dir"
	[ "$failed_rc" -ne 0 ] || { printf '%s\n' 'unwritable paste dir did not fail' >&2; exit 1; }
	assert_eq "agr${tab}@VERSION@" "$(printf '%s\n' "$failed_output" | sed -n '1p')" 'a failed write still prints the header first'
fi
