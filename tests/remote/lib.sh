#!/bin/sh
set -eu

REMOTE_TEST_ROOT=$(CDPATH= cd -- "$(dirname "$0")/../../.." && pwd)
REMOTE_SCRIPT=$REMOTE_TEST_ROOT/internal/remotescript/agr.sh

# Resolve the real binary before putting the shim directory first in PATH.
REAL_TMUX=$(command -v tmux)
[ -n "$REAL_TMUX" ] || { printf '%s\n' 'tmux is required for remote tests' >&2; exit 1; }
"$REAL_TMUX" -L agr-test kill-server >/dev/null 2>&1 || :

REMOTE_TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/agr-remote.XXXXXX")
export REMOTE_TEST_ROOT REMOTE_SCRIPT REAL_TMUX REMOTE_TEST_TMP
export HOME=$REMOTE_TEST_TMP/home
export AGR_MUX=tmux
export AGR_SOCK=$REMOTE_TEST_TMP/bridge.sock
export AGR_RELAY=nc
export NC_CAPTURE=$REMOTE_TEST_TMP/nc.capture

mkdir -p "$HOME/.cache/agr" "$HOME/.config/agr"
: > "$NC_CAPTURE"
export PATH=$REMOTE_TEST_ROOT/tests/remote/shims:$PATH

# stop_ctl_server kills the fake bridge the test started, if any.
stop_ctl_server() {
	if [ -n "${CTL_PID:-}" ]; then
		kill "$CTL_PID" >/dev/null 2>&1 || :
		wait "$CTL_PID" >/dev/null 2>&1 || :
		CTL_PID=
	fi
	[ -z "${CTL_DIR:-}" ] || rm -rf "$CTL_DIR"
}

cleanup_remote_test() {
	stop_ctl_server
	"$REAL_TMUX" -L agr-test kill-server >/dev/null 2>&1 || :
	rm -rf "$REMOTE_TEST_TMP"
}
trap cleanup_remote_test EXIT HUP INT TERM

run_agr() {
	"$REMOTE_SCRIPT" "$@"
}

# start_ctl_server starts the fake bridge (tests/remote/fake_ctl_server.py) on
# a short path under /tmp and points agr at it. The directory name is
# deliberately short: a unix socket path is limited to about 100 bytes.
start_ctl_server() {
	CTL_DIR=$(mktemp -d /tmp/agrctl.XXXXXX)
	: > "$CTL_DIR/reply"
	python3 "$REMOTE_TEST_ROOT/tests/remote/fake_ctl_server.py" "$CTL_DIR" &
	CTL_PID=$!
	tries=0
	while [ ! -e "$CTL_DIR/ready" ]; do
		tries=$((tries + 1))
		[ "$tries" -lt 100 ] || { printf '%s\n' 'fake ctl server did not start' >&2; exit 1; }
		sleep 0.1
	done
	AGR_SOCK=$CTL_DIR/s
	export AGR_SOCK
}

# ctl_call runs `agr ctl "$@"` and leaves its streams and status in
# $CTL_DIR/out, $CTL_DIR/err and CTL_RC.
ctl_call() {
	set +e
	run_agr ctl "$@" >"$CTL_DIR/out" 2>"$CTL_DIR/err"
	# shellcheck disable=SC2034
	CTL_RC=$?
	set -e
}

reset_capture() {
	: > "$NC_CAPTURE"
}

assert_eq() {
	expected=$1
	actual=$2
	message=${3:-values differ}
	if [ "$expected" != "$actual" ]; then
		printf 'assert_eq: %s\nexpected: %s\nactual: %s\n' "$message" "$expected" "$actual" >&2
		exit 1
	fi
}

assert_contains() {
	needle=$1
	haystack=$2
	message=${3:-missing expected text}
	case "$haystack" in
		*"$needle"*) ;;
		*) printf 'assert_contains: %s\nmissing: %s\nin: %s\n' "$message" "$needle" "$haystack" >&2; exit 1 ;;
	esac
}

assert_not_contains() {
	needle=$1
	haystack=$2
	message=${3:-unexpected text}
	case "$haystack" in
		*"$needle"*) printf 'assert_not_contains: %s\nfound: %s\n' "$message" "$needle" >&2; exit 1 ;;
		*) ;;
	esac
}
