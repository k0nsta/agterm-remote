package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type installFake struct {
	host  string
	mux   string
	err   error
	calls int
}

func (f *installFake) Install(_ context.Context, host, mux string) error {
	f.calls++
	f.host, f.mux = host, mux
	return f.err
}

func TestRunInstallParsesMuxAfterHost(t *testing.T) {
	t.Helper()
	installer := &installFake{}
	var errout strings.Builder
	if code := RunInstall(context.Background(), []string{"user@example.com", "--mux", "zmx"}, installer, &errout); code != 0 {
		t.Fatalf("RunInstall() exit = %d, want zero: %s", code, errout.String())
	}
	if installer.calls != 1 || installer.host != "user@example.com" || installer.mux != "zmx" {
		t.Fatalf("install call = %#v, want host and zmx", installer)
	}
}

func TestRunInstallRejectsInvalidInputWithoutCallingInstaller(t *testing.T) {
	t.Helper()
	for _, args := range [][]string{{"-host"}, {"host", "--mux", "bad"}, {"host", "--unknown"}, {"host", "other"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			t.Helper()
			installer := &installFake{}
			var errout strings.Builder
			if code := RunInstall(context.Background(), args, installer, &errout); code != 2 {
				t.Fatalf("RunInstall(%v) exit = %d, want 2", args, code)
			}
			if installer.calls != 0 {
				t.Fatalf("installer calls = %d, want none", installer.calls)
			}
		})
	}
}

func TestRunInstallReturnsRemoteFailure(t *testing.T) {
	t.Helper()
	installer := &installFake{err: errors.New("cannot reach host")}
	var errout strings.Builder
	if code := RunInstall(context.Background(), []string{"host"}, installer, &errout); code != 1 {
		t.Fatalf("RunInstall() exit = %d, want 1", code)
	}
	if !strings.Contains(errout.String(), "cannot reach host") {
		t.Fatalf("stderr = %q, want remote error", errout.String())
	}
}
