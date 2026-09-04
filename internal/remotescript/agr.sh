#!/bin/sh
set -eu

AGR_VERSION="@VERSION@"

have() {
	command -v "$1" >/dev/null 2>&1
}

usage() {
	printf '%s\n' "agr $AGR_VERSION — remote session bridge" >&2
	printf '%s\n' "usage: agr {attach|sessions|reap|status|--version} ..." >&2
	exit "${1:-2}"
}

valid_token() {
	case "${1:-}" in
		''|.*|-*|*[!A-Za-z0-9_.-]*) return 1 ;;
		*) return 0 ;;
	esac
}

valid_state() {
	case "${1:-}" in
		idle|active|completed|blocked) return 0 ;;
		*) return 1 ;;
	esac
}

if [ -f "$HOME/.config/agr/env" ]; then
	# shellcheck disable=SC1091
	. "$HOME/.config/agr/env"
fi

: "${AGR_MUX:=tmux}"
: "${AGR_SOCK:=$HOME/.cache/agr/bridge.sock}"

if [ -z "${AGR_RELAY:-}" ]; then
if have nc && nc -h 2>&1 | grep -q 'U'; then
		AGR_RELAY=nc
	elif have python3; then
		AGR_RELAY=python3
	elif have socat; then
		AGR_RELAY=socat
	else
		AGR_RELAY=
	fi
fi

export AGR_MUX AGR_SOCK AGR_RELAY

header() {
	printf 'agr\t%s\n' "$AGR_VERSION"
}

tmux_option() {
	n=$1
	key=$2
	tmux show-option -t "=$n:" -qv "$key" 2>/dev/null || :
}

tmux_owned() {
	n=$1
	[ "$(tmux_option "$n" @agr)" = 1 ] || [ -n "$(tmux_option "$n" @agr_target)" ]
}

tmux_state() {
	n=$1
	label=$(tmux_option "$n" @agr_state)
	if [ -z "$label" ]; then
		printf '%s|%s\n' - -
		return 0
	fi

	case "$label" in
		*@*)
			state=${label%@*}
			epoch=${label##*@}
			;;
		*)
			printf '%s|%s\n' - -
			return 0
			;;
	esac
	if ! valid_state "$state"; then
		printf '%s|%s\n' - -
		return 0
	fi
	case "$epoch" in
		''|*[!0-9]*)
			printf '%s|%s\n' - -
			return 0
			;;
		*) ;;
	esac
	now=$(date +%s)
	idle=$((now - epoch))
	[ "$idle" -ge 0 ] || idle=0
	printf '%s|%s\n' "$state" "$idle"
}

tmux_commands() {
	n=$1
	commands=$(tmux list-panes -s -t "=$n" -F '#{pane_current_command}' 2>/dev/null | sort -u || :)
	non_shell=
	shells=
	while IFS= read -r command; do
		[ -n "$command" ] || continue
		case "$command" in
			sh|bash|zsh|fish|dash)
			if [ -n "$shells" ]; then shells="$shells,$command"; else shells="$command"; fi
				;;
			*)
				if [ -n "$non_shell" ]; then non_shell="$non_shell,$command"; else non_shell="$command"; fi
				;;
		esac
	done <<EOF
$commands
EOF
	if [ -n "$non_shell" ]; then
		printf '%s' "$non_shell"
	elif [ -n "$shells" ]; then
		printf '%s' "$shells"
	else
		printf '%s' -
	fi
}

tmux_sessions() {
	list=$(tmux list-sessions -F '#{session_name}' 2>/dev/null || :)
	while IFS= read -r n; do
		[ -n "$n" ] || continue
		if ! tmux_owned "$n"; then
			continue
		fi
		attached=$(tmux display-message -p -t "=$n:" '#{session_attached}' 2>/dev/null || :)
		[ -n "$attached" ] || attached=0
		state_idle=$(tmux_state "$n")
		state=${state_idle%|*}
		idle=${state_idle#*|}
		cmds=$(tmux_commands "$n")
		printf '%s\t%s\t%s\t%s\t%s\n' "$n" "$attached" "$idle" "$cmds" "$state"
	done <<EOF
$list
EOF
}

tmux_attach() {
	n=$1
	if ! tmux has-session -t "=$n" 2>/dev/null; then
		tmux new-session -d -s "$n"
	fi
	tmux set-option -t "=$n:" @agr 1
	exec tmux attach -t "=$n"
}

tmux_reap() {
	n=$1
	if ! tmux has-session -t "=$n" 2>/dev/null; then
		printf "agr: no session '%s'\n" "$n" >&2
		return 1
	fi
	if ! tmux_owned "$n"; then
		printf "agr: '%s' exists but is not agr-managed\n" "$n" >&2
		return 1
	fi
	tmux kill-session -t "=$n"
	printf 'killed %s\n' "$n"
}

relay() {
	n=$1
	state=$2
	shift 2
	valid_token "$n" || return 0

	args=
	for arg in "$@"; do
		case "$arg" in
			--blink|--auto-reset)
				if [ -n "$args" ]; then args="$args,\"$arg\""; else args="\"$arg\""; fi
				;;
			*) ;;
		esac
	done
	payload=$(printf '{"cmd":"session-status","session":"%s","state":"%s","args":[%s]}\n' "$n" "$state" "$args")

	case "$AGR_RELAY" in
		nc)
			if printf '%s' "$payload" | nc -U -w1 "$AGR_SOCK"; then :; fi
			;;
		python3)
			if printf '%s' "$payload" | python3 -c '
import socket
import sys

sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
sock.settimeout(1)
sock.connect(sys.argv[1])
sock.sendall(sys.stdin.buffer.read())
' "$AGR_SOCK"; then :; fi
			;;
		socat)
			if printf '%s' "$payload" | socat - "UNIX-CONNECT:$AGR_SOCK"; then :; fi
			;;
		*) : ;;
	esac
	return 0
}

tmux_status() {
	state=$1
	shift
	[ -n "${TMUX_PANE:-}" ] || exit 0
	n=$(tmux display-message -p -t "$TMUX_PANE" '#{session_name}' 2>/dev/null) || exit 0
	[ -n "$n" ] || exit 0
	if ! tmux_owned "$n"; then
		exit 0
	fi
	label="$state@$(date +%s)"
	if ! tmux set-option -t "=$n:" @agr_state "$label" 2>/dev/null; then
		exit 0
	fi
	relay "$n" "$state" "$@"
	exit 0
}

dispatch() {
	sub=${1:-}
	shift || :
	case "$sub" in
		attach)
			[ "$#" -eq 1 ] || usage 2
			valid_token "$1" || { printf '%s\n' 'agr: invalid session name' >&2; exit 2; }
			case "$AGR_MUX" in
				tmux) tmux_attach "$1" ;;
				*) printf 'agr: unsupported multiplexer %s\n' "$AGR_MUX" >&2; exit 2 ;;
			esac
			;;
		sessions)
			[ "$#" -eq 0 ] || usage 2
			header
			case "$AGR_MUX" in
				tmux) tmux_sessions ;;
				*) printf 'agr: unsupported multiplexer %s\n' "$AGR_MUX" >&2; exit 2 ;;
			esac
			;;
		reap)
			[ "$#" -eq 1 ] || usage 2
			valid_token "$1" || { printf '%s\n' 'agr: invalid session name' >&2; exit 2; }
			header
			case "$AGR_MUX" in
				tmux) tmux_reap "$1" ;;
				*) printf 'agr: unsupported multiplexer %s\n' "$AGR_MUX" >&2; exit 2 ;;
			esac
			;;
		status)
			[ "$#" -ge 1 ] || exit 0
			state=$1
			shift
			valid_state "$state" || exit 2
			case "$#" in
				0|1|2) ;;
				*) exit 2 ;;
			esac
			valid_args=1
			for arg in "$@"; do
				case "$arg" in
					--blink|--auto-reset) ;;
					*) valid_args=0 ;;
				esac
			done
			[ "$valid_args" -eq 1 ] || exit 2
			case "$AGR_MUX" in
				tmux) tmux_status "$state" "$@" ;;
				*) exit 2 ;;
			esac
			;;
		--version|-V)
			[ "$#" -eq 0 ] || usage 2
			printf 'agr %s\n' "$AGR_VERSION"
			;;
		''|-h|--help) usage 0 ;;
		*) printf 'agr: unknown command %s\n' "$sub" >&2; usage 2 ;;
	esac
}

dispatch "$@"
