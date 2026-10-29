#!/bin/sh
# Checks the restore trap of proxy_live.sh with stand-in agr, agtermctl, ssh and
# ps on PATH: every restore step runs, in order, on EXIT and on INT. Needs no
# host and no agterm; run it directly.
set -eu

test_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)

TMP=$(mktemp -d "${TMPDIR:-/tmp}/agrlive-restore.XXXXXX")
cleanup() {
	if [ -r "$TMP/pids" ]; then
		while read -r pid; do
			kill "$pid" 2>/dev/null || :
		done <"$TMP/pids"
	fi
	rm -rf "$TMP"
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$TMP/stub" "$TMP/prev" "$TMP/test" "$TMP/cache/agr"
LOG=$TMP/log
: >"$LOG"
: >"$TMP/pids"

# One stand-in serves as the previous agr and as bin/agr; $0 tells them apart.
cat >"$TMP/prev/agr" <<'EOF'
#!/bin/sh
echo "agr $0 $*" >>"$STUB_LOG"
case $1 in
	daemon)
		echo "daemon-start $0" >>"$STUB_LOG"
		echo $$ >>"$STUB_PIDS"
		echo $$ >"$XDG_CACHE_HOME/agr/agr.pid"
		trap 'echo "daemon-stop $0" >>"$STUB_LOG"; exit 0' TERM
		while :; do sleep 1; done
		;;
	ls)
		printf 'NAME ATT IDLE STATE CMD ROW\nagrlive-a 1 1s - - X\nother 1 1s - - Y\n'
		;;
esac
exit 0
EOF
cp "$TMP/prev/agr" "$TMP/test/agr"

cat >"$TMP/stub/agtermctl" <<'EOF'
#!/bin/sh
echo "agtermctl $*" >>"$STUB_LOG"
if [ "$1" = tree ]; then
	printf '%s\n' '{"ok":true,"result":{"tree":{"workspaces":[{"sessions":[{"name":"agrlive-a","id":"AAA"},{"name":"user-row","id":"UUU"},{"name":"agrlive-b","id":"BBB"}]}]}}}'
fi
exit 0
EOF

cat >"$TMP/stub/ssh" <<'EOF'
#!/bin/sh
echo "ssh $*" >>"$STUB_LOG"
# The skill directory does not exist yet, so the run creates it.
case $* in
	*'test -e'*) exit 1 ;;
esac
exit 0
EOF

cat >"$TMP/stub/ps" <<'EOF'
#!/bin/sh
echo "$STUB_PREV_AGR"
EOF
chmod +x "$TMP/prev/agr" "$TMP/test/agr" "$TMP/stub/agtermctl" "$TMP/stub/ssh" "$TMP/stub/ps"

export STUB_LOG=$LOG STUB_PIDS=$TMP/pids STUB_PREV_AGR=$TMP/prev/agr
export XDG_CACHE_HOME=$TMP/cache
export AGR_LIVE_BIN=$TMP/test/agr AGR_LIVE_HOST=stubhost
export PATH=$TMP/stub:$PATH

# line_of PATTERN prints the first log line number matching PATTERN, or fails.
line_of() {
	n=$(grep -n -- "$1" "$LOG" | head -n 1 | cut -d: -f1)
	[ -n "$n" ] || { printf 'restore_test: %s: step missing: %s\n--- log\n%s\n' "$scenario" "$1" "$(cat "$LOG")" >&2; exit 1; }
	printf '%s\n' "$n"
}

# assert_order PATTERN... fails unless the patterns first appear in this order.
assert_order() {
	last=0
	for pattern in "$@"; do
		n=$(line_of "$pattern")
		[ "$n" -gt "$last" ] || { printf 'restore_test: %s: out of order: %s\n--- log\n%s\n' "$scenario" "$pattern" "$(cat "$LOG")" >&2; exit 1; }
		last=$n
	done
}

# run_scenario NAME TRIGGER runs proxy_live.sh's swap and traps in a child shell,
# clears the log, fires TRIGGER, and checks the restore log.
run_scenario() {
	scenario=$1
	trigger=$2
	: >"$LOG"
	# A running previous daemon that the swap will stop.
	# Double fork, so the stopped daemon is reaped and `kill -0` sees it gone.
	prev=$(sh -c 'sleep 300 >/dev/null 2>&1 & echo $!')
	echo "$prev" >>"$TMP/pids"
	echo "$prev" >"$TMP/cache/agr/agr.pid"
	set +e
	AGR_LIVE_SOURCE_ONLY=1 LOG=$LOG TRIGGER=$trigger LIVE=$test_dir/proxy_live.sh sh -c '
		. "$LIVE"
		init_work
		install_traps
		swap_in
		: >"$LOG"
		case $TRIGGER in
			exit) exit 0 ;;
			int) kill -INT $$; sleep 5; exit 9 ;;
		esac
	'
	rc=$?
	set -e
	case $trigger in
		exit) [ "$rc" = 0 ] || { printf 'restore_test: %s: exit %s\n' "$scenario" "$rc" >&2; exit 1; } ;;
		int) [ "$rc" = 130 ] || { printf 'restore_test: %s: exit %s, want 130\n' "$scenario" "$rc" >&2; exit 1; } ;;
	esac
	assert_order \
		'agtermctl session close --target AAA' \
		'agtermctl session close --target BBB' \
		"agr $TMP/test/agr kill stubhost agrlive-a" \
		"daemon-stop $TMP/test/agr" \
		"agr $TMP/prev/agr install stubhost" \
		'ssh .*skills/agr' \
		"daemon-start $TMP/prev/agr"
	if grep -q 'close --target UUU' "$LOG"; then
		printf 'restore_test: %s: closed a row that is not agrlive-\n' "$scenario" >&2
		exit 1
	fi
	kill "$prev" 2>/dev/null || :
	printf 'restore_test: PASS %s\n' "$scenario"
}

run_scenario EXIT exit
run_scenario INT int
