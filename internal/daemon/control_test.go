package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/bridge"
	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

type task10Trace struct {
	mu     sync.Mutex
	values []string
}

func newTask10Trace(t *testing.T) *task10Trace {
	t.Helper()
	return &task10Trace{}
}

func (r *task10Trace) add(value string) {
	r.mu.Lock()
	r.values = append(r.values, value)
	r.mu.Unlock()
}

func (r *task10Trace) snapshot(t *testing.T) []string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.values...)
}

type task10Remote struct {
	mu        sync.Mutex
	home      map[string]string
	sessions  map[string][]remote.Session
	ensured   map[string]int
	trace     *task10Trace
	sessionCh chan string
}

func newTask10Remote(t *testing.T, trace *task10Trace) *task10Remote {
	t.Helper()
	return &task10Remote{
		home:      make(map[string]string),
		sessions:  make(map[string][]remote.Session),
		ensured:   make(map[string]int),
		trace:     trace,
		sessionCh: make(chan string, 16),
	}
}

func (r *task10Remote) Home(_ context.Context, host string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if home := r.home[host]; home != "" {
		return home, nil
	}
	return "/home/test", nil
}

func (r *task10Remote) Sessions(_ context.Context, host string) ([]remote.Session, error) {
	if r.trace != nil {
		r.trace.add("sessions")
	}
	select {
	case r.sessionCh <- host:
	default:
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]remote.Session(nil), r.sessions[host]...), nil
}

func (r *task10Remote) EnsureDirs(_ context.Context, host string) error {
	r.mu.Lock()
	r.ensured[host]++
	r.mu.Unlock()
	return nil
}

type task10Supervisor struct {
	changes  chan bridge.State
	stop     chan struct{}
	stopOnce sync.Once
}

func (s *task10Supervisor) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return nil
	case <-s.stop:
		return nil
	}
}

func (s *task10Supervisor) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *task10Supervisor) Changes() <-chan bridge.State { return s.changes }

type task10Supervisors struct {
	mu      sync.Mutex
	created []*task10Supervisor
}

func (f *task10Supervisors) New(string, string, string, string) Supervisor {
	f.mu.Lock()
	defer f.mu.Unlock()
	supervisor := newTask10SupervisorForFactory()
	f.created = append(f.created, supervisor)
	return supervisor
}

func newTask10SupervisorForFactory() *task10Supervisor {
	return &task10Supervisor{changes: make(chan bridge.State, 8), stop: make(chan struct{})}
}

func (f *task10Supervisors) latest(t *testing.T) *task10Supervisor {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.created) == 0 {
		t.Fatal("supervisor factory has not created a supervisor")
	}
	return f.created[len(f.created)-1]
}

type task10UICall struct {
	kind string
	row  string
	msg  string
}

type task10UI struct {
	calls chan task10UICall
	trace *task10Trace
}

func newTask10UI(t *testing.T, trace *task10Trace) *task10UI {
	t.Helper()
	return &task10UI{calls: make(chan task10UICall, 32), trace: trace}
}

func (u *task10UI) HudOpen(_ context.Context, row, message string) error {
	if u.trace != nil {
		u.trace.add("hud-open " + row)
	}
	u.calls <- task10UICall{kind: "open", row: row, msg: message}
	return nil
}

func (u *task10UI) HudClose(_ context.Context, row string) error {
	if u.trace != nil {
		u.trace.add("hud-close " + row)
	}
	u.calls <- task10UICall{kind: "close", row: row}
	return nil
}

type task10StatusCall struct {
	target string
	args   agterm.StatusArgs
}

type task10Sink struct {
	mu        sync.Mutex
	calls     []task10StatusCall
	callCh    chan task10StatusCall
	trace     *task10Trace
	firstErr  error
	firstDone bool
	errByRow  map[string]error
}

func newTask10Sink(t *testing.T, trace *task10Trace) *task10Sink {
	t.Helper()
	return &task10Sink{callCh: make(chan task10StatusCall, 32), trace: trace, errByRow: make(map[string]error)}
}

func (s *task10Sink) Status(_ context.Context, target string, args agterm.StatusArgs) error {
	call := task10StatusCall{target: target, args: args}
	s.mu.Lock()
	s.calls = append(s.calls, call)
	err := s.errByRow[target]
	if err == nil && !s.firstDone && s.firstErr != nil {
		err = s.firstErr
		s.firstDone = true
	}
	s.mu.Unlock()
	if s.trace != nil {
		s.trace.add("status " + target)
	}
	s.callCh <- call
	return err
}

type task10Events struct {
	mu      sync.Mutex
	calls   int
	streams []chan string
}

func (e *task10Events) ClosedRows(context.Context) (<-chan string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if len(e.streams) == 0 {
		return make(chan string), nil
	}
	index := e.calls - 1
	if index >= len(e.streams) {
		index = len(e.streams) - 1
	}
	return e.streams[index], nil
}

func (e *task10Events) count(t *testing.T) int {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func task10Config(t *testing.T, dirs paths.Dirs, remoteDep Remote, ui UI, events EventSource, sink StatusSink, supervisors Supervisors, store *bindings.Store) Config {
	t.Helper()
	return Config{
		Dirs: dirs, Remote: remoteDep, UI: ui, Events: events, Sink: sink,
		Supervisors: supervisors, Bindings: store, Logger: log.New(io.Discard, "", 0),
	}
}

func controlExchange(t *testing.T, path string, request controlRequest) controlResponse {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial daemon control socket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal control request: %v", err)
	}
	if _, err := fmt.Fprintf(conn, "%s\n", data); err != nil {
		t.Fatalf("write control request: %v", err)
	}
	var response controlResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatalf("decode control response: %v", err)
	}
	return response
}

func waitForTask10(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func waitForTask10Call(t *testing.T, calls <-chan task10UICall, kind, row string) task10UICall {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case call := <-calls:
			if call.kind == kind && call.row == row {
				return call
			}
		case <-deadline:
			t.Fatalf("timed out waiting for HUD %s %s", kind, row)
		}
	}
}

func TestDaemonControlRoundTripAndInvalidHosts(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	r := newTask10Remote(t, nil)
	factory := &task10Supervisors{}
	d := New(task10Config(t, dirs, r, nil, nil, nil, factory, nil))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()

	up := controlExchange(t, dirs.Sock(), controlRequest{Op: "up", Host: "host-a"})
	if !up.OK {
		t.Fatalf("up response = %#v, want ok", up)
	}
	if got, ok := up.Result.(map[string]any); !ok || got["host"] != "host-a" || got["state"] != "down" {
		t.Fatalf("up result = %#v, want host-a/down status", up.Result)
	}
	status := controlExchange(t, dirs.Sock(), controlRequest{Op: "status", Host: "host-a"})
	if !status.OK {
		t.Fatalf("status response = %#v, want ok", status)
	}
	down := controlExchange(t, dirs.Sock(), controlRequest{Op: "down", Host: "host-a"})
	if !down.OK {
		t.Fatalf("down response = %#v, want ok", down)
	}
	reload := controlExchange(t, dirs.Sock(), controlRequest{Op: "reload-bindings"})
	if !reload.OK {
		t.Fatalf("reload-bindings response = %#v, want ok", reload)
	}

	for _, op := range []string{"up", "down"} {
		response := controlExchange(t, dirs.Sock(), controlRequest{Op: op, Host: "-bad"})
		if response.OK || response.Error == "" || !containsText(t, errors.New(response.Error), "invalid host") {
			t.Fatalf("%s invalid-host response = %#v, want rejection", op, response)
		}
	}
}

func TestDaemonControlMalformedLineGetsErrorResponse(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	d := New(task10Config(t, dirs, nil, nil, nil, nil, nil, nil))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()
	conn, err := net.Dial("unix", dirs.Sock())
	if err != nil {
		t.Fatalf("dial daemon control socket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, "not-json\n"); err != nil {
		t.Fatalf("write malformed request: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read malformed response: %v", err)
	}
	var response controlResponse
	if err := json.Unmarshal(bytesTrimSpace(line), &response); err != nil {
		t.Fatalf("decode malformed response: %v", err)
	}
	if response.OK || response.Error == "" {
		t.Fatalf("malformed response = %#v, want error", response)
	}
}

func TestDaemonControlUsesListenCleanForLeftoverSocket(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	if err := os.WriteFile(dirs.Sock(), []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale control socket: %v", err)
	}
	d := New(task10Config(t, dirs, nil, nil, nil, nil, nil, nil))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() with stale control socket error = %v", err)
	}
	defer func() { _ = d.Close() }()
	if _, err := os.Stat(dirs.Sock()); err != nil {
		t.Fatalf("control socket after Start(): %v", err)
	}
}
