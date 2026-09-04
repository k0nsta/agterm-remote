package agterm_test

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/agterm"
)

func TestExecRunnerUsesPathShimAndExactArgv(t *testing.T) {
	t.Helper()
	shim := testAgtermctlShim(t)
	record := filepath.Join(filepath.Dir(shim), "argv.txt")
	t.Setenv("PATH", filepath.Dir(shim)+string(os.PathListSeparator)+os.Getenv("PATH"))
	path, err := exec.LookPath("agtermctl")
	if err != nil {
		t.Fatalf("LookPath(agtermctl): %v", err)
	}
	if err := (&agterm.ExecRunner{}).Run(context.Background(), path, "--record", record, "value with spaces", "last"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read argv record: %v", err)
	}
	if got, want := string(data), "value with spaces\nlast\n"; got != want {
		t.Fatalf("recorded argv = %q, want %q", got, want)
	}
}

func TestExecRunnerOutputSplitsStdoutAndExitCodeAndIncludesStderr(t *testing.T) {
	t.Helper()
	shim := testAgtermctlShim(t)
	out, exit, err := (&agterm.ExecRunner{}).Output(context.Background(), nil, shim, "--exit", "7")
	if string(out) != "stdout\n" {
		t.Fatalf("Output() stdout = %q, want stdout", out)
	}
	if exit != 7 {
		t.Fatalf("Output() exit = %d, want 7", exit)
	}
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("Output() error = %v, want captured stderr", err)
	}
}

func TestExecRunnerRunIncludesStderr(t *testing.T) {
	t.Helper()
	shim := testAgtermctlShim(t)
	err := (&agterm.ExecRunner{}).Run(context.Background(), shim, "--exit", "9")
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("Run() error = %v, want captured stderr", err)
	}
}

func TestExecRunnerDeliversStdin(t *testing.T) {
	t.Helper()
	shim := testAgtermctlShim(t)
	out, exit, err := (&agterm.ExecRunner{}).Output(context.Background(), []byte("stdin payload\n"), shim, "--stdin")
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	if exit != 0 || string(out) != "stdin payload\n" {
		t.Fatalf("Output() = (%q, %d), want stdin payload and 0", out, exit)
	}
}

func TestExecRunnerStreamStopsCleanlyOnContextCancel(t *testing.T) {
	t.Helper()
	shim := testAgtermctlShim(t)
	ctx, cancel := context.WithCancel(context.Background())
	reader, stop, err := (&agterm.ExecRunner{}).Stream(ctx, shim, "--stream")
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() || scanner.Text() != "first" {
		t.Fatalf("first stream line = %q, scan error = %v", scanner.Text(), scanner.Err())
	}
	cancel()
	if err := stop(); err != nil {
		t.Fatalf("stop() error = %v, want nil after cancellation", err)
	}
	if err := stop(); err != nil {
		t.Fatalf("second stop() error = %v, want nil", err)
	}
}

func testAgtermctlShim(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agtermctl")
	script := `#!/bin/sh
case "${1:-}" in
--record)
  record="$2"
  shift 2
  printf '%s\n' "$@" > "$record"
  ;;
--exit)
  printf 'stdout\n'
  printf 'unknown subcommand\n' >&2
  exit "${2:-1}"
  ;;
--stdin)
  cat
  ;;
--stream)
  printf 'first\n'
  while :; do printf 'tick\n'; done
  ;;
*)
  printf '%s\n' "$@"
  ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write agtermctl shim: %v", err)
	}
	return path
}
