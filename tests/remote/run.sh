#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
failed=0
for test_script in "$root"/*/*.sh; do
	[ -f "$test_script" ] || continue
	printf 'remote test: %s\n' "${test_script#"$root"/}"
	if "$test_script"; then
		printf 'remote test: PASS %s\n' "${test_script#"$root"/}"
	else
		printf 'remote test: FAIL %s\n' "${test_script#"$root"/}" >&2
		failed=1
	fi
done
exit "$failed"
