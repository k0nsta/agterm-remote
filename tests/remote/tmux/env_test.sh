#!/bin/sh
set -eu

test_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$test_dir/../lib.sh"

marker=$REMOTE_TEST_TMP/ran
: > "$marker"

# Wait for the new session's shell to answer, then read what it saw.
pane_env() {
	tmux show-environment -t "=$1" "$2" 2>/dev/null || :
}

run_agr attach envy --env AGTERM_SESSION_ID=row-1 --env AGTERM_ENABLED=1 \
	--command "echo ran >> '$marker'; echo \"id=\$AGTERM_SESSION_ID\" >> '$marker'" </dev/null >/dev/null 2>&1 || :

assert_eq 1 "$(tmux show-option -t '=envy:' -qv @agr)" 'attach with env and command marks the session owned'
assert_eq AGTERM_SESSION_ID=row-1 "$(pane_env envy AGTERM_SESSION_ID)" 'a new session carries the env'
assert_eq AGTERM_ENABLED=1 "$(pane_env envy AGTERM_ENABLED)" 'a new session carries every env pair'

i=0
while [ "$i" -lt 50 ] && [ "$(grep -c '^ran$' "$marker")" -lt 1 ]; do
	sleep 0.1
	i=$((i + 1))
done
assert_eq 1 "$(grep -c '^ran$' "$marker")" 'a new session runs CMD'
assert_contains 'id=row-1' "$(cat "$marker")" 'CMD sees the env'
assert_eq 1 "$(tmux list-panes -t '=envy' | wc -l | tr -d ' ')" 'CMD leaves a shell pane, not a dead one'

# The shell started by CMD keeps its old value after a re-attach, while new
# windows and panes get the new one.
run_agr attach envy --env AGTERM_SESSION_ID=row-2 --command "echo ran >> '$marker'" </dev/null >/dev/null 2>&1 || :
assert_eq AGTERM_SESSION_ID=row-2 "$(pane_env envy AGTERM_SESSION_ID)" 'a re-attach sets the new value'
assert_eq AGTERM_ENABLED=1 "$(pane_env envy AGTERM_ENABLED)" 'a re-attach leaves pairs it was not given'

tmux new-window -t '=envy' -d "echo \"new=\$AGTERM_SESSION_ID\" >> '$marker'; sleep 5"
i=0
while [ "$i" -lt 50 ] && ! grep -q '^new=' "$marker"; do
	sleep 0.1
	i=$((i + 1))
done
assert_contains 'new=row-2' "$(cat "$marker")" 'a window created after the re-attach sees the new value'
assert_not_contains 'id=row-2' "$(cat "$marker")" 'the process already running keeps the old value'

sleep 0.3
assert_eq 1 "$(grep -c '^ran$' "$marker")" 'a re-attach does not run CMD again'

# Values are validated before tmux sees them.
# shellcheck disable=SC2016
for bad in 'PATH=/tmp' 'AGTERM_X=a b' 'AGTERM_X=$(id)' 'AGTERM_x=1' 'AGTERM_X=' 'AGTERM_=1' 'noequals'; do
	set +e
	run_agr attach badenv --env "$bad" </dev/null >/dev/null 2>&1
	rc=$?
	set -e
	assert_eq 2 "$rc" "invalid --env $bad is a usage error"
done
set +e
run_agr attach badenv --command '' </dev/null >/dev/null 2>&1
rc=$?
set -e
assert_eq 2 "$rc" 'an empty --command is a usage error'
if tmux has-session -t '=badenv' 2>/dev/null; then
	printf '%s\n' 'a refused attach created a session' >&2
	exit 1
fi
