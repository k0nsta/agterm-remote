package daemon

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/agterm/agtermtest"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

type task10RecoverySink struct {
	mu          sync.Mutex
	versionCall int
	statusCalls []string
	firstRow    string
	firstDone   bool
	versionCh   chan struct{}
}

func (s *task10RecoverySink) Version(context.Context) (string, error) {
	s.mu.Lock()
	s.versionCall++
	s.mu.Unlock()
	select {
	case s.versionCh <- struct{}{}:
	default:
	}
	return "0.26.0", nil
}

func (s *task10RecoverySink) Status(_ context.Context, target string, _ agterm.StatusArgs) error {
	s.mu.Lock()
	s.statusCalls = append(s.statusCalls, target)
	if !s.firstDone && target == s.firstRow {
		s.firstDone = true
		s.mu.Unlock()
		return syscall.ECONNREFUSED
	}
	s.mu.Unlock()
	if target == "row-dead" {
		return agterm.ErrUnknownTarget
	}
	return nil
}

func (s *task10RecoverySink) versions(t *testing.T) int {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.versionCall
}

func TestDaemonAgtermRecoveryLogsOnceResubscribesAndResyncs(t *testing.T) {
	t.Helper()
	oldInterval := agtermRecoveryInterval
	agtermRecoveryInterval = 10 * time.Millisecond
	t.Cleanup(func() { agtermRecoveryInterval = oldInterval })

	dirs := pathstest.Dirs(t)
	missingSocket := dirs.Cache + "/agterm-not-started.sock"
	t.Setenv("AGTERM_CONTROL_SOCKET", missingSocket)
	trace := newTask10Trace(t)
	r := newTask10Remote(t, trace)
	host := "host-a"
	r.sessions[host] = []remote.Session{{Name: "api", State: "active"}, {Name: "gone", State: "completed"}}
	store := bindings.New(dirs)
	live := daemonBinding(t, "row-live", host, "api")
	dead := daemonBinding(t, "row-dead", host, "gone")
	if err := store.Save([]bindings.Binding{live, dead}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	events := &task10Events{streams: []chan string{make(chan string), make(chan string)}}
	factory := &task10Supervisors{}
	sink := &task10RecoverySink{firstRow: live.Row, versionCh: make(chan struct{}, 8)}
	var logs strings.Builder
	d := New(Config{
		Dirs: dirs, Remote: r, Events: events, Sink: sink, Supervisors: factory, Bindings: store,
		Logger: log.New(&logs, "", 0),
	})
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()
	factory.latest(t)
	d.mu.Lock()
	d.hosts[host].state = "up"
	d.mu.Unlock()
	if err := d.Status(context.Background(), live.Row, agterm.StatusArgs{Status: "active"}); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("initial Status() error = %v, want connection refused", err)
	}
	fakeSocket := agtermtest.NewFakeAgterm(t, func(agterm.Request) agterm.Response {
		return agterm.Response{OK: true}
	})
	t.Setenv("AGTERM_CONTROL_SOCKET", fakeSocket)
	waitForTask10(t, func() bool { return sink.versions(t) >= 2 })
	waitForTask10(t, func() bool {
		return events.count(t) >= 2
	})
	waitForTask10(t, func() bool {
		_, ok := store.ByRow(dead.Row)
		return !ok
	})
	if _, ok := store.ByRow(live.Row); !ok {
		t.Fatal("live row was removed during recovery")
	}
	if count := strings.Count(logs.String(), "agterm unavailable"); count != 1 {
		t.Fatalf("agterm unavailable log count = %d, want 1; logs=%q", count, logs.String())
	}
}

func TestDaemonClosedRowUnbindsAndStopsLastHost(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	host := "host-a"
	store := bindings.New(dirs)
	binding := daemonBinding(t, "row-1", host, "api")
	if err := store.Bind(binding); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	r := newTask10Remote(t, nil)
	r.home[host] = "/home/test"
	events := &task10Events{streams: []chan string{make(chan string, 1)}}
	factory := &task10Supervisors{}
	d := New(task10Config(t, dirs, r, nil, events, nil, factory, store))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()
	events.streams[0] <- binding.Row
	waitForTask10(t, func() bool {
		_, ok := store.ByRow(binding.Row)
		return !ok
	})
	supervisor := factory.latest(t)
	select {
	case <-supervisor.stop:
	case <-time.After(2 * time.Second):
		t.Fatal("last binding close did not stop the supervisor")
	}
	waitForTask10(t, func() bool {
		_, err := os.Stat(dirs.Recv(binding.Host))
		return errors.Is(err, os.ErrNotExist)
	})
}

func TestDaemonClosedRowLeavesHostRunningWhileAnotherBindingRemains(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	host := "host-a"
	store := bindings.New(dirs)
	first := daemonBinding(t, "row-1", host, "api")
	second := daemonBinding(t, "row-2", host, "worker")
	if err := store.Save([]bindings.Binding{first, second}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	r := newTask10Remote(t, nil)
	events := &task10Events{streams: []chan string{make(chan string, 2)}}
	factory := &task10Supervisors{}
	d := New(task10Config(t, dirs, r, nil, events, nil, factory, store))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()
	events.streams[0] <- first.Row
	waitForTask10(t, func() bool {
		_, ok := store.ByRow(first.Row)
		return !ok
	})
	select {
	case <-factory.latest(t).stop:
		t.Fatal("host stopped while another binding remained")
	case <-time.After(100 * time.Millisecond):
	}
	if _, ok := store.ByRow(second.Row); !ok {
		t.Fatal("remaining binding was removed")
	}
}

func TestDaemonClosedSplitRowUnbindsBothPanesAndKeepsBusyHost(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	host := "host-a"
	store := bindings.New(dirs)
	left := daemonBinding(t, "row-1", host, "api")
	right := daemonBinding(t, "row-1", host, "worker")
	right.Pane = "right"
	right.PaneID = "pane-row-1-right"
	other := daemonBinding(t, "row-2", host, "infra")
	for _, item := range []bindings.Binding{left, right, other} {
		if err := store.Bind(item); err != nil {
			t.Fatalf("Bind(%#v) error = %v", item, err)
		}
	}
	if got := store.ForRow("row-1"); len(got) != 2 {
		t.Fatalf("ForRow(row-1) = %#v, want both panes bound", got)
	}
	r := newTask10Remote(t, nil)
	events := &task10Events{streams: []chan string{make(chan string, 1)}}
	factory := &task10Supervisors{}
	d := New(task10Config(t, dirs, r, nil, events, nil, factory, store))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()
	events.streams[0] <- "row-1"
	waitForTask10(t, func() bool { return len(store.ForRow("row-1")) == 0 })
	select {
	case <-factory.latest(t).stop:
		t.Fatal("host stopped while row-2 was still bound to it")
	case <-time.After(100 * time.Millisecond):
	}
	if _, ok := store.ByHostName(host, other.Name); !ok {
		t.Fatal("binding in another row was removed")
	}
}

func TestDaemonClosedSplitRowStopsEveryHostItWasLastBoundTo(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	store := bindings.New(dirs)
	left := daemonBinding(t, "row-1", "host-a", "api")
	right := daemonBinding(t, "row-1", "host-b", "api")
	right.Pane = "right"
	right.PaneID = "pane-row-1-right"
	for _, item := range []bindings.Binding{left, right} {
		if err := store.Bind(item); err != nil {
			t.Fatalf("Bind(%#v) error = %v", item, err)
		}
	}
	r := newTask10Remote(t, nil)
	r.home["host-a"] = "/home/test"
	r.home["host-b"] = "/home/test"
	events := &task10Events{streams: []chan string{make(chan string, 1)}}
	factory := &task10Supervisors{}
	d := New(task10Config(t, dirs, r, nil, events, nil, factory, store))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()
	waitForTask10(t, func() bool {
		factory.mu.Lock()
		defer factory.mu.Unlock()
		return len(factory.created) == 2
	})
	events.streams[0] <- "row-1"
	factory.mu.Lock()
	created := append([]*task10Supervisor(nil), factory.created...)
	factory.mu.Unlock()
	for i, supervisor := range created {
		select {
		case <-supervisor.stop:
		case <-time.After(2 * time.Second):
			t.Fatalf("supervisor %d was not stopped after its last binding's row closed", i)
		}
	}
}
