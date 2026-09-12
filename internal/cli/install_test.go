package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/remote"
)

type installFake struct {
	host  string
	mux   string
	err   error
	calls int
}

func (f *installFake) InstallResult(_ context.Context, host, mux string) (remote.InstallResult, error) {
	f.calls++
	f.host, f.mux = host, mux
	if f.err != nil {
		return remote.InstallResult{}, f.err
	}
	return remote.InstallResult{Host: host, Version: "1.2.3", Mux: "zmx", Relay: "nc"}, nil
}

func TestRunInstallParsesMuxAfterHost(t *testing.T) {
	t.Helper()
	installer := &installFake{}
	var out, errout strings.Builder
	if code := RunInstall(context.Background(), []string{"user@example.com", "--mux", "zmx"}, installer, &out, &errout); code != 0 {
		t.Fatalf("RunInstall() exit = %d, want zero: %s", code, errout.String())
	}
	if installer.calls != 1 || installer.host != "user@example.com" || installer.mux != "zmx" {
		t.Fatalf("install call = %#v, want host and zmx", installer)
	}
	// A silent success left the user guessing whether anything happened and
	// which multiplexer the probe picked.
	if got := out.String(); got != "installed agr 1.2.3 on user@example.com (mux zmx, relay nc)\n" {
		t.Fatalf("install confirmation = %q", got)
	}
}

func TestRunInstallRejectsInvalidInputWithoutCallingInstaller(t *testing.T) {
	t.Helper()
	for _, args := range [][]string{{"-host"}, {"host", "--mux", "bad"}, {"host", "--unknown"}, {"host", "other"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			t.Helper()
			installer := &installFake{}
			var errout strings.Builder
			if code := RunInstall(context.Background(), args, installer, nil, &errout); code != 2 {
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
	if code := RunInstall(context.Background(), []string{"host"}, installer, nil, &errout); code != 1 {
		t.Fatalf("RunInstall() exit = %d, want 1", code)
	}
	if !strings.Contains(errout.String(), "cannot reach host") {
		t.Fatalf("stderr = %q, want remote error", errout.String())
	}
}
