package remote

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ProbeResult contains the remote capabilities needed by installation and
// doctor. Tool versions are empty when the corresponding tool is missing.
type ProbeResult struct {
	ZmxVersion      string
	ZmxLabels       bool
	ZmxDir          string
	TmuxVersion     string
	NCU             bool
	Python3         bool
	Socat           bool
	MoshServer      bool
	Home            string
	AgrVersion      string
	Sock            bool
	LegacyTargets   int
	LegacyAgrTarget int
}

// probeScript is deliberately constant. Values discovered on the remote are
// emitted as tab-separated data, while zmx is resolved by a login shell so
// its interactive PATH and socket directory are the ones we record.
const probeScript = `have() {
	command -v "$1" >/dev/null 2>&1
}

zmx_info=
if zmx_info=$(sh -lc 'command -v zmx >/dev/null 2>&1 && zmx version' 2>/dev/null); then
	zmx_version=$(sh -lc 'zmx --version' 2>/dev/null | awk 'NF >= 2 && ($1 == "zmx" || $1 == "ZMX") { print $2; exit }')
	if [ -z "$zmx_version" ]; then
		zmx_version=$(printf '%s\n' "$zmx_info" | awk 'NF >= 2 && ($1 == "zmx" || $1 == "ZMX") { print $2; exit }')
	fi
	[ -n "$zmx_version" ] || zmx_version=unknown
	zmx_dir=$(printf '%s\n' "$zmx_info" | sed -n 's/^[[:space:]]*socket[[:space:]_][[:space:]]*dir:[[:space:]]*//p' | sed -n '1p')
	if [ -z "$zmx_dir" ]; then
		zmx_dir=$(printf '%s\n' "$zmx_info" | sed -n 's/^[[:space:]]*socket_dir[[:space:]]*=[[:space:]]*//p' | sed -n '1p')
	fi
	printf 'zmx\t%s\n' "$zmx_version"
	if sh -lc 'zmx attach --help' 2>/dev/null | grep -q -- --labels; then
		printf 'zmx_labels\t1\n'
	else
		printf 'zmx_labels\t0\n'
	fi
	printf 'zmx_dir\t%s\n' "$zmx_dir"
else
	printf 'zmx\tMISSING\nzmx_labels\t0\nzmx_dir\t\n'
fi

if tmux_version=$(tmux -V 2>/dev/null); then
	printf 'tmux\t%s\n' "${tmux_version#tmux }"
else
	printf 'tmux\tMISSING\n'
fi

if have nc && nc -h 2>&1 | grep -q -- -U; then printf 'nc_u\t1\n'; else printf 'nc_u\t0\n'; fi
if have python3; then printf 'python3\t1\n'; else printf 'python3\t0\n'; fi
if have socat; then printf 'socat\t1\n'; else printf 'socat\t0\n'; fi
if have mosh-server; then printf 'mosh_server\t1\n'; else printf 'mosh_server\t0\n'; fi

printf 'home\t%s\n' "$HOME"
if [ -x "$HOME/.local/bin/agr" ]; then
	agr_version=$("$HOME/.local/bin/agr" --version 2>/dev/null || :)
	case "$agr_version" in
		'agr '*) printf 'agr\t%s\n' "${agr_version#agr }" ;;
		*) printf 'agr\tMISSING\n' ;;
	esac
else
	printf 'agr\tMISSING\n'
fi

if [ -S "$HOME/.cache/agr/bridge.sock" ]; then printf 'sock\t1\n'; else printf 'sock\t0\n'; fi

legacy_targets=0
if [ -d "$HOME/.cache/agterm/targets" ]; then
	legacy_targets=$(ls -A "$HOME/.cache/agterm/targets" 2>/dev/null | wc -l | tr -d ' ')
fi
printf 'legacy_targets\t%s\n' "$legacy_targets"

legacy_agr_target=0
if have tmux; then
	legacy_agr_target=$(tmux list-sessions -F '#{session_name}\t#{@agr_target}' 2>/dev/null | awk -F '\t' '$2 != "" { n++ } END { print n + 0 }')
fi
printf 'legacy_agr_target\t%s\n' "${legacy_agr_target:-0}"
`

// Probe executes the constant probe on the remote host and parses its
// stdout. SSH diagnostics remain in the SSH error and cannot corrupt the
// tab-separated response.
func (r *Runner) Probe(ctx context.Context, host string) (ProbeResult, error) {
	if err := validateHost(host); err != nil {
		return ProbeResult{}, err
	}
	if ctx == nil {
		return ProbeResult{}, errors.New("nil remote context")
	}
	body, err := r.sshClient().Run(ctx, host, []byte(probeScript), "sh")
	if err != nil {
		return ProbeResult{}, err
	}
	return ParseProbe(body)
}

// ParseProbe parses the key/value lines produced by probeScript. Unknown keys
// are ignored so a newer remote probe can still be consumed by an older CLI.
func ParseProbe(body []byte) (ProbeResult, error) {
	var result ProbeResult
	text := strings.TrimSuffix(string(body), "\n")
	if strings.TrimSpace(text) == "" {
		return ProbeResult{}, errors.New("empty remote probe")
	}

	seen := make(map[string]bool)
	for lineNumber, line := range strings.Split(text, "\n") {
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 || fields[0] == "" {
			return ProbeResult{}, fmt.Errorf("parse probe line %d: expected key and value separated by a tab", lineNumber+1)
		}
		key, value := fields[0], fields[1]
		if seen[key] {
			return ProbeResult{}, fmt.Errorf("parse probe line %d: duplicate key %q", lineNumber+1, key)
		}
		seen[key] = true

		var err error
		switch key {
		case "zmx", "zmx_version":
			result.ZmxVersion = parseMissing(value)
		case "zmx_labels":
			result.ZmxLabels, err = parseProbeBool(value)
		case "zmx_dir":
			result.ZmxDir = value
		case "tmux", "tmux_version":
			result.TmuxVersion = parseMissing(value)
		case "nc_u":
			result.NCU, err = parseProbeBool(value)
		case "python3":
			result.Python3, err = parseProbeBool(value)
		case "socat":
			result.Socat, err = parseProbeBool(value)
		case "mosh_server":
			result.MoshServer, err = parseProbeBool(value)
		case "home":
			result.Home = value
		case "agr", "agr_version":
			result.AgrVersion = parseMissing(value)
		case "sock", "sock_present":
			result.Sock, err = parseProbeBool(value)
		case "legacy_targets":
			result.LegacyTargets, err = parseProbeCount(value)
		case "legacy_agr_target", "legacy_agr_targets":
			result.LegacyAgrTarget, err = parseProbeCount(value)
		case "legacy":
			result.LegacyTargets, result.LegacyAgrTarget, err = parseLegacyCounts(value)
		}
		if err != nil {
			return ProbeResult{}, fmt.Errorf("parse probe key %q: %w", key, err)
		}
	}
	return result, nil
}

func parseMissing(value string) string {
	if value == "MISSING" {
		return ""
	}
	return value
}

func parseProbeBool(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "1", "true", "yes", "present":
		return true, nil
	case "0", "false", "no", "missing", "":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q", value)
	}
}

func parseProbeCount(value string) (int, error) {
	count, err := strconv.Atoi(value)
	if err != nil || count < 0 {
		return 0, fmt.Errorf("invalid count %q", value)
	}
	return count, nil
}

func parseLegacyCounts(value string) (targets, agrTargets int, err error) {
	for _, field := range strings.Split(value, ",") {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) != 2 {
			return 0, 0, fmt.Errorf("invalid legacy counts %q", value)
		}
		count, countErr := parseProbeCount(parts[1])
		if countErr != nil {
			return 0, 0, countErr
		}
		switch parts[0] {
		case "targets":
			targets = count
		case "agr_target", "agr_targets":
			agrTargets = count
		default:
			return 0, 0, fmt.Errorf("unknown legacy count %q", parts[0])
		}
	}
	return targets, agrTargets, nil
}

func (p ProbeResult) hasZmx() bool {
	return p.ZmxVersion != "" || p.ZmxDir != ""
}

func (p ProbeResult) hasTmux() bool {
	return p.TmuxVersion != ""
}
