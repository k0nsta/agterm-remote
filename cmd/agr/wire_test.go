package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

type dispatchRemoteFake struct {
	path  string
	items []remote.Session
	calls []string
}

func (f *dispatchRemoteFake) Sessions(_ context.Context, host string) ([]remote.Session, error) {
	f.calls = append(f.calls, "sessions "+host)
	return f.items, nil
}

func (f *dispatchRemoteFake) Reap(_ context.Context, host, name string) error {
	f.calls = append(f.calls, "reap "+host+" "+name)
	return nil
}

func (f *dispatchRemoteFake) AgrPath(string) string { return f.path }

type dispatchBridgeFake struct {
	calls []string
}

func (f *dispatchBridgeFake) Up(_ context.Context, host string) error {
	f.calls = append(f.calls, "up "+host)
	return nil
}

func (f *dispatchBridgeFake) Down(_ context.Context, host string) error {
	f.calls = append(f.calls, "down "+host)
	return nil
}

func (f *dispatchBridgeFake) ReloadBindings(context.Context) error {
	f.calls = append(f.calls, "reload")
	return nil
}

type dispatchInstallerFake struct{ calls []string }

func (f *dispatchInstallerFake) Install(_ context.Context, host, mux string) error {
	f.calls = append(f.calls, host+" "+mux)
	return nil
}

type dispatchDaemonFake struct {
	calls int
	err   error
}

func (f *dispatchDaemonFake) Run(context.Context) error {
	f.calls++
	return f.err
}

type dispatchTTYFake struct{ calls int }

func (f *dispatchTTYFake) Interactive(context.Context, ...string) error {
	f.calls++
	return nil
}

func TestRunDispatchesEveryRegisteredCommand(t *testing.T) {
	t.Helper()
	t.Setenv("AGTERM_SESSION_ID", "")
	dirs := paths.TestDirs(t)
	remoteFake := &dispatchRemoteFake{path: "/agr"}
	bridgeFake := &dispatchBridgeFake{}
	installer := &dispatchInstallerFake{}
	daemon := &dispatchDaemonFake{}
	tty := &dispatchTTYFake{}
	app := &application{
		dirs: dirs, remote: remoteFake, openRemote: remoteFake, store: bindings.New(dirs),
		bridge: bridgeFake, installer: installer, daemon: daemon,
		tty:      tty,
		hostInfo: func(string) (remote.HostInfo, error) { return remote.HostInfo{Mux: "tmux"}, nil },
	}

	tests := []struct {
		name  string
		argv  []string
		want  int
		check func(t *testing.T)
	}{
		{name: "open", argv: []string{"open", "host", "api"}, want: 0, check: func(t *testing.T) {
			if tty.calls != 1 {
				t.Fatalf("TTY calls = %d, want one", tty.calls)
			}
		}},
		{name: "ls", argv: []string{"ls", "host"}, want: 0, check: func(t *testing.T) {
			if !containsString(t, remoteFake.calls, "sessions host") {
				t.Fatalf("remote calls = %v, want ls dispatch", remoteFake.calls)
			}
		}},
		{name: "kill", argv: []string{"kill", "host", "api"}, want: 0, check: func(t *testing.T) {
			if !containsString(t, remoteFake.calls, "reap host api") {
				t.Fatalf("remote calls = %v, want kill dispatch", remoteFake.calls)
			}
		}},
		{name: "up", argv: []string{"up", "host"}, want: 0, check: func(t *testing.T) {
			if !containsString(t, bridgeFake.calls, "up host") {
				t.Fatalf("bridge calls = %v, want up dispatch", bridgeFake.calls)
			}
		}},
		{name: "down", argv: []string{"down", "host"}, want: 0, check: func(t *testing.T) {
			if !containsString(t, bridgeFake.calls, "down host") {
				t.Fatalf("bridge calls = %v, want down dispatch", bridgeFake.calls)
			}
		}},
		{name: "install", argv: []string{"install", "host", "--mux", "zmx"}, want: 0, check: func(t *testing.T) {
			if !containsString(t, installer.calls, "host zmx") {
				t.Fatalf("installer calls = %v, want install dispatch", installer.calls)
			}
		}},
		{name: "daemon", argv: []string{"daemon"}, want: 0, check: func(t *testing.T) {
			if daemon.calls != 1 {
				t.Fatalf("daemon calls = %d, want one", daemon.calls)
			}
		}},
		{name: "doctor", argv: []string{"doctor", "host"}, want: 1, check: func(t *testing.T) {}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Helper()
			var out, errout strings.Builder
			if got := runWithApplication(test.argv, &out, &errout, app); got != test.want {
				t.Fatalf("run(%v) exit = %d, want %d; stderr=%q", test.argv, got, test.want, errout.String())
			}
			test.check(t)
		})
	}

	if got, want := len(commandHandlers), 8; got != want {
		t.Fatalf("registered command count = %d, want %d", got, want)
	}
}

func TestRunUnknownCommandKeepsUsageBehavior(t *testing.T) {
	t.Helper()
	var out, errout strings.Builder
	if got := runWithApplication([]string{"unknown"}, &out, &errout, nil); got != 2 {
		t.Fatalf("unknown command exit = %d, want 2", got)
	}
	if errout.String() != usage+"\n" {
		t.Fatalf("stderr = %q, want usage", errout.String())
	}
}

func TestNewApplicationWiresConcreteGraph(t *testing.T) {
	t.Helper()
	app := newApplication()
	if app == nil || app.control == nil || app.status == nil || app.remote == nil || app.store == nil || app.bridge == nil || app.daemon == nil {
		t.Fatal("newApplication() returned an incomplete dependency graph")
	}
	if _, ok := app.remote.(*remote.Runner); !ok {
		t.Fatalf("remote dependency = %T, want *remote.Runner", app.remote)
	}
	if !reflect.TypeOf(app.daemon).AssignableTo(reflect.TypeOf((*daemonRunner)(nil)).Elem()) {
		t.Fatalf("daemon dependency = %T, does not satisfy daemonRunner", app.daemon)
	}
}

func TestRunDaemonReportsFailure(t *testing.T) {
	t.Helper()
	app := &application{daemon: &dispatchDaemonFake{err: errors.New("daemon failed")}}
	var out, errout strings.Builder
	if got := runWithApplication([]string{"daemon"}, &out, &errout, app); got != 1 || !strings.Contains(errout.String(), "daemon failed") {
		t.Fatalf("daemon run = exit %d stderr %q, want reported failure", got, errout.String())
	}
}

func containsString(t *testing.T, items []string, want string) bool {
	t.Helper()
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
