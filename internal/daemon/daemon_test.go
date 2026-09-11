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
	"strings"
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

// TestStopHostHoldsSlotUntilTeardownCompletes pins the lifecycle invariant the
// loop kept breaking: the host slot must stay claimed for the WHOLE of a
// teardown. Releasing it early let a concurrent start bind a replacement
// receiver socket at the same path, which the finishing teardown then
// unlinked — leaving that host with a live tunnel and nothing listening.
func TestStopHostHoldsSlotUntilTeardownCompletes(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	r := newTask10Remote(t, nil)
	d := New(task10Config(t, dirs, r, nil, nil, nil, &task10Supervisors{exitDelay: 300 * time.Millisecond}, nil))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()

	ctx := context.Background()
	if err := d.startHost(ctx, "host-a"); err != nil {
		t.Fatalf("startHost() error = %v", err)
	}
	sock := dirs.Recv(token.FileKey("host-a"))
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("receiver socket missing after start: %v", err)
	}

	// Place the start INSIDE the teardown window rather than hoping to land
	// there: the supervisor takes 300ms to exit, so stopHost is still waiting
	// when the start runs.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); d.stopHost("host-a") }()
	time.Sleep(60 * time.Millisecond)
	if err := d.startHost(ctx, "host-a"); err != nil {
		t.Fatalf("startHost() during teardown error = %v", err)
	}
	wg.Wait()

	// The restarted host must still own a socket on disk. With the slot
	// released early, the finishing teardown unlinks the replacement the
	// start just bound, and the host runs with nothing listening.
	d.mu.Lock()
	_, running := d.hosts["host-a"]
	d.mu.Unlock()
	if !running {
		t.Fatal("host is not registered after a start during teardown")
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("host is running but its receiver socket was unlinked: %v", err)
	}
}

// TestStopHostIsIdempotentAndConcurrencySafe covers a second stop arriving
// while the first is still tearing down: it must wait, not double-free.
func TestStopHostIsIdempotentAndConcurrencySafe(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	r := newTask10Remote(t, nil)
	d := New(task10Config(t, dirs, r, nil, nil, nil, &task10Supervisors{}, nil))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()

	if err := d.startHost(context.Background(), "host-b"); err != nil {
		t.Fatalf("startHost() error = %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); d.stopHost("host-b") }()
	}
	wg.Wait()

	d.mu.Lock()
	_, running := d.hosts["host-b"]
	d.mu.Unlock()
	if running {
		t.Fatal("host still registered after stopHost returned")
	}
	if _, err := os.Stat(dirs.Recv(token.FileKey("host-b"))); !os.IsNotExist(err) {
		t.Fatalf("receiver socket still present after teardown: %v", err)
	}
}

// TestBindFailureRacingStopHostUnwindsCleanly covers the shape behind the
// eighth review round's critical: a runtime that is published but never gets
// to run its goroutines, with stopHost racing it. The bind now happens under
// d.mu, so no teardown can claim the runtime mid-bind; what remains to hold
// is that the failed start unwinds every reservation it made — the host slot,
// runtime.done, d.children, torndown — so concurrent stops return, a restart
// does not block on a torndown that never closes, and Close does not hang on
// children that never launched.
func TestBindFailureRacingStopHostUnwindsCleanly(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	r := newTask10Remote(t, nil)
	d := New(task10Config(t, dirs, r, nil, nil, nil, &task10Supervisors{}, nil))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()

	// A NON-EMPTY directory at the socket path makes Bind fail
	// deterministically: ListenClean reclaims a stale path with os.Remove,
	// which succeeds on an empty directory but not on one with contents.
	sock := dirs.Recv(token.FileKey("host-x"))
	if err := os.MkdirAll(sock, 0o700); err != nil {
		t.Fatalf("occupy socket path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sock, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatalf("occupy socket path: %v", err)
	}

	var stops sync.WaitGroup
	for i := 0; i < 3; i++ {
		stops.Add(1)
		go func() { defer stops.Done(); d.stopHost("host-x") }()
	}
	if err := d.startHost(context.Background(), "host-x"); err == nil {
		t.Fatal("startHost() error = nil, want bind failure")
	}
	stopped := make(chan struct{})
	go func() { stops.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("stopHost did not return after a failed bind")
	}

	d.mu.Lock()
	_, running := d.hosts["host-x"]
	d.mu.Unlock()
	if running {
		t.Fatal("host still registered after a failed bind")
	}
	// A restart must not wait on a torndown the failed start never closed.
	_ = os.RemoveAll(sock)
	if err := d.startHost(context.Background(), "host-x"); err != nil {
		t.Fatalf("restart after failed bind error = %v", err)
	}
	// Close must not wait on children the failed start reserved but never
	// launched.
	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close() hung: a failed start left children reserved")
	}
}

// TestCloseIsBoundedByAWedgedChild pins the shutdown bound: a host goroutine
// that ignores cancellation must not hold Close open. Close returns within the
// teardown timeout, reports it as an error, and releases the daemon socket and
// pid file — but NOT the lock, which fences a successor off shared state until
// the wedged goroutines are actually gone; a later Close releases it once the
// children have drained.
func TestCloseIsBoundedByAWedgedChild(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	r := newTask10Remote(t, nil)
	config := task10Config(t, dirs, r, nil, nil, nil, &task10Supervisors{exitDelay: time.Second}, nil)
	config.TeardownTimeout = 200 * time.Millisecond
	d := New(config)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := d.startHost(context.Background(), "host-a"); err != nil {
		t.Fatalf("startHost() error = %v", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()
	select {
	case err := <-closed:
		if err == nil || !strings.Contains(err.Error(), "did not stop within") {
			t.Fatalf("Close() error = %v, want the teardown timeout reported", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close() hung on a wedged child")
	}
	for _, path := range []string{dirs.Sock(), dirs.Pid()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still present after a timed-out Close: %v", path, err)
		}
	}

	// The wedged goroutine is still alive, so the lock must still fence a
	// successor off the shared state it may yet write.
	fresh := New(task10Config(t, dirs, newTask10Remote(t, nil), nil, nil, nil, &task10Supervisors{}, nil))
	if err := fresh.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "already running") {
		_ = fresh.Close()
		t.Fatalf("fresh Start() while the old daemon is not quiescent: error = %v, want refused as already running", err)
	}

	// Once the children drain, a later Close finishes the job and a
	// successor can start.
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := d.Close()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Close() after the wedge cleared still failed: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := fresh.Start(context.Background()); err != nil {
		t.Fatalf("fresh Start() after the old daemon became quiescent: %v", err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatalf("fresh Close() error = %v", err)
	}
	// The same instance restarts too: the retried Closes above must have
	// shared ONE waiter on d.children, or a leftover Wait would now be
	// racing this Start's Add on a reused WaitGroup.
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("same-instance Start() after a timed-out Close: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("same-instance Close() error = %v", err)
	}
}
