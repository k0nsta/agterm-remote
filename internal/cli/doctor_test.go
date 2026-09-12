package cli

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/daemon"
	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

type doctorVersionFake struct {
	version string
	err     error
}

func (f doctorVersionFake) Version(context.Context) (string, error) {
	return f.version, f.err
}

type doctorAgtermFake struct {
	mode       string
	modeErr    error
	context    bool
	contextErr error
}

func (f doctorAgtermFake) RestoreMode(context.Context) (string, error) {
	return f.mode, f.modeErr
}

func (f doctorAgtermFake) SupportsContext(context.Context) (bool, error) {
	return f.context, f.contextErr
}

type doctorProbeFake struct {
	result remote.ProbeResult
	err    error
}

func (f doctorProbeFake) Probe(context.Context, string) (remote.ProbeResult, error) {
	return f.result, f.err
}

type doctorStatusFake struct {
	statuses []daemon.HostStatus
	err      error
}

func (f doctorStatusFake) Status(context.Context, string) ([]daemon.HostStatus, error) {
	return f.statuses, f.err
}

func TestDoctorCollectsAllBlocksWithoutLiveNetwork(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	listener, err := net.Listen("unix", dirs.Sock())
	if err != nil {
		t.Fatalf("listen fake agterm socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	deps := DoctorDependencies{
		LocalVersion: "1.0.0",
		SocketPath:   listener.Addr().String(),
		AppVersion:   doctorVersionFake{version: "0.26.0"},
		Agterm:       doctorAgtermFake{mode: "live", context: true},
		CtlPath:      "/Applications/agterm.app/Contents/MacOS/agtermctl",
		Remote: doctorProbeFake{result: remote.ProbeResult{
			AgrVersion: "1.0.0", ZmxVersion: "0.7.1", ZmxDir: "/run/user/1000/zmx",
			ZmxLabels: true, NCU: true, Sock: true,
		}},
		Daemon: doctorStatusFake{statuses: []daemon.HostStatus{{
			Host: "home", State: "up", Since: "2026-09-04T12:00:00Z", Attempts: 2,
			LastEvent: "2026-09-04T12:00:01Z",
		}}},
		MoshPath: func() string { return "/opt/homebrew/bin/mosh" },
	}
	report, err := Doctor(context.Background(), "home", deps)
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	if report.Local.AppVersion != "0.26.0" || !report.Local.SocketPresent || report.Remote.Probe.ZmxVersion != "0.7.1" || len(report.Daemon.Statuses) != 1 {
		t.Fatalf("Doctor() report = %#v, want all blocks populated", report)
	}
	var output strings.Builder
	RenderDoctor(report, &output)
	for _, want := range []string{
		"local:", "agterm app: 0.26.0", "restore mode: live (recommended)",
		"session context: yes", "mosh: yes", "remote (home):",
		"agr remote: 1.0.0", "mux: zmx 0.7.1 (--labels), ZMX_DIR=/run/user/1000/zmx",
		"relay: nc (-U)", "bridge socket: present", "daemon:",
		"home: state=up since=2026-09-04T12:00:00Z attempts=2 last event=2026-09-04T12:00:01Z",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("doctor output = %q, want %q", output.String(), want)
		}
	}
}

func TestRenderDoctorUnsupportedAndFailureStates(t *testing.T) {
	t.Helper()
	report := DoctorReport{
		Host: "home",
		Local: LocalDoctorReport{
			AgrVersion: "1.0.0", AppVersionError: errors.New("connection refused"),
			RestoreModeErr: agterm.ErrUnsupported, ContextErr: agterm.ErrUnsupported,
		},
		Remote: RemoteDoctorReport{Probe: remote.ProbeResult{AgrVersion: "0.9.0", TmuxVersion: "3.4", Python3: true, Term: "xterm-ghostty", TerminfoChecked: true}},
		Daemon: DaemonDoctorReport{Err: errors.New("socket missing")},
	}
	var output strings.Builder
	RenderDoctor(report, &output)
	got := output.String()
	for _, want := range []string{
		"agterm app: MISSING (connection refused)",
		"restore mode: n/a (agterm < 0.26)",
		"session context: n/a (agterm < 0.26)",
		"agr remote: 0.9.0 (version mismatch; run: agr install home)",
		"mux: tmux 3.4", "relay: python3", "terminfo (xterm-ghostty): MISSING (run: agr install home)", "daemon:\n  not running",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctor output = %q, want %q", got, want)
		}
	}
}

func TestRenderDoctorShowsRemoteMissingAndNonLiveRestoreMode(t *testing.T) {
	t.Helper()
	report := DoctorReport{
		Host:   "home",
		Local:  LocalDoctorReport{RestoreMode: "classic", ContextSupport: true},
		Remote: RemoteDoctorReport{Probe: remote.ProbeResult{}},
		Daemon: DaemonDoctorReport{Statuses: []daemon.HostStatus{{Host: "home", State: "down"}}},
	}
	var output strings.Builder
	RenderDoctor(report, &output)
	got := output.String()
	for _, want := range []string{
		"restore mode: classic (recommend live)",
		"agr remote: MISSING (run: agr install home)",
		"mux: MISSING (install zmx or tmux)",
		"relay: MISSING (install nc with -U support, python3, or socat)",
		"bridge socket: MISSING", "home: state=down since=- attempts=0 last event=-",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctor output = %q, want %q", got, want)
		}
	}
}

func TestRunDoctorRejectsInvalidHostWithoutCallingDependencies(t *testing.T) {
	t.Helper()
	called := false
	deps := DoctorDependencies{Remote: doctorProbeFunc(func(context.Context, string) (remote.ProbeResult, error) {
		called = true
		return remote.ProbeResult{}, nil
	})}
	var errout strings.Builder
	if code := RunDoctor(context.Background(), "-oProxyCommand=bad", deps, &strings.Builder{}, &errout); code != 2 {
		t.Fatalf("RunDoctor() exit = %d, want 2", code)
	}
	if called {
		t.Fatal("RunDoctor() called the remote probe for an invalid host")
	}
}

type doctorProbeFunc func(context.Context, string) (remote.ProbeResult, error)

func (f doctorProbeFunc) Probe(ctx context.Context, host string) (remote.ProbeResult, error) {
	return f(ctx, host)
}
