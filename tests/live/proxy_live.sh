#!/bin/sh
# Live round trip of the remote agtermctl proxy against a real host and the
# running agterm. Not part of `make check`: it swaps the running agr daemon for
# bin/agr and puts bin/agr's remote script on the host, then restores both on
# exit. See tests/live/README.md.
#
# Usage: tests/live/proxy_live.sh
# Exit:  0 all checks passed, 1 a check failed (named on stderr), 2 unreachable.
set -eu

LIVE_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$LIVE_DIR/../.." && pwd)
HOST=${AGR_LIVE_HOST:-homelab}
AGR_BIN=${AGR_LIVE_BIN:-$ROOT/bin/agr}
AGTERMCTL=${AGTERMCTL:-agtermctl}
CACHE_DIR=${XDG_CACHE_HOME:-$HOME/.cache}/agr
PID_FILE=$CACHE_DIR/agr.pid
PREFIX=agrlive-

# State the trap relies on.
SWAPPED=
RESTORED=
PREV_PID=
RECORDED_AGR=
TEST_DAEMON_PID=
SKILL_CREATED=
WORK=
HELPER_PIDS=
STEP=setup
RUN_ID=$$
REMOTE_DIR=/tmp/agrlive-$RUN_ID
RR_N=0
RR_TAG=
RR_RC=
RR_OUT=

# Reads `agtermctl tree --json` on stdin; argv: op args.
TREE_PY='
import json, os, sys
op, args = sys.argv[1], sys.argv[2:]
data = json.load(sys.stdin)
rows = []
for ws in data["result"]["tree"].get("workspaces", []):
    rows.extend(ws.get("sessions", []))
def row(key):
    for r in rows:
        if r.get("id") == key or r.get("name") == key:
            return r
    return {}
def walk(o):
    if isinstance(o, dict):
        for k, v in o.items():
            yield k, v
            yield from walk(v)
    elif isinstance(o, list):
        for v in o:
            yield from walk(v)
if op == "ids":
    for r in rows:
        if r.get("name", "").startswith(args[0]):
            print(r["id"])
elif op == "id":
    print(row(args[0]).get("id", ""))
elif op == "fg":
    fg = row(args[0]).get("foreground") or []
    print(os.path.basename(fg[0]) if fg else "")
elif op == "attached":
    fg = row(args[0]).get("foreground") or []
    ok = len(fg) > 2 and os.path.basename(fg[0]) == "agr" and fg[1] == "open" and fg[2] == args[1]
    print("yes" if ok else "no")
elif op == "ask":
    print("yes" if any(k in ("ask", "askPending") and v for k, v in walk(row(args[0]))) else "no")
elif op == "stamp":
    print(row(args[0]).get("statusChangedAt", 0))
elif op == "panes":
    for s in row(args[0]).get("surfaces", []):
        print(s["kind"], s["paneID"])
'

# --- reporting ---------------------------------------------------------------

step() {
	STEP=$1
	printf 'live: %s\n' "$STEP"
}

fail() {
	printf 'FAIL: %s: %s\n' "$STEP" "$*" >&2
	exit 1
}

assert_contains() {
	case $2 in
		*"$1"*) ;;
		*) fail "missing '$1' in: $2" ;;
	esac
}

assert_not_contains() {
	case $2 in
		*"$1"*) fail "unexpected '$1' in: $2" ;;
		*) ;;
	esac
}

# assert_refused expects the last rr call to have failed with the proxy prefix.
assert_refused() {
	[ "$RR_RC" != 0 ] || fail "expected a refusal, got success: $RR_OUT"
	assert_contains 'agtermctl (via agr):' "$RR_OUT"
}

# --- agterm and host helpers -------------------------------------------------

ctl() {
	"$AGTERMCTL" "$@"
}

tq() {
	ctl tree --json | python3 -I -c "$TREE_PY" "$@"
}

host_sh() {
	ssh -o BatchMode=yes -o ConnectTimeout=8 "$HOST" "$@"
}

# wait_for DESC SECONDS CMD... polls CMD once a second.
wait_for() {
	desc=$1
	limit=$2
	shift 2
	waited=0
	until "$@"; do
		waited=$((waited + 1))
		[ "$waited" -lt "$limit" ] || fail "timed out waiting for $desc"
		sleep 1
	done
}

# new_local NAME [COMMAND] creates a local row and prints its id.
new_local() {
	if [ -n "${2:-}" ]; then
		ctl session new --name "$1" --no-select --command "$2" >/dev/null
	else
		ctl session new --name "$1" --no-select >/dev/null
	fi
	wait_for "row $1" 15 row_exists "$1"
	tq id "$1"
}

row_exists() {
	[ -n "$(tq id "$1")" ]
}

# new_remote NAME [OPEN-ARGS] creates a row running agr open and prints its id.
new_remote() {
	name=$1
	extra=${2:-}
	id=$(new_local "$name" "'$AGR_BIN' open '$HOST' '$name' $extra")
	wait_for "$name attached" 40 is_attached "$id"
	printf '%s\n' "$id"
}

is_attached() {
	[ "$(tq attached "$1" "$HOST")" = yes ]
}

# claude_row NAME creates a local row whose foreground is a program named claude.
claude_row() {
	id=$(new_local "$1")
	type_line "$id" '' "bash -c 'exec -a claude sleep 3600'"
	wait_for "$1 running claude" 20 fg_is "$id" claude
	printf '%s\n' "$id"
}

fg_is() {
	[ "$(tq fg "$1")" = "$2" ]
}

fg_not() {
	[ "$(tq fg "$1")" != "$2" ]
}

ask_is() {
	[ "$(tq ask "$1")" = "$2" ]
}

# pane_of ROW SIDE prints a pane id.
pane_of() {
	tq panes "$1" | awk -v side="$2" '$1 == side { print $2 }'
}

# type_line ROW PANE TEXT types TEXT and Enter into a pane of a local row.
type_line() {
	if [ -n "$2" ]; then
		printf '%s\n' "$3" | ctl session type --target "$1" --pane-id "$2" --stdin >/dev/null
	else
		printf '%s\n' "$3" | ctl session type --target "$1" --stdin >/dev/null
	fi
}

ctrl_c() {
	printf '\003' | ctl session type --target "$1" --stdin >/dev/null
}

# put_remote NAME CONTENT writes a file under the remote scratch directory.
put_remote() {
	printf '%s' "$2" | host_sh "cat > '$REMOTE_DIR/$1'"
}

# rr_start ROW PANE SCRIPT types a run of SCRIPT into the row's remote shell.
# The script's output and status land in files on the host.
rr_start() {
	RR_N=$((RR_N + 1))
	RR_TAG=r$RR_N
	base=$REMOTE_DIR/$RR_TAG
	# shellcheck disable=SC2016 # $HOME and $PATH expand on the host, not here
	printf 'PATH="$HOME/.local/bin:$PATH"\n{\n%s\n} >%s.out 2>&1\necho $? >%s.rc.tmp\nmv %s.rc.tmp %s.rc\n' "$3" "$base" "$base" "$base" "$base" |
		host_sh "cat > '$base.sh'"
	type_line "$1" "$2" "sh $base.sh"
}

rr_done() {
	host_sh "test -f '$REMOTE_DIR/$RR_TAG.rc'" 2>/dev/null
}

# rr_wait SECONDS fills RR_RC and RR_OUT for the last rr_start.
rr_wait() {
	wait_for "remote command $RR_TAG" "$1" rr_done
	RR_RC=$(host_sh "cat '$REMOTE_DIR/$RR_TAG.rc'")
	RR_OUT=$(host_sh "cat '$REMOTE_DIR/$RR_TAG.out'")
}

# rr ROW SCRIPT [SECONDS] runs SCRIPT in the row's remote shell and waits.
rr() {
	rr_start "$1" '' "$2"
	rr_wait "${3:-60}"
}

# rr_pane ROW PANE SCRIPT [SECONDS]: same, typed into one pane of a row.
rr_pane() {
	rr_start "$1" "$2" "$3"
	rr_wait "${4:-60}"
}

screen_has() {
	ctl session text --target "$1" 2>/dev/null | grep -q -- "$2"
}

# --- reachability, swap and restore -----------------------------------------

unreachable() {
	printf '%s or agterm unreachable\n' "$HOST" >&2
	exit 2
}

preflight() {
	host_sh true >/dev/null 2>&1 || unreachable
	ctl version >/dev/null 2>&1 || unreachable
	command -v python3 >/dev/null 2>&1 || fail 'python3 is required on the Mac'
	[ -x "$AGR_BIN" ] || fail "$AGR_BIN is missing (run make build)"
}

init_work() {
	WORK=$(mktemp -d "${TMPDIR:-/tmp}/agrlive.XXXXXX")
}

# record_daemon notes which agr executable runs the daemon now.
record_daemon() {
	PREV_PID=
	if [ -r "$PID_FILE" ]; then
		pid=$(tr -d '[:space:]' <"$PID_FILE")
		if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
			PREV_PID=$pid
		fi
	fi
	if [ -n "$PREV_PID" ]; then
		RECORDED_AGR=$(ps -p "$PREV_PID" -o comm= | sed 's/^ *//')
		case $RECORDED_AGR in
			/*) ;;
			*) RECORDED_AGR=$(command -v "$RECORDED_AGR" || :) ;;
		esac
	else
		RECORDED_AGR=$(command -v agr || :)
	fi
	[ -n "$RECORDED_AGR" ] || fail 'cannot find the agr that is installed now'
}

pid_gone() {
	! kill -0 "$1" 2>/dev/null
}

daemon_ready() {
	[ -r "$PID_FILE" ] && [ "$(tr -d '[:space:]' <"$PID_FILE" 2>/dev/null)" = "$TEST_DAEMON_PID" ]
}

# swap_in stops the running daemon, starts bin/agr's, and installs bin/agr's
# remote script. The trap (install_traps) has to be in place first.
swap_in() {
	step 'swap in bin/agr'
	record_daemon
	if host_sh 'test -e "$HOME/.claude/skills/agr"' >/dev/null 2>&1; then
		SKILL_CREATED=
	else
		SKILL_CREATED=1
	fi
	SWAPPED=1
	if [ -n "$PREV_PID" ]; then
		kill -TERM "$PREV_PID"
		wait_for 'the running daemon to stop' 15 pid_gone "$PREV_PID"
	fi
	"$AGR_BIN" daemon >"$WORK/daemon.log" 2>&1 &
	TEST_DAEMON_PID=$!
	wait_for 'the test daemon' 15 daemon_ready
	"$AGR_BIN" install "$HOST" >/dev/null || fail "$AGR_BIN install $HOST failed"
}

stop_helpers() {
	for hp in $HELPER_PIDS; do
		kill "$hp" 2>/dev/null || :
	done
	HELPER_PIDS=
}

close_rows() {
	for id in $(tq ids "$PREFIX" 2>/dev/null || :); do
		ctl session close --target "$id" >/dev/null 2>&1 || :
	done
}

kill_remote_sessions() {
	names=$("$AGR_BIN" ls "$HOST" 2>/dev/null | awk -v p="$PREFIX" 'index($1, p) == 1 { print $1 }' || :)
	if [ -n "$names" ]; then
		# shellcheck disable=SC2086 # one argument per session name
		"$AGR_BIN" kill "$HOST" $names >/dev/null 2>&1 || :
	fi
	host_sh "rm -rf '$REMOTE_DIR'" >/dev/null 2>&1 || :
}

stop_test_daemon() {
	if [ -n "$TEST_DAEMON_PID" ]; then
		kill -TERM "$TEST_DAEMON_PID" 2>/dev/null || :
		wait "$TEST_DAEMON_PID" 2>/dev/null || :
		TEST_DAEMON_PID=
	fi
}

reinstall_previous() {
	[ -n "$RECORDED_AGR" ] || return 0
	"$RECORDED_AGR" install "$HOST" >/dev/null 2>&1 || printf 'live: restore: %s install %s failed; run it by hand\n' "$RECORDED_AGR" "$HOST" >&2
}

remove_skill() {
	[ -z "$SKILL_CREATED" ] || host_sh 'rm -rf "$HOME/.claude/skills/agr"' >/dev/null 2>&1 || :
}

start_previous_daemon() {
	[ -n "$PREV_PID" ] || return 0
	nohup "$RECORDED_AGR" daemon </dev/null >/dev/null 2>&1 &
}

# restore_all undoes swap_in, once, in a fixed order. Every step is best effort.
restore_all() {
	[ -z "$RESTORED" ] || return 0
	[ -n "$SWAPPED" ] || { remove_work; return 0; }
	RESTORED=1
	set +e
	stop_helpers
	close_rows
	kill_remote_sessions
	stop_test_daemon
	reinstall_previous
	remove_skill
	start_previous_daemon
	remove_work
	return 0
}

remove_work() {
	[ -z "$WORK" ] || rm -rf "$WORK"
}

install_traps() {
	trap restore_all EXIT
	trap 'restore_all; exit 130' INT
	trap 'restore_all; exit 143' TERM
}

# --- checks ------------------------------------------------------------------

check_overlay_round_trip() {
	step 'overlay --html --js --block round trip through the shim'
	put_remote submit.html '<!doctype html><title>agrlive</title><script>agterm.request("session.overlay.submit",{args:{value:"agrlive-pong"}});</script>'
	rr "$R1" "agtermctl session overlay open --html $REMOTE_DIR/submit.html --js --block --target \"\$AGTERM_SESSION_ID\""
	[ "$RR_RC" = 0 ] || fail "exit $RR_RC: $RR_OUT"
	assert_contains '"outcome":"submitted"' "$RR_OUT"
	assert_contains 'agrlive-pong' "$RR_OUT"
}

check_program_overlay() {
	step 'overlay COMMAND from a host runs there as a shell line and holds the overlay'
	want="agrlive-on-$(host_sh hostname)"
	rr "$R1" "agtermctl session overlay open 'echo agrlive-on-\$(hostname); sleep 8' --target \"\$AGTERM_SESSION_ID\""
	[ "$RR_RC" = 0 ] || fail "exit $RR_RC: $RR_OUT"
	wait_for 'the program overlay output' 15 overlay_text_has "$R1" "$want"
	sleep 2
	overlay_text_has "$R1" "$want" || fail 'the program overlay closed early'
	ctl session overlay close --target "$R1" >/dev/null 2>&1 || :
}

overlay_text_has() {
	ctl session overlay text --target "$1" 2>/dev/null | grep -q -- "$2"
}

check_data_agterm_submit() {
	step 'a data-agterm submit button on a host page returns its value'
	put_remote attr.html '<!doctype html><title>agrlive</title><button id="b" type="button" data-agterm="session.overlay.submit" data-agterm-args="{&quot;value&quot;:&quot;agrlive-attr&quot;}">ok</button><script>document.getElementById("b").click();</script>'
	rr "$R1" "agtermctl session overlay open --html $REMOTE_DIR/attr.html --block --target \"\$AGTERM_SESSION_ID\""
	[ "$RR_RC" = 0 ] || fail "exit $RR_RC: $RR_OUT"
	assert_contains '"outcome":"submitted"' "$RR_OUT"
	assert_contains 'agrlive-attr' "$RR_OUT"
}

check_follow_dropped() {
	step '--follow from a host is dropped with a warning; the selection stays'
	before=$(selected_row)
	rr "$R1" "agtermctl session overlay open --html $REMOTE_DIR/attr.html --follow --block --target \"\$AGTERM_SESSION_ID\""
	[ "$RR_RC" = 0 ] || fail "exit $RR_RC: $RR_OUT"
	assert_contains '--follow ignored from a host' "$RR_OUT"
	assert_contains 'agrlive-attr' "$RR_OUT"
	[ "$(selected_row)" = "$before" ] || fail 'the selection moved'
}

selected_row() {
	ctl tree --json | python3 -I -c 'import json, sys
d = json.load(sys.stdin)
for ws in d["result"]["tree"].get("workspaces", []):
    for r in ws.get("sessions", []):
        if r.get("active"):
            print(r.get("id"))'
}

check_page_isolation() {
	step 'a host page cannot type, click or fetch out'
	mk=$(new_local "${PREFIX}mk" cat)
	python3 -I -c 'import http.server, socketserver, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        f = open(sys.argv[2], "a")
        f.write("hit\n")
        f.close()
        self.send_response(200)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
    log_message = lambda *a: None
class S(http.server.HTTPServer):
    def server_bind(self):
        socketserver.TCPServer.server_bind(self)
s = S(("127.0.0.1", 0), H)
f = open(sys.argv[1], "w")
f.write(str(s.server_address[1]))
f.close()
s.serve_forever()' "$WORK/port" "$WORK/hits" &
	HELPER_PIDS="$HELPER_PIDS $!"
	wait_for 'the fetch target' 10 test -s "$WORK/port"
	port=$(cat "$WORK/port")
	page=$(cat <<EOF
<!doctype html>
<button id="b" data-agterm="session.type" data-agterm-target="$mk" data-agterm-args="{&quot;text&quot;:&quot;agrlive-btn&quot;}">x</button>
<script>
document.getElementById("b").click();
agterm.request("session.type", {target: "$mk", args: {text: "agrlive-req"}}).catch(function () {});
fetch("http://127.0.0.1:$port/x", {mode: "no-cors"}).catch(function () {});
setTimeout(function () { agterm.request("session.overlay.submit", {args: {value: "isolated"}}); }, 2000);
</script>
EOF
	)
	put_remote isolated.html "$page"
	rr "$R1" "agtermctl session overlay open --html $REMOTE_DIR/isolated.html --js --block --target \"\$AGTERM_SESSION_ID\""
	[ "$RR_RC" = 0 ] || fail "exit $RR_RC: $RR_OUT"
	assert_contains 'isolated' "$RR_OUT"
	sleep 1
	[ ! -s "$WORK/hits" ] || fail 'the page reached another localhost port'
	if screen_has "$mk" agrlive-btn || screen_has "$mk" agrlive-req; then
		fail 'the marker pane received text from the page'
	fi
}

check_sibling() {
	step 'session type / text on a sibling row of the same host'
	rr "$R1" "printf %s agrlive-sib | agtermctl session type --target $R2 --stdin"
	[ "$RR_RC" = 0 ] || fail "type: exit $RR_RC: $RR_OUT"
	rr "$R1" "agtermctl session text --target $R2"
	[ "$RR_RC" = 0 ] || fail "text: exit $RR_RC: $RR_OUT"
	assert_contains 'agrlive-sib' "$RR_OUT"
	# the typed text sits at R2's prompt: clear it for later requests from R2
	ctrl_c "$R2"
}

check_type_refusals() {
	step 'session type refused on a local row with no grant'
	l1=$(new_local "${PREFIX}l1")
	rr "$R1" "printf x | agtermctl session type --target $l1 --stdin"
	assert_refused
	[ "$(tq ask "$l1")" = no ] || fail 'a shell row got a dialog'

	step 'session type refused on the local split pane of an owned row'
	sp=$(new_local "${PREFIX}sp")
	ctl session split visibility on --target "$sp" >/dev/null
	wait_for 'the split panes' 15 has_panes "$sp"
	left=$(pane_of "$sp" left)
	right=$(pane_of "$sp" right)
	type_line "$sp" "$left" "'$AGR_BIN' open '$HOST' '${PREFIX}sp'"
	wait_for 'the split row attached' 40 is_attached "$sp"
	rr_pane "$sp" "$left" "printf x | agtermctl session type --target $sp --pane-id $right --stdin"
	assert_refused

	step 'session type refused on a detached pane'
	# Typed into a shell, so the row survives the detach as a local shell.
	R4=$(new_local "${PREFIX}r4")
	type_line "$R4" '' "'$AGR_BIN' open '$HOST' '${PREFIX}r4'"
	wait_for "${PREFIX}r4 attached" 40 is_attached "$R4"
	wait_for 'r4 to detach' 20 detach_r4 "$R4"
	rr "$R1" "printf x | agtermctl session type --target $R4 --stdin"
	assert_refused
	assert_contains 'not attached' "$RR_OUT"
}

has_panes() {
	[ -n "$(pane_of "$1" right)" ]
}

# detach_r4 ROW detaches the tmux client (it may not have attached yet, so the
# caller retries) and reports whether the row has stopped being attached.
detach_r4() {
	host_sh "tmux detach-client -s '${PREFIX}r4'" >/dev/null 2>&1 || :
	sleep 1
	not_attached "$1"
}

not_attached() {
	! is_attached "$1"
}

check_session_new() {
	step 'session new --command gives a bound homelab row; an early type is typed or told to retry'
	tries=0
	while :; do
		tries=$((tries + 1))
		name=${PREFIX}n$tries
		script=$(cat <<EOF
id=\$(agtermctl session new --name $name --no-select --command "sh -c 'echo agrlive-marker-up; exec sleep 600'" | grep -Eo '[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}' | head -n 1)
echo "NEW=\$id"
printf x | agtermctl session type --target "\$id" --stdin
echo "typerc=\$?"
EOF
		)
		rr "$R1" "$script"
		# agr open is the foreground within milliseconds of session new, so the
		# type usually wins the race; the retry refusal is pinned by unit tests.
		# Whatever the timing, a refusal must carry the retry message.
		case $RR_OUT in
			*'typerc=0'*) break ;;
			*'typerc=1'*'not attached yet; retry'* | *'not attached yet; retry'*'typerc=1'*) break ;;
		esac
		[ "$tries" -lt 3 ] || fail "type right after session new: neither typed nor told to retry: $RR_OUT"
	done
	new_id=$(printf '%s\n' "$RR_OUT" | sed -n 's/^NEW=//p' | head -n 1)
	[ -n "$new_id" ] || fail "session new printed no row id: $RR_OUT"
	wait_for 'the new row attached' 40 is_attached "$new_id"
	wait_for 'the marker program on the host' 40 screen_has "$new_id" agrlive-marker-up
	ls_out=$("$AGR_BIN" ls "$HOST")
	assert_contains "$name" "$ls_out"
	assert_contains "$name" "$(printf '%s\n' "$ls_out" | grep " bound")"
}

check_orchestrator() {
	step 'a worker opened with --orchestrator types into the orchestrator, until it is at a shell'
	orch=$(claude_row "${PREFIX}orch")
	orch_pane=$(pane_of "$orch" left)
	W=$(new_remote "${PREFIX}w" "--orchestrator $orch:$orch_pane")
	rr "$W" "printf %s agrlive-orch | agtermctl session type --target $orch --stdin"
	[ "$RR_RC" = 0 ] || fail "type: exit $RR_RC: $RR_OUT"
	wait_for 'the text on the orchestrator pane' 10 screen_has "$orch" agrlive-orch
	ctrl_c "$orch"
	wait_for 'the orchestrator pane at a shell' 15 fg_not "$orch" claude
	rr "$W" "printf %s agrlive-late | agtermctl session type --target $orch --stdin"
	assert_refused
}

check_consent() {
	step 'consent dialog: opens, the host Ctrl-C closes it, nothing is stored'
	cl1=$(claude_row "${PREFIX}cl1")
	grants_before=$(grep -o consent "$CACHE_DIR/bindings.json" 2>/dev/null | wc -l)
	rr_start "$R1" '' "printf %s agrlive-consent | agtermctl session type --target $cl1 --stdin"
	wait_for 'the consent dialog' 20 ask_is "$cl1" yes
	ctrl_c "$R1"
	wait_for 'the dialog to close' 20 ask_is "$cl1" no
	grants_after=$(grep -o consent "$CACHE_DIR/bindings.json" 2>/dev/null | wc -l)
	if [ "$grants_after" -gt "$grants_before" ]; then
		fail 'a cancelled dialog stored a consent grant'
	fi
	if screen_has "$cl1" agrlive-consent; then
		fail 'cancelled text reached the claude pane'
	fi

	step 'consent dialog: a second request from the same host while it is open is refused'
	cl2=$(claude_row "${PREFIX}cl2")
	rr_start "$R1" '' "printf %s agrlive-first | agtermctl session type --target $cl2 --stdin"
	wait_for 'the consent dialog' 20 ask_is "$cl2" yes
	rr "$R2" "printf %s agrlive-second | agtermctl session type --target $cl2 --stdin"
	assert_refused
	case $RR_OUT in
		*'already open'* | *'dialog open'*) ;;
		*) fail "unexpected refusal: $RR_OUT" ;;
	esac
	ctrl_c "$R1"
	wait_for 'the dialog to close' 20 ask_is "$cl2" no

	step 'consent dialog: a host-bound target row is refused with no dialog'
	rr "$R1" "printf x | agtermctl session type --target $R4 --stdin" 15
	assert_refused
	[ "$(tq ask "$R4")" = no ] || fail 'a host-bound row got a dialog'
}

status_advanced() {
	awk -v a="$1" -v b="$(tq stamp "$R1")" 'BEGIN { exit !(b > a) }'
}

check_bridge() {
	step 'bridge: status events unchanged'
	before=$(tq stamp "$R1")
	rr "$R1" '"$HOME/.local/bin/agr" status active'
	[ "$RR_RC" = 0 ] || fail "agr status: exit $RR_RC: $RR_OUT"
	wait_for 'the status event to reach the row' 10 status_advanced "$before"

	step 'bridge: a refused leaf names the refusal'
	rr "$R1" 'agtermctl pick open'
	assert_refused

	step 'bridge: bridge-down message'
	# ~/.config/agr/env sets AGR_SOCK, so run under a HOME without that file.
	rr "$R1" "agr=\"\$HOME/.local/bin/agr\"; HOME=$REMOTE_DIR AGR_SOCK=$REMOTE_DIR/none.sock \"\$agr\" ctl version"
	[ "$RR_RC" != 0 ] || fail 'expected a failure with no bridge socket'
	assert_contains 'the bridge to the Mac is down' "$RR_OUT"

	step 'bridge: old-daemon message'
	script=$(cat <<EOF
python3 -c 'import socket, sys, time
s = socket.socket(socket.AF_UNIX)
s.bind(sys.argv[1])
s.listen(1)
time.sleep(30)' $REMOTE_DIR/old.sock &
srv=\$!
sleep 1
agr="\$HOME/.local/bin/agr"
HOME=$REMOTE_DIR AGR_SOCK=$REMOTE_DIR/old.sock "\$agr" ctl version
rc=\$?
kill \$srv
(exit \$rc)
EOF
	)
	rr "$R1" "$script" 30
	[ "$RR_RC" != 0 ] || fail 'expected a failure with a silent bridge'
	assert_contains "the Mac's agr daemon is too old or not running" "$RR_OUT"

	step 'bridge: agr down returns within teardownTimeout during a blocking call'
	put_remote hold.html '<!doctype html><p>agrlive hold</p>'
	rr_start "$R1" '' "agtermctl session overlay open --html $REMOTE_DIR/hold.html --js --block --target \"\$AGTERM_SESSION_ID\""
	wait_for 'the held overlay' 20 overlay_open "$R1"
	started=$(date +%s)
	"$AGR_BIN" down "$HOST" >/dev/null || fail 'agr down failed'
	elapsed=$(($(date +%s) - started))
	[ "$elapsed" -le 8 ] || fail "agr down took ${elapsed}s during a blocking call (teardownTimeout is 5s)"
	ctl session overlay close --target "$R1" >/dev/null 2>&1 || :
	"$AGR_BIN" up "$HOST" >/dev/null || fail 'agr up failed'
	wait_for 'the bridge to come back' 30 bridge_back
}

overlay_open() {
	ctl tree --json | python3 -I -c 'import json, sys
d = json.load(sys.stdin)
for ws in d["result"]["tree"].get("workspaces", []):
    for r in ws.get("sessions", []):
        if r.get("id") == sys.argv[1] and r.get("htmlOverlays"):
            sys.exit(0)
sys.exit(1)' "$1"
}

bridge_back() {
	rr_start "$R1" '' 'agtermctl version'
	waited=0
	until rr_done; do
		waited=$((waited + 1))
		[ "$waited" -lt 15 ] || return 1
		sleep 1
	done
	[ "$(host_sh "cat '$REMOTE_DIR/$RR_TAG.rc'")" = 0 ]
}

main() {
	preflight
	init_work
	install_traps
	swap_in
	host_sh "mkdir -p '$REMOTE_DIR'"
	step 'throwaway rows'
	R1=$(new_remote "${PREFIX}r1")
	R2=$(new_remote "${PREFIX}r2")
	check_overlay_round_trip
	check_program_overlay
	check_data_agterm_submit
	check_follow_dropped
	check_page_isolation
	check_sibling
	check_type_refusals
	check_session_new
	check_orchestrator
	check_consent
	check_bridge
	printf 'live: all checks passed\n'
}

# AGR_LIVE_SOURCE_ONLY=1 loads the functions without running anything
# (tests/live/restore_test.sh).
if [ -z "${AGR_LIVE_SOURCE_ONLY:-}" ]; then
	main "$@"
fi
