package remote

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
)

func TestRunnerValidationAndFallbackPaths(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	ssh := &runnerSSH{body: []byte("\n")}
	runner := NewRunner(ssh, dirs)
	var nilContext context.Context

	if _, err := runner.Home(context.Background(), "bad host"); err == nil {
		t.Fatal("Home() invalid host error = nil")
	}
	if _, err := runner.Home(nilContext, "host"); err == nil {
		t.Fatal("Home() nil context error = nil")
	}
	if err := runner.EnsureDirs(nilContext, "host"); err == nil {
		t.Fatal("EnsureDirs() nil context error = nil")
	}
	if err := runner.EnsureDirs(context.Background(), "bad host"); err == nil {
		t.Fatal("EnsureDirs() invalid host error = nil")
	}
	if _, err := runner.Data(nilContext, "host", "sessions"); err == nil {
		t.Fatal("Data() nil context error = nil")
	}
	if _, err := runner.Data(context.Background(), "host", ""); err == nil {
		t.Fatal("Data() empty verb error = nil")
	}
	if _, err := runner.Data(context.Background(), "bad host", "sessions"); err == nil {
		t.Fatal("Data() invalid host error = nil")
	}

	if _, err := runner.Home(context.Background(), "host"); err == nil || !strings.Contains(err.Error(), "empty home") {
		t.Fatalf("Home() empty response error = %v, want empty-home error", err)
	}

	ssh.err = errors.New("offline")
	if _, err := runner.Home(context.Background(), "other-host"); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("Home() SSH error = %v, want SSH error", err)
	}

	if _, err := NewRunnerWithVersion(ssh, dirs, "").Data(context.Background(), "host", "sessions"); err == nil {
		t.Fatal("Data() with empty version unexpectedly succeeded")
	}
}

func TestRunnerSessionsAndDataMissingHeader(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	if err := SaveHostInfo(dirs, "host", HostInfo{Home: "/home/remote"}); err != nil {
		t.Fatalf("SaveHostInfo() error = %v", err)
	}
	ssh := &runnerSSH{body: []byte("agr\t1.0.0\napi\t0\t-\t-\t-\n")}
	runner := NewRunnerWithVersion(ssh, dirs, "1.0.0")
	sessions, err := runner.Sessions(context.Background(), "host")
	if err != nil {
		t.Fatalf("Sessions() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].Name != "api" {
		t.Fatalf("Sessions() = %#v, want one api session", sessions)
	}

	ssh.body = []byte("not an agr header\n")
	if _, err := runner.Data(context.Background(), "host", "sessions"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Data() error = %v, want ErrNotInstalled", err)
	}
}

func TestRunnerHostInfoRoundTripAndDefaultVersion(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	want := HostInfo{Home: "/home/remote"}
	if err := SaveHostInfo(dirs, "host", want); err != nil {
		t.Fatalf("SaveHostInfo() error = %v", err)
	}
	got, err := LoadHostInfo(dirs, "host")
	if err != nil {
		t.Fatalf("LoadHostInfo() error = %v", err)
	}
	if got.Home != want.Home {
		t.Fatalf("LoadHostInfo() = %#v, want %#v", got, want)
	}
	if got := NewRunnerWithVersion(&runnerSSH{}, dirs, "").version; got != defaultVersion {
		t.Fatalf("default runner version = %q, want %q", got, defaultVersion)
	}
}

func TestChooseMuxCoversSelectionAndFailures(t *testing.T) {
	t.Helper()
	tests := []struct {
		name     string
		probe    ProbeResult
		override string
		want     string
		wantErr  string
	}{
		{name: "zmx preferred", probe: ProbeResult{ZmxVersion: "0.7.1", TmuxVersion: "3.4"}, want: "zmx"},
		{name: "tmux fallback", probe: ProbeResult{TmuxVersion: "3.4"}, want: "tmux"},
		{name: "no mux", probe: ProbeResult{}, wantErr: "no supported multiplexer"},
		{name: "invalid override", probe: ProbeResult{TmuxVersion: "3.4"}, override: "screen", wantErr: "unsupported multiplexer"},
		{name: "missing zmx override", probe: ProbeResult{TmuxVersion: "3.4"}, override: "zmx", wantErr: "zmx is not installed"},
		{name: "missing tmux override", probe: ProbeResult{ZmxVersion: "0.7.1"}, override: "tmux", wantErr: "tmux is not installed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			got, err := chooseMux(tt.probe, tt.override)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("chooseMux() error = %v, want text %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("chooseMux() = %q, %v, want %q, nil", got, err, tt.want)
			}
		})
	}
}

func TestRunnerProbeAndInstallWrappersValidateInputs(t *testing.T) {
	t.Helper()
	runner := NewRunner(&runnerSSH{}, pathstest.Dirs(t))
	var nilContext context.Context
	if _, err := runner.Probe(nilContext, "host"); err == nil {
		t.Fatal("Probe() nil context error = nil")
	}
	if _, err := runner.Probe(context.Background(), "bad host"); err == nil {
		t.Fatal("Probe() invalid host error = nil")
	}
	if err := runner.Install(nilContext, "host", ""); err == nil {
		t.Fatal("Install() nil context error = nil")
	}
	if err := runner.Install(context.Background(), "bad host", ""); err == nil {
		t.Fatal("Install() invalid host error = nil")
	}
}

func TestConcreteExecSSHAndTTY(t *testing.T) {
	t.Helper()
	var nilContext context.Context
	shim := sshCoverageShim(t)
	t.Setenv("PATH", filepath.Dir(shim)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REMOTE_SSH_MODE", "success")
	client := NewExecSSH()
	body, err := client.Run(context.Background(), "host", []byte("ignored"), "agr", "sessions")
	if err != nil || string(body) != "stdout\n" {
		t.Fatalf("ExecSSH.Run() = %q, %v, want stdout and nil", body, err)
	}

	t.Setenv("REMOTE_SSH_MODE", "unreachable")
	body, err = client.Run(context.Background(), "host", nil, "agr", "sessions")
	if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "network down") || string(body) != "partial\n" {
		t.Fatalf("ExecSSH.Run() unreachable = %q, %v, want sentinel, stderr, partial stdout", body, err)
	}

	t.Setenv("REMOTE_SSH_MODE", "exit")
	_, err = client.Run(context.Background(), "host", nil, "agr", "reap", "api")
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 42 || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("ExecSSH.Run() exit = %v, want ExitError 42 with stderr", err)
	}

	if _, err := (&ExecSSH{Path: filepath.Join(t.TempDir(), "missing-ssh")}).Run(context.Background(), "host", nil, "agr"); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("ExecSSH.Run() missing executable error = %v, want OS error", err)
	}
	if _, err := (&ExecSSH{}).Run(nilContext, "host", nil, "agr"); err == nil || !errors.Is(err, ErrInvalidSSHCommand) {
		t.Fatalf("ExecSSH.Run() nil context error = %v, want invalid command", err)
	}
	if _, err := client.Run(context.Background(), "bad host", nil, "agr"); err == nil || !errors.Is(err, ErrInvalidSSHCommand) {
		t.Fatalf("ExecSSH.Run() invalid host error = %v, want invalid command", err)
	}
	if _, err := client.Run(context.Background(), "host", nil); err == nil || !errors.Is(err, ErrInvalidSSHCommand) {
		t.Fatalf("ExecSSH.Run() empty argv error = %v, want invalid command", err)
	}

	if err := (&ExecTTY{}).Interactive(nilContext, "true"); err == nil || !errors.Is(err, ErrInvalidSSHCommand) {
		t.Fatalf("ExecTTY.Interactive() nil context error = %v, want invalid command", err)
	}
	if err := (&ExecTTY{}).Interactive(context.Background()); err == nil || !errors.Is(err, ErrInvalidSSHCommand) {
		t.Fatalf("ExecTTY.Interactive() empty argv error = %v, want invalid command", err)
	}
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Fatalf("LookPath(true): %v", err)
	}
	if err := (&ExecTTY{}).Interactive(context.Background(), truePath); err != nil {
		t.Fatalf("ExecTTY.Interactive() error = %v", err)
	}
}

func TestConcreteSSHHelpers(t *testing.T) {
	t.Helper()
	if got := resolveExecutable("agr-command-that-does-not-exist"); got != "agr-command-that-does-not-exist" {
		t.Fatalf("resolveExecutable() = %q, want unchanged name", got)
	}
	var exitErr *ExitError
	if got := exitErr.Error(); got != "remote command exited" {
		t.Fatalf("nil ExitError.Error() = %q, want fallback text", got)
	}
	if got := (&ExitError{Code: 4}).Error(); got != "remote command exited with status 4" {
		t.Fatalf("ExitError.Error() = %q, want status text", got)
	}
}

func sshCoverageShim(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ssh")
	script := `#!/bin/sh
case "${REMOTE_SSH_MODE:-success}" in
success)
  printf 'stdout\n'
  printf 'diagnostic\n' >&2
  ;;
unreachable)
  printf 'partial\n'
  printf 'network down\n' >&2
  exit 255
  ;;
exit)
  printf 'refused\n' >&2
  exit 42
  ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write ssh shim: %v", err)
	}
	return path
}
