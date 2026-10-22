package receiver

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
	"github.com/k0nsta/agterm-remote/internal/proxy"
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
	listener := NewListener("host-a", "host-a", dirs, sink, resolver, liveness, nil)
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
	listener := NewListener("host-a", "host-a", dirs, sink, resolver, liveness, nil)
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
	listener := NewListener("host-a", "host-a", dirs, sink, resolver, nil, nil)
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
	listener := NewListener("host-a", "host-a", dirs, sink, &recordingResolver{}, nil, nil)
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
	listener := NewListener("host-a", "host-a", dirs, sink, resolver, nil, nil)
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
	conn, err := dialSocket(path)
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
	first := NewListener("host-a", "host-a", dirs, sink, &recordingResolver{}, nil, nil)

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
	// Cancellation closes the first listener from a goroutine; wait until the
	// path stops accepting (ECONNREFUSED on a node that is still there) before
	// the successor probes it, or ListenClean rightly reports it in use.
	deadline := time.Now().Add(2 * time.Second)
	for {
		probe, dialErr := net.Dial("unix", path)
		if dialErr != nil {
			break
		}
		_ = probe.Close()
		if time.Now().After(deadline) {
			t.Fatal("first listener never stopped accepting after cancellation")
		}
		time.Sleep(time.Millisecond)
	}

	// A successor binds the same path while the first Serve is still alive
	// (its handler is still wedged in the sink).
	second := NewListener("host-a", "host-a", dirs, &recordingSink{calls: make(chan statusCall, 1)}, &recordingResolver{}, nil, nil)
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

type ctlCall struct {
	req proxy.Request
}

// funcCtl adapts a function to CtlSink and records every request.
type funcCtl struct {
	calls chan ctlCall
	run   func(ctx context.Context, req proxy.Request, frames proxy.FrameWriter) int
}

func (c *funcCtl) Ctl(ctx context.Context, req proxy.Request, frames proxy.FrameWriter) int {
	c.calls <- ctlCall{req: req}
	return c.run(ctx, req, frames)
}

func newFuncCtl(run func(ctx context.Context, req proxy.Request, frames proxy.FrameWriter) int) *funcCtl {
	return &funcCtl{calls: make(chan ctlCall, 4), run: run}
}

func ctlHeader(session string, files []int, stdin int) string {
	parts := make([]string, len(files))
	for i, size := range files {
		parts[i] = strconv.Itoa(size)
	}
	return `{"cmd":"ctl","v":1,"session":"` + session + `","self":{"row":"r","pane_id":"p"},"cwd":"/w","argv":["tree"],"files":[` +
		strings.Join(parts, ",") + `],"stdin":` + strconv.Itoa(stdin) + "}\n"
}

func dialCtl(t *testing.T, path string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := dialSocket(path)
	if err != nil {
		t.Fatalf("dial receiver socket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, bufio.NewReader(conn)
}

func readLine(t *testing.T, reader *bufio.Reader, conn net.Conn) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read reply line: %v (got %q)", err, line)
	}
	return line
}

// readAll returns everything up to the server closing the connection.
func readAll(t *testing.T, reader *bufio.Reader, conn net.Conn) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read reply: %v (got %q)", err, data)
	}
	return string(data)
}

func ctlListener(t *testing.T, sink CtlSink, liveness Liveness, found bool) (*Listener, string, func()) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	resolver := &recordingResolver{binding: bindings.Binding{Row: "row-1", Pane: "left", PaneID: "pane-1", Host: "host-a", Name: "api"}, found: found}
	listener := NewListener("host-a", "host-a", dirs, &recordingSink{calls: make(chan statusCall, 4)}, resolver, liveness, sink)
	path := dirs.Recv("host-a")
	return listener, path, startListener(t, listener, path)
}

func TestListenerCtlAfterStatusLinesOnOneConnection(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	status := &recordingSink{calls: make(chan statusCall, 2)}
	resolver := &recordingResolver{binding: bindings.Binding{Row: "row-1", Pane: "left", PaneID: "pane-1"}, found: true}
	ctl := newFuncCtl(func(_ context.Context, _ proxy.Request, frames proxy.FrameWriter) int {
		_, _ = frames.Stdout().Write([]byte("hi"))
		return 7
	})
	listener := NewListener("host-a", "host-a", dirs, status, resolver, nil, ctl)
	stop := startListener(t, listener, dirs.Recv("host-a"))
	defer stop()

	conn, reader := dialCtl(t, dirs.Recv("host-a"))
	_, err := io.WriteString(conn, `{"cmd":"session-status","session":"api","state":"active"}`+"\n"+ctlHeader("api", nil, 0))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := receiveCall(t, status.calls); got.args.Status != "active" {
		t.Fatalf("status = %#v", got)
	}
	if got := readLine(t, reader, conn); got != "ack 1\n" {
		t.Fatalf("first reply = %q, want ack 1", got)
	}
	if got := readAll(t, reader, conn); got != "o 2\nhix 7\n" {
		t.Fatalf("frames = %q", got)
	}
	call := <-ctl.calls
	if call.req.Host != "host-a" || call.req.Caller.Row != "row-1" || call.req.Caller.PaneID != "pane-1" ||
		call.req.Session != "api" || call.req.CWD != "/w" || call.req.Self.PaneID != "p" || len(call.req.Argv) != 1 {
		t.Fatalf("request = %#v", call.req)
	}
}

func TestListenerCtlAcksBeforeBodyAndReadsLargeBody(t *testing.T) {
	t.Helper()
	ctl := newFuncCtl(func(context.Context, proxy.Request, proxy.FrameWriter) int { return 0 })
	_, path, stop := ctlListener(t, ctl, nil, true)
	defer stop()

	conn, reader := dialCtl(t, path)
	file := bytes.Repeat([]byte("f"), 10000)
	stdin := bytes.Repeat([]byte("s"), 5000)
	if _, err := io.WriteString(conn, ctlHeader("api", []int{len(file)}, len(stdin))); err != nil {
		t.Fatalf("write header: %v", err)
	}
	// The ack arrives with no body sent yet.
	if got := readLine(t, reader, conn); got != "ack 1\n" {
		t.Fatalf("reply = %q, want ack 1", got)
	}
	select {
	case <-ctl.calls:
		t.Fatal("sink called before the body arrived")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := conn.Write(append(file, stdin...)); err != nil {
		t.Fatalf("write body: %v", err)
	}
	call := <-ctl.calls
	if len(call.req.Files) != 1 || !bytes.Equal(call.req.Files[0], file) || !bytes.Equal(call.req.Stdin, stdin) {
		t.Fatalf("body = %d files, stdin %d bytes", len(call.req.Files), len(call.req.Stdin))
	}
	if got := readAll(t, reader, conn); got != "x 0\n" {
		t.Fatalf("frames = %q", got)
	}
}

func TestListenerCtlRefusals(t *testing.T) {
	t.Helper()
	tests := []struct {
		name   string
		header string
		found  bool
		nilCtl bool
		want   string
	}{
		{name: "session_id", header: `{"cmd":"ctl","v":1,"session":"api","session_id":"row"}` + "\n", found: true, want: "session_id"},
		{name: "unknown session", header: ctlHeader("api", nil, 0), found: false, want: "no binding for session api"},
		{name: "too many files", header: ctlHeader("api", []int{1, 1, 1, 1, 1, 1, 1, 1, 1}, 0), found: true, want: "too many files"},
		{name: "stdin limit", header: ctlHeader("api", nil, 2<<20), found: true, want: "stdin"},
		{name: "files limit", header: ctlHeader("api", []int{6 << 20, 6 << 20}, 0), found: true, want: "exceed"},
		{name: "nil sink", header: ctlHeader("api", nil, 0), found: true, nilCtl: true, want: "not available"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Helper()
			ctl := newFuncCtl(func(context.Context, proxy.Request, proxy.FrameWriter) int { return 0 })
			var sink CtlSink = ctl
			if test.nilCtl {
				sink = nil
			}
			_, path, stop := ctlListener(t, sink, nil, test.found)
			defer stop()

			conn, reader := dialCtl(t, path)
			if _, err := io.WriteString(conn, test.header); err != nil {
				t.Fatalf("write: %v", err)
			}
			got := readAll(t, reader, conn)
			if strings.HasPrefix(got, "ack") || !strings.Contains(got, test.want) || !strings.HasSuffix(got, "x 1\n") {
				t.Fatalf("reply = %q, want a refusal containing %q and no ack", got, test.want)
			}
			if len(ctl.calls) != 0 {
				t.Fatal("sink called for a refused call")
			}
		})
	}
}

func TestListenerCtlShortBodyHitsDeadline(t *testing.T) {
	t.Helper()
	ctl := newFuncCtl(func(context.Context, proxy.Request, proxy.FrameWriter) int { return 0 })
	listener, path, stop := ctlListener(t, ctl, nil, true)
	listener.bodyTimeout = 150 * time.Millisecond
	defer stop()

	conn, reader := dialCtl(t, path)
	if _, err := io.WriteString(conn, ctlHeader("api", nil, 10)+"short"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readAll(t, reader, conn)
	if !strings.HasPrefix(got, "ack 1\n") || !strings.Contains(got, "read stdin") || !strings.HasSuffix(got, "x 1\n") {
		t.Fatalf("reply = %q, want ack, a stdin read error and x 1", got)
	}
	if len(ctl.calls) != 0 {
		t.Fatal("sink called for a short body")
	}
}

func TestListenerCtlClientEOFCancelsRequest(t *testing.T) {
	t.Helper()
	canceled := make(chan struct{})
	ctl := newFuncCtl(func(ctx context.Context, _ proxy.Request, _ proxy.FrameWriter) int {
		<-ctx.Done()
		close(canceled)
		return 1
	})
	_, path, stop := ctlListener(t, ctl, nil, true)
	defer stop()

	conn, reader := dialCtl(t, path)
	if _, err := io.WriteString(conn, ctlHeader("api", nil, 0)); err != nil {
		t.Fatalf("write: %v", err)
	}
	readLine(t, reader, conn)
	<-ctl.calls
	_ = conn.Close()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("request context was not canceled by client EOF")
	}
}

func TestListenerServeReturnsWhileSinkBlocks(t *testing.T) {
	t.Helper()
	ctl := newFuncCtl(func(ctx context.Context, _ proxy.Request, _ proxy.FrameWriter) int {
		<-ctx.Done()
		return 1
	})
	_, path, stop := ctlListener(t, ctl, nil, true)

	conn, reader := dialCtl(t, path)
	if _, err := io.WriteString(conn, ctlHeader("api", nil, 0)); err != nil {
		t.Fatalf("write: %v", err)
	}
	readLine(t, reader, conn)
	<-ctl.calls
	stop() // fails the test if Serve does not return within 2 s
}

func TestListenerCtlMarksAlive(t *testing.T) {
	t.Helper()
	liveness := &recordingLiveness{calls: make(chan struct{}, 2)}
	ctl := newFuncCtl(func(context.Context, proxy.Request, proxy.FrameWriter) int { return 0 })
	listener, path, stop := ctlListener(t, ctl, liveness, true)
	defer stop()

	conn, reader := dialCtl(t, path)
	if _, err := io.WriteString(conn, ctlHeader("api", nil, 0)); err != nil {
		t.Fatalf("write: %v", err)
	}
	readAll(t, reader, conn)
	select {
	case <-liveness.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("MarkAlive was not called for a ctl call")
	}
	if listener.LastEvent().IsZero() {
		t.Fatal("LastEvent() is zero after a ctl call")
	}
}

// dialSocket retries a refused dial: the socket file appears at bind, slightly before listen.
func dialSocket(path string) (net.Conn, error) {
	var conn net.Conn
	var err error
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		conn, err = net.Dial("unix", path)
		if err == nil || !errors.Is(err, syscall.ECONNREFUSED) {
			break
		}
	}
	return conn, err
}
