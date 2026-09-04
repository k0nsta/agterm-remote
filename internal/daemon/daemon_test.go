package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/remote"
	"github.com/k0nsta/agterm-remote/internal/token"
)

type daemonRemote struct {
	mu          sync.Mutex
	ensureCalls map[string]int
	homeCalls   map[string]int
	home        map[string]string
}

func (r *daemonRemote) Home(_ context.Context, host string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.homeCalls[host]++
	return r.home[host], nil
}

func (r *daemonRemote) Sessions(context.Context, string) ([]remote.Session, error) {
	return nil, nil
}

func (r *daemonRemote) EnsureDirs(_ context.Context, host string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureCalls[host]++
	return nil
}

func (r *daemonRemote) ensured(t *testing.T, host string) int {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensureCalls[host]
}

type daemonRows struct {
	rows  []string
	calls int
}

func (r *daemonRows) Tree(context.Context) ([]string, error) {
	r.calls++
	return append([]string(nil), r.rows...), nil
}

type daemonSink struct {
	err   error
	mu    sync.Mutex
	calls []string
}

func (s *daemonSink) Status(_ context.Context, target string, _ agterm.StatusArgs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, target)
	return s.err
}

type daemonSupervisor struct {
	runStarted chan struct{}
	stopCalled chan struct{}
	stopOnce   sync.Once
}

func (s *daemonSupervisor) Run(ctx context.Context) error {
	select {
	case <-s.runStarted:
	default:
		close(s.runStarted)
	}
	<-ctx.Done()
	return nil
}

func (s *daemonSupervisor) Stop() {
	s.stopOnce.Do(func() { close(s.stopCalled) })
}

type daemonSupervisors struct {
	mu      sync.Mutex
	created []*daemonSupervisor
	args    [][]string
}

func (f *daemonSupervisors) New(host, hostKey, remoteSock, localSock string) Supervisor {
	f.mu.Lock()
	defer f.mu.Unlock()
	supervisor := &daemonSupervisor{runStarted: make(chan struct{}), stopCalled: make(chan struct{})}
	f.created = append(f.created, supervisor)
	f.args = append(f.args, []string{host, hostKey, remoteSock, localSock})
	return supervisor
}

func daemonConfig(t *testing.T, dirs paths.Dirs, r Remote, rows Rows, sink StatusSink, supervisors Supervisors) Config {
	t.Helper()
	return Config{
		Dirs:        dirs,
		Remote:      r,
		Rows:        rows,
		Sink:        sink,
		Supervisors: supervisors,
		Logger:      log.New(io.Discard, "", 0),
	}
}

func daemonBinding(t *testing.T, row, host, name string) bindings.Binding {
	t.Helper()
	return bindings.Binding{Row: row, PaneID: "pane-" + row, Pane: "left", Host: host, Name: name, Mux: "tmux", BoundAt: time.Now().UTC()}
}

func TestDaemonSingleInstanceLockRefusesSecondDaemon(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	first := New(Config{Dirs: dirs, Logger: log.New(io.Discard, "", 0)})
	second := New(Config{Dirs: dirs, Logger: log.New(io.Discard, "", 0)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := first.Start(ctx); err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	defer func() { _ = first.Close() }()

	if err := second.Start(context.Background()); err == nil || !containsText(t, err, "already running") {
		t.Fatalf("second Start() error = %v, want already-running error", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close() on unstarted second daemon = %v", err)
	}
}

func TestDaemonStartsListenerAndSupervisorWithExpectedRemoteSocket(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	host := "user@example.com"
	remote := &daemonRemote{
		ensureCalls: map[string]int{},
		homeCalls:   map[string]int{},
		home:        map[string]string{host: "/Users/test-user"},
	}
	rows := &daemonRows{rows: []string{"frontmost"}}
	supervisors := &daemonSupervisors{}
	store := bindings.New(dirs)
	if err := store.Bind(daemonBinding(t, "row-1", host, "api")); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}

	daemon := New(daemonConfig(t, dirs, remote, rows, &daemonSink{}, supervisors))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := daemon.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = daemon.Close() }()
	waitForDaemonPath(t, dirs.Recv(token.FileKey(host)))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		supervisors.mu.Lock()
		created := len(supervisors.created)
		supervisors.mu.Unlock()
		if created == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	supervisors.mu.Lock()
	defer supervisors.mu.Unlock()
	if len(supervisors.args) != 1 {
		t.Fatalf("supervisor creations = %d, want 1", len(supervisors.args))
	}
	args := supervisors.args[0]
	if args[0] != host || args[1] != token.FileKey(host) || args[3] != dirs.Recv(args[1]) {
		t.Fatalf("supervisor args = %#v, want host/key/local socket consistency", args)
	}
	if args[2] != filepath.Join("/Users/test-user", ".cache", "agr", "bridge.sock") {
		t.Fatalf("remote socket = %q, want remote home cache socket", args[2])
	}
	if got := remote.ensured(t, host); got != 1 {
		t.Fatalf("EnsureDirs() calls = %d, want 1", got)
	}
	if rows.calls != 1 {
		t.Fatalf("Tree() calls = %d, want 1", rows.calls)
	}
}

func TestDaemonEnsureDirsOncePerHostAcrossRestart(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	host := "host-a"
	remote := &daemonRemote{ensureCalls: map[string]int{}, homeCalls: map[string]int{}, home: map[string]string{host: "/home/a"}}
	supervisors := &daemonSupervisors{}
	store := bindings.New(dirs)
	if err := store.Bind(daemonBinding(t, "row-1", host, "api")); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	daemon := New(daemonConfig(t, dirs, remote, nil, &daemonSink{}, supervisors))
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		if err := daemon.Start(ctx); err != nil {
			t.Fatalf("Start() iteration %d error = %v", i+1, err)
		}
		waitForDaemonPath(t, dirs.Recv(token.FileKey(host)))
		if err := daemon.Close(); err != nil {
			t.Fatalf("Close() iteration %d error = %v", i+1, err)
		}
		cancel()
	}
	if got := remote.ensured(t, host); got != 1 {
		t.Fatalf("EnsureDirs() calls after restart = %d, want 1", got)
	}
}

func TestDaemonSIGTERMRemovesPidfileAndReceiverSocket(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	host := "host-a"
	remote := &daemonRemote{ensureCalls: map[string]int{}, homeCalls: map[string]int{}, home: map[string]string{host: "/home/a"}}
	supervisors := &daemonSupervisors{}
	store := bindings.New(dirs)
	if err := store.Bind(daemonBinding(t, "row-1", host, "api")); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	daemon := New(daemonConfig(t, dirs, remote, nil, &daemonSink{}, supervisors))
	runDone := make(chan error, 1)
	go func() { runDone <- daemon.Run(context.Background()) }()
	waitForDaemonPath(t, dirs.Pid())
	waitForDaemonPath(t, dirs.Recv(token.FileKey(host)))
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() after SIGTERM error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run() did not stop after SIGTERM")
	}
	if _, err := os.Stat(dirs.Pid()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pidfile stat error = %v, want removed", err)
	}
	if _, err := os.Stat(dirs.Recv(token.FileKey(host))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receiver socket stat error = %v, want removed", err)
	}
}

func TestDaemonTreeResultNeverUnbindsBindings(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	host := "host-a"
	store := bindings.New(dirs)
	want := daemonBinding(t, "row-1", host, "api")
	if err := store.Bind(want); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	remote := &daemonRemote{ensureCalls: map[string]int{}, homeCalls: map[string]int{}, home: map[string]string{host: "/home/a"}}
	daemon := New(Config{
		Dirs:        dirs,
		Rows:        &daemonRows{rows: []string{"different-window-row"}},
		Remote:      remote,
		Sink:        &daemonSink{},
		Supervisors: &daemonSupervisors{},
		Bindings:    store,
		Logger:      log.New(io.Discard, "", 0),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := daemon.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = daemon.Close() }()
	got, ok := store.ByRow(want.Row)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("binding after Tree() = %#v, %t; want unchanged %#v, true", got, ok, want)
	}
}

func TestDaemonUnknownTargetUnbindsExactlyThatRow(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	store := bindings.New(dirs)
	first := daemonBinding(t, "row-1", "host-a", "api")
	second := daemonBinding(t, "row-2", "host-b", "web")
	if err := store.Save([]bindings.Binding{first, second}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	daemon := New(Config{Dirs: dirs, Bindings: store, Sink: &daemonSink{err: agterm.ErrUnknownTarget}, Logger: log.New(io.Discard, "", 0)})
	err := daemon.Status(context.Background(), first.Row, agterm.StatusArgs{Status: "active"})
	if !errors.Is(err, agterm.ErrUnknownTarget) {
		t.Fatalf("Status() error = %v, want ErrUnknownTarget", err)
	}
	if _, ok := store.ByRow(first.Row); ok {
		t.Fatal("unknown target row remains bound")
	}
	if got, ok := store.ByRow(second.Row); !ok || !reflect.DeepEqual(got, second) {
		t.Fatalf("unrelated binding = %#v, %t; want %#v, true", got, ok, second)
	}
}

func waitForDaemonPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat %q: %v", path, err)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("path %q was not created", path)
}

func containsText(t *testing.T, err error, want string) bool {
	t.Helper()
	return err != nil && bytes.Contains([]byte(err.Error()), []byte(want))
}
