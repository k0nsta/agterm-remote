package remote

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/k0nsta/agterm-remote/internal/remotescript"
)

const (
	envWriteScript    = `cat > "$HOME/.config/agr/env.tmp" && chmod 600 "$HOME/.config/agr/env.tmp" && mv -f "$HOME/.config/agr/env.tmp" "$HOME/.config/agr/env"`
	remoteScriptWrite = `cat > "$HOME/.local/bin/agr.tmp" && chmod +x "$HOME/.local/bin/agr.tmp" && mv -f "$HOME/.local/bin/agr.tmp" "$HOME/.local/bin/agr"`
)

// InstallResult describes the choices made for a successful installation.
// It lets a CLI print a user-facing summary without re-probing the host.
type InstallResult struct {
	Host    string
	Version string
	Mux     string
	Relay   string
}

// Install probes the host, installs the versioned remote script, writes the
// selected environment, persists HostInfo, and merges Claude Code hooks.
func (r *Runner) Install(ctx context.Context, host, muxOverride string) error {
	_, err := r.InstallResult(ctx, host, muxOverride)
	return err
}

// InstallResult performs Install and returns the resulting choices.
func (r *Runner) InstallResult(ctx context.Context, host, muxOverride string) (InstallResult, error) {
	if err := validateHost(host); err != nil {
		return InstallResult{}, err
	}
	if ctx == nil {
		return InstallResult{}, errors.New("nil remote context")
	}

	probe, err := r.Probe(ctx, host)
	if err != nil {
		return InstallResult{}, err
	}
	mux, err := chooseMux(probe, muxOverride)
	if err != nil {
		return InstallResult{}, err
	}
	relay, err := chooseRelay(probe)
	if err != nil {
		return InstallResult{}, err
	}

	// EnsureDirs is intentionally before every write, including writes to
	// the remote env and binary. This also makes a failed directory setup
	// leave both remote files and local HostInfo untouched.
	if err := r.EnsureDirs(ctx, host); err != nil {
		return InstallResult{}, err
	}
	env := installEnv(mux, relay, probe.ZmxDir)
	if _, err := r.sshClient().Run(ctx, host, []byte(env), "sh", "-c", envWriteScript); err != nil {
		return InstallResult{}, fmt.Errorf("write remote agr environment: %w", err)
	}
	if _, err := r.sshClient().Run(ctx, host, remotescript.Script(r.version), "sh", "-c", remoteScriptWrite); err != nil {
		return InstallResult{}, fmt.Errorf("write remote agr script: %w", err)
	}

	info := HostInfo{
		Home:        probe.Home,
		Mux:         mux,
		Relay:       relay,
		Mosh:        probe.MoshServer,
		AgrVersion:  r.version,
		ZmxVersion:  probe.ZmxVersion,
		TmuxVersion: probe.TmuxVersion,
		ZmxLabels:   probe.ZmxLabels,
		ProbedAt:    time.Now(),
	}
	if err := SaveHostInfo(r.dirs, host, info); err != nil {
		return InstallResult{}, err
	}
	if err := r.mergeHooks(ctx, host); err != nil {
		return InstallResult{}, err
	}

	return InstallResult{Host: host, Version: r.version, Mux: mux, Relay: relay}, nil
}

func chooseMux(probe ProbeResult, override string) (string, error) {
	if override != "" {
		switch override {
		case "zmx":
			if !probe.hasZmx() {
				return "", errors.New("zmx is not installed; install zmx or choose --mux tmux")
			}
		case "tmux":
			if !probe.hasTmux() {
				return "", errors.New("tmux is not installed; install tmux or choose --mux zmx")
			}
		default:
			return "", fmt.Errorf("unsupported multiplexer %q (choose zmx or tmux)", override)
		}
		return override, nil
	}
	if probe.hasZmx() {
		return "zmx", nil
	}
	if probe.hasTmux() {
		return "tmux", nil
	}
	return "", errors.New("no supported multiplexer found; install zmx or tmux")
}

func chooseRelay(probe ProbeResult) (string, error) {
	switch {
	case probe.NCU:
		return "nc", nil
	case probe.Python3:
		return "python3", nil
	case probe.Socat:
		return "socat", nil
	default:
		return "", errors.New("no Unix-socket relay found; install nc with -U support, python3, or socat")
	}
}

func installEnv(mux, relay, zmxDir string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "export AGR_MUX=%s\n", mux)
	fmt.Fprintf(&builder, "export AGR_RELAY=%s\n", relay)
	builder.WriteString("export AGR_SOCK=$HOME/.cache/agr/bridge.sock\n")
	fmt.Fprintf(&builder, "export ZMX_DIR=%s\n", shellEnvValue(zmxDir))
	return builder.String()
}

func shellEnvValue(value string) string {
	if value == "" {
		return ""
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("_./-", character) {
			continue
		}
		return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	}
	return value
}

func (r *Runner) mergeHooks(ctx context.Context, host string) error {
	existing, err := r.sshClient().Run(ctx, host, nil, "sh", "-c", hooksReadScript)
	if err != nil {
		return fmt.Errorf("read Claude Code settings: %w", err)
	}
	merged, changed, err := MergeHooks(existing)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if _, err := r.sshClient().Run(ctx, host, merged, "sh", "-c", hooksWriteScript); err != nil {
		return fmt.Errorf("write Claude Code settings: %w", err)
	}
	return nil
}
