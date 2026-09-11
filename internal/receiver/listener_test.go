package receiver

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
)

type statusCall struct {
	target string
	args   agterm.StatusArgs
}

type recordingSink struct {
	calls chan statusCall
}

func (s *recordingSink) Status(_ context.Context, target string, args agterm.StatusArgs) error {
	s.calls <- statusCall{target: target, args: args}
	return nil
}

type recordingResolver struct {
	mu      sync.Mutex
	called  int
	binding bindings.Binding
	found   bool
}

func (r *recordingResolver) ByHostName(_ string, _ string) (bindings.Binding, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called++
	return r.binding, r.found
}

func (r *recordingResolver) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.called
}

type recordingLiveness struct {
	calls chan struct{}
}

func (l *recordingLiveness) MarkAlive() {
	l.calls <- struct{}{}
}

func TestListenerRoutesTwoAgrEventsAndUsesBindingPane(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	sink := &recordingSink{calls: make(chan statusCall, 2)}
	resolver := &recordingResolver{
		binding: bindings.Binding{Row: "row-1", Pane: "right", PaneID: "pane-1"},
		found:   true,
	}
	liveness := &recordingLiveness{calls: make(chan struct{}, 2)}
	listener := NewListener("host-a", "host-a", dirs, sink, resolver, liveness)
	stop := startListener(t, listener, dirs.Recv("host-a"))
	defer stop()

	writeConnection(t, dirs.Recv("host-a"),
		`{"cmd":"session-status","session":"api","state":"active","pane":"left","pane_id":"stale","args":["--blink"]}`+"\n"+
			`{"cmd":"session-status","session":"api","state":"completed","args":["--auto-reset"]}`+"\n")

	first := receiveCall(t, sink.calls)
	second := receiveCall(t, sink.calls)
	if first.target != "row-1" || second.target != "row-1" {
		t.Fatalf("status targets = %q, %q; want row-1 for both", first.target, second.target)
	}
	if first.args.Status != "active" || first.args.Pane != "right" || first.args.PaneID != "pane-1" || !boolValue(t, first.args.Blink) {
		t.Fatalf("first status args = %#v, want active/right/pane-1/blink", first.args)
	}
	if second.args.Status != "completed" || second.args.Pane != "right" || second.args.PaneID != "pane-1" || !boolValue(t, second.args.AutoReset) {
		t.Fatalf("second status args = %#v, want completed/right/pane-1/auto-reset", second.args)
	}
	for range 2 {
		select {
		case <-liveness.calls:
		case <-time.After(2 * time.Second):
			t.Fatal("MarkAlive was not called for both events")
		}
	}
	if got := resolver.calls(); got != 2 {
		t.Fatalf("ByHostName calls = %d, want 2", got)
	}
	if listener.LastEvent().IsZero() {
		t.Fatal("LastEvent() is zero after delivered events")
	}
}

func TestListenerRoutesCookbookEventWithoutBindingLookup(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	sink := &recordingSink{calls: make(chan statusCall, 1)}
	resolver := &recordingResolver{}
	liveness := &recordingLiveness{calls: make(chan struct{}, 1)}
	listener := NewListener("host-a", "host-a", dirs, sink, resolver, liveness)
	stop := startListener(t, listener, dirs.Recv("host-a"))
	defer stop()

	writeConnection(t, dirs.Recv("host-a"), `{"cmd":"session-status","state":"blocked","session_id":"row-42","pane":"scratch","pane_id":"surface-42","args":["--blink","--auto-reset"]}`+"\n")
	call := receiveCall(t, sink.calls)
	if call.target != "row-42" {
		t.Fatalf("cookbook target = %q, want row-42", call.target)
	}
	if call.args.Status != "blocked" || call.args.Pane != "scratch" || call.args.PaneID != "surface-42" || !boolValue(t, call.args.Blink) || !boolValue(t, call.args.AutoReset) {
		t.Fatalf("cookbook args = %#v, want blocked/scratch/surface-42/blink/auto-reset", call.args)
	}
	if got := resolver.calls(); got != 0 {
		t.Fatalf("ByHostName calls = %d, want 0 for cookbook event", got)
	}
	select {
	case <-liveness.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("MarkAlive was not called for cookbook event")
	}
}

func TestListenerMalformedAndUnknownEventsDoNotBreakConnection(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	sink := &recordingSink{calls: make(chan statusCall, 2)}
	resolver := &recordingResolver{binding: bindings.Binding{Row: "row-1"}, found: true}
	listener := NewListener("host-a", "host-a", dirs, sink, resolver, nil)
	stop := startListener(t, listener, dirs.Recv("host-a"))
	defer stop()

	writeConnection(t, dirs.Recv("host-a"),
		`{"cmd":"session-status","session":"api","state":"active"}`+"\n"+
			`not-json`+"\n"+
			`{"cmd":"session-status","session":"api","state":"completed"}`+"\n")
	if first := receiveCall(t, sink.calls); first.args.Status != "active" {
		t.Fatalf("first delivered state = %q, want active", first.args.Status)
	}
	if second := receiveCall(t, sink.calls); second.args.Status != "completed" {
		t.Fatalf("second delivered state = %q, want completed", second.args.Status)
	}
}

func TestListenerUnknownSessionIsLoggedAndNotPushed(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	sink := &recordingSink{calls: make(chan statusCall, 1)}
	listener := NewListener("host-a", "host-a", dirs, sink, &recordingResolver{}, nil)
	stop := startListener(t, listener, dirs.Recv("host-a"))
	defer stop()

	writeConnection(t, dirs.Recv("host-a"), `{"cmd":"session-status","session":"missing","state":"active"}`+"\n")
	select {
	case call := <-sink.calls:
		t.Fatalf("unexpected status call %#v", call)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestListenerSkipsOversizedLineAndReadsNextEvent(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	sink := &recordingSink{calls: make(chan statusCall, 1)}
	resolver := &recordingResolver{binding: bindings.Binding{Row: "row-1"}, found: true}
	listener := NewListener("host-a", "host-a", dirs, sink, resolver, nil)
	stop := startListener(t, listener, dirs.Recv("host-a"))
	defer stop()

	oversized := make([]byte, maxEventLine+1)
	for i := range oversized {
		oversized[i] = 'x'
	}
	writeConnection(t, dirs.Recv("host-a"), string(oversized)+"\n"+`{"cmd":"session-status","session":"api","state":"active"}`+"\n")
	call := receiveCall(t, sink.calls)
	if call.target != "row-1" || call.args.Status != "active" {
		t.Fatalf("post-oversize status = %#v, want row-1/active", call)
	}
}

func startListener(t *testing.T, listener *Listener, path string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- listener.Serve(ctx) }()
	waitForSocket(t, path)
	return func() {
		t.Helper()
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Serve() did not stop after context cancellation")
		}
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat listener socket: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("listener socket %q was not created", path)
}

func writeConnection(t *testing.T, path, body string) {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial receiver socket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(body)); err != nil {
		t.Fatalf("write receiver events: %v", err)
	}
}

func receiveCall(t *testing.T, calls <-chan statusCall) statusCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for status call")
		return statusCall{}
	}
}

func boolValue(t *testing.T, value *bool) bool {
	t.Helper()
	return value != nil && *value
}

// blockingSink holds every Status call until released, standing in for a
// handler wedged on a slow agterm socket.
type blockingSink struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockingSink) Status(_ context.Context, _ string, _ agterm.StatusArgs) error {
	s.entered <- struct{}{}
	<-s.release
	return nil
}

// TestServeLateExitDoesNotUnlinkSuccessorSocket pins the ownership rule: a
// listener whose Serve is still draining a wedged handler after cancellation
// must not remove the socket path, because by then a successor may have bound
// it. Before the rule, Serve's deferred os.Remove (and Go's unlink-on-close)
// took the successor's socket with it — a host with a live tunnel and no
// reachable receiver.
func TestServeLateExitDoesNotUnlinkSuccessorSocket(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	path := dirs.Recv("host-a")
	sink := &blockingSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
	first := NewListener("host-a", "host-a", dirs, sink, &recordingResolver{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- first.Serve(ctx) }()
	waitForSocket(t, path)

	// Wedge one handler inside the sink, then cancel: the first listener is
	// closed but Serve cannot return until the handler does.
	writeConnection(t, path, `{"cmd":"session-status","state":"active","session_id":"row-1"}`+"\n")
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never reached the sink")
	}
	cancel()

	// A successor binds the same path while the first Serve is still alive.
	second := NewListener("host-a", "host-a", dirs, &recordingSink{calls: make(chan statusCall, 1)}, &recordingResolver{}, nil)
	if err := second.Bind(); err != nil {
		t.Fatalf("successor Bind() error = %v", err)
	}
	defer second.closeResources()

	// Let the wedged handler go; the first Serve now exits late.
	close(sink.release)
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("first Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first Serve() did not return after its handler was released")
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("successor socket gone after the predecessor's late exit: %v", err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("successor socket not reachable after the predecessor's late exit: %v", err)
	}
	_ = conn.Close()
}
