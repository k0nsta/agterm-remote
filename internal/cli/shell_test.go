package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/remote"
)

func TestShellArgvForSSHAndMosh(t *testing.T) {
	t.Helper()
	tty := &openTTYFake{}
	deps := ShellDependencies{TTY: tty, HostInfo: func(string) (remote.HostInfo, error) { return remote.HostInfo{}, os.ErrNotExist }}
	if err := Shell(context.Background(), "host", "/srv/it's", deps); err != nil {
		t.Fatalf("Shell() error = %v", err)
	}
	wantSSH := []string{"ssh", "-t", "host", "--", `'sh' '-c' '[ -z "$1" ] || cd "$1" 2>/dev/null; exec "${SHELL:-/bin/sh}" -l' 'sh' '/srv/it'\''s'`}
	if !reflect.DeepEqual(tty.argv, wantSSH) {
		t.Fatalf("ssh argv = %#v, want %#v", tty.argv, wantSSH)
	}

	deps.HostInfo = func(string) (remote.HostInfo, error) { return remote.HostInfo{Mosh: true}, nil }
	deps.MoshPath = func() string { return "/usr/local/bin/mosh" }
	if err := Shell(context.Background(), "host", "", deps); err != nil {
		t.Fatalf("Shell() error = %v", err)
	}
	wantMosh := []string{"mosh", "host", "--", "sh", "-c", shellScript, "sh", ""}
	if !reflect.DeepEqual(tty.argv, wantMosh) {
		t.Fatalf("mosh argv = %#v, want %#v", tty.argv, wantMosh)
	}
}

func TestShellRejectsBadInputBeforeConnecting(t *testing.T) {
	t.Helper()
	tty := &openTTYFake{}
	deps := ShellDependencies{TTY: tty}
	for _, tc := range []struct{ host, dir string }{{"-oProxyCommand=x", ""}, {"host", "relative"}} {
		if err := Shell(context.Background(), tc.host, tc.dir, deps); err == nil {
			t.Fatalf("Shell(%q, %q) error = nil", tc.host, tc.dir)
		}
	}
	if tty.calls != 0 {
		t.Fatal("Shell() connected despite invalid input")
	}
	deps.HostInfo = func(string) (remote.HostInfo, error) { return remote.HostInfo{}, errors.New("corrupt") }
	if err := Shell(context.Background(), "host", "", deps); err == nil {
		t.Fatal("Shell() ignored an unreadable host-info cache")
	}
	if code := RunShell(context.Background(), "host", "rel", deps, nil); code != 1 {
		t.Fatalf("RunShell() = %d, want 1", code)
	}
}

type exitTTY struct{ code int }

func (f exitTTY) Interactive(ctx context.Context, _ ...string) error {
	return exec.CommandContext(ctx, "sh", "-c", "exit "+strconv.Itoa(f.code)).Run()
}

func TestShellPausesOnlyWhenSSHCouldNotConnect(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		code  int
		pause bool
	}{{255, true}, {1, false}, {0, false}} {
		paused := false
		deps := ShellDependencies{TTY: exitTTY{code: tc.code}, Pause: func() { paused = true }}
		_ = Shell(context.Background(), "host", "", deps)
		if paused != tc.pause {
			t.Fatalf("exit %d: paused = %v, want %v", tc.code, paused, tc.pause)
		}
	}
}
