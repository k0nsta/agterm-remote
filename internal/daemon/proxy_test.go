package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
	"github.com/k0nsta/agterm-remote/internal/proxy"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

const (
	pxHost = "homelab"
	pxID   = "00000000-0000-4000-8000-0000000000"

	rowCaller = pxID + "01" // w1: the caller, attached
	paneCall  = pxID + "02"
	rowSib    = pxID + "03" // w2: another session of the host, attached
	paneSib   = pxID + "04"
	rowAgent  = pxID + "05" // a local row running claude
	paneAgent = pxID + "06"
	rowAgent2 = pxID + "07" // a second local row running claude
	paneAgnt2 = pxID + "08"
	rowNew    = pxID + "09" // the row `session new` makes
	paneNew   = pxID + "10"
)

func attachedAs(t *testing.T, name string) []string {
	t.Helper()
	return []string{"/opt/homebrew/bin/agr", "open", pxHost, name}
}

// fakeCtl is the agtermctl the proxy runs on: a tree it serves, a script for
// Exec, and a dialog it records.
type fakeCtl struct {
	mu    sync.Mutex
	rows  []agterm.TreeRow
	execs []execRecord
	// execFn answers a call; nil answers 0.
	execFn func(ctx context.Context, stdin []byte, stdout, stderr io.Writer, args []string) (int, error)

	asks       []agterm.AskRequest
	askErr     error
	results    []agterm.AskAnswer
	resultCall int
	// onResult runs on each AskResult call before it answers.
	onResult   func(call int)
	askCancels []string
	// notices records Notify calls as row, title, body.
	notices [][3]string
}

type execRecord struct {
	args  []string
	stdin []byte
	// live is whether the call's context was still live when it started.
	live bool
}

func (c *fakeCtl) Exec(ctx context.Context, stdin []byte, stdout, stderr io.Writer, args ...string) (int, error) {
	c.mu.Lock()
	c.execs = append(c.execs, execRecord{args: append([]string(nil), args...), stdin: stdin, live: ctx.Err() == nil})
	fn := c.execFn
	c.mu.Unlock()
	if fn == nil {
		return 0, nil
	}
	return fn(ctx, stdin, stdout, stderr, args)
}

func (c *fakeCtl) TreeRows(context.Context) ([]agterm.TreeRow, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := make([]agterm.TreeRow, len(c.rows))
	copy(rows, c.rows)
	return rows, nil
}

func (c *fakeCtl) AskOpen(_ context.Context, request agterm.AskRequest) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asks = append(c.asks, request)
	if c.askErr != nil {
		return "", c.askErr
	}
	return "D1", nil
}

func (c *fakeCtl) AskResult(context.Context, string) (agterm.AskAnswer, error) {
	c.mu.Lock()
	call := c.resultCall
	c.resultCall++
	hook := c.onResult
	c.mu.Unlock()
	if hook != nil {
		hook(call)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.results) == 0 {
		return agterm.AskAnswer{State: agterm.AskPending}, nil
	}
	if call >= len(c.results) {
		call = len(c.results) - 1
	}
	return c.results[call], nil
}

func (c *fakeCtl) AskCancel(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.askCancels = append(c.askCancels, id)
	return nil
}

func (c *fakeCtl) Notify(_ context.Context, row, title, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notices = append(c.notices, [3]string{row, title, body})
	return nil
}

func (c *fakeCtl) setRow(t *testing.T, id string, edit func(*agterm.TreeRow)) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.rows {
		if c.rows[i].ID == id {
			edit(&c.rows[i])
			return
		}
	}
	t.Fatalf("no row %s", id)
}

func (c *fakeCtl) execArgs(t *testing.T) [][]string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var all [][]string
	for _, record := range c.execs {
		all = append(all, record.args)
	}
	return all
}

func (c *fakeCtl) askCount(t *testing.T) int {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.asks)
}

func (c *fakeCtl) cancelled(t *testing.T) []string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.askCancels...)
}

// recFrames records what a call wrote, in order.
type recFrames struct {
	mu     sync.Mutex
	events []string
	out    bytes.Buffer
	err    bytes.Buffer
}

type recStream struct {
	frames *recFrames
	kind   string
}

func (r *recFrames) Stdout() io.Writer { return recStream{r, "o:"} }
func (r *recFrames) Stderr() io.Writer { return recStream{r, "e:"} }

func (s recStream) Write(p []byte) (int, error) {
	s.frames.mu.Lock()
	defer s.frames.mu.Unlock()
	s.frames.events = append(s.frames.events, s.kind+string(p))
	if s.kind == "o:" {
		s.frames.out.Write(p)
	} else {
		s.frames.err.Write(p)
	}
	return len(p), nil
}

func (r *recFrames) stdout(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.String()
}

func (r *recFrames) stderr(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err.String()
}

type versionSink struct {
	mu      sync.Mutex
	version string
	err     error
	reads   int
	status  []string
}

func (s *versionSink) Version(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return s.version, s.err
}

func (s *versionSink) Status(_ context.Context, target string, _ agterm.StatusArgs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = append(s.status, target)
	return nil
}

type sessionList []string

func (l sessionList) Sessions(context.Context, string) ([]remote.Session, error) {
	var sessions []remote.Session
	for _, name := range l {
		sessions = append(sessions, remote.Session{Name: name})
	}
	return sessions, nil
}

type proxyEnv struct {
	dirs  paths.Dirs
	ctl   *fakeCtl
	store *bindings.Store
	sink  *versionSink
	d     *Daemon
	host  *hostCtl
	// clock is the service's time, advanced by tests.
	clockMu sync.Mutex
	clock   time.Time
}

func newProxyEnv(t *testing.T) *proxyEnv {
	t.Helper()
	dirs := pathstest.Dirs(t)
	store := bindings.New(dirs)
	for _, b := range []bindings.Binding{
		{Row: rowCaller, PaneID: paneCall, Pane: "left", Host: pxHost, Name: "w1", Mux: "tmux"},
		{Row: rowSib, PaneID: paneSib, Pane: "left", Host: pxHost, Name: "w2", Mux: "tmux"},
	} {
		if err := store.Bind(b); err != nil {
			t.Fatalf("Bind() error = %v", err)
		}
	}
	env := &proxyEnv{dirs: dirs, store: store, sink: &versionSink{version: "0.36.0"}, clock: time.Unix(1_000_000, 0)}
	env.ctl = &fakeCtl{rows: []agterm.TreeRow{
		{ID: rowCaller, Name: "w1", Surfaces: oneSurface(t, paneCall), Foreground: attachedAs(t, "w1")},
		{ID: rowSib, Name: "w2", Surfaces: oneSurface(t, paneSib), Foreground: attachedAs(t, "w2")},
		{ID: rowAgent, Name: "orchestrator", Surfaces: oneSurface(t, paneAgent), Foreground: []string{"claude", "--resume"}},
		{ID: rowAgent2, Name: "other agent", Surfaces: oneSurface(t, paneAgnt2), Foreground: []string{"codex"}},
	}}
	rewriter := &proxy.Rewriter{Agr: "/opt/homebrew/bin/agr", Root: dirs.Proxy(), Sessions: sessionList{}, Binds: store}
	env.d = New(Config{
		Dirs: dirs, Sink: env.sink, Bindings: store, Logger: log.New(io.Discard, "", 0),
		Ctl: env.ctl, Authorizer: proxy.NewPolicy(env.ctl, store), Rewriter: rewriter, ProxyStore: store,
	})
	if env.d.proxy == nil {
		t.Fatal("proxy service was not configured")
	}
	t.Cleanup(env.d.proxy.shim.close)
	svc := env.d.proxy
	svc.now = env.now
	svc.consentPoll = 2 * time.Millisecond
	svc.consentTimeout = 5 * time.Second
	svc.pagePoll = 5 * time.Millisecond
	svc.prebindWait = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	env.host = svc.newHost(pxHost, ctx)
	return env
}

func oneSurface(t *testing.T, pane string) []agterm.Surface {
	t.Helper()
	return []agterm.Surface{{Kind: "left", PaneID: pane, Visible: true}}
}

func (e *proxyEnv) now() time.Time {
	e.clockMu.Lock()
	defer e.clockMu.Unlock()
	return e.clock
}

func (e *proxyEnv) advance(t *testing.T, by time.Duration) {
	t.Helper()
	e.clockMu.Lock()
	defer e.clockMu.Unlock()
	e.clock = e.clock.Add(by)
}

// request builds a call from a session of the host.
func (e *proxyEnv) request(t *testing.T, session string, argv ...string) proxy.Request {
	t.Helper()
	caller, ok := e.store.ByHostName(pxHost, session)
	if !ok {
		t.Fatalf("no binding for %s", session)
	}
	return proxy.Request{
		Host: pxHost, Session: session, Caller: caller, Argv: argv, CWD: "/home/u",
		Self: proxy.Self{Row: caller.Row, PaneID: caller.PaneID},
	}
}

func (e *proxyEnv) call(t *testing.T, session string, argv ...string) (int, *recFrames) {
	t.Helper()
	return e.callCtx(t, context.Background(), e.request(t, session, argv...))
}

func (e *proxyEnv) callCtx(t *testing.T, ctx context.Context, req proxy.Request) (int, *recFrames) {
	t.Helper()
	frames := &recFrames{}
	return e.host.Ctl(ctx, req, frames), frames
}

// callAsync starts a call and returns its result channel.
func (e *proxyEnv) callAsync(t *testing.T, ctx context.Context, req proxy.Request) (<-chan int, *recFrames) {
	t.Helper()
	frames := &recFrames{}
	done := make(chan int, 1)
	go func() { done <- e.host.Ctl(ctx, req, frames) }()
	return done, frames
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func receive(t *testing.T, done <-chan int, what string) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return 0
	}
}

func TestProxyRefusesAndRunsThroughAuthorize(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	code, frames := env.call(t, "w1", "session", "scratch")
	if code != 1 || !strings.Contains(frames.stderr(t), "agtermctl (via agr): `session scratch` is not available from a host") {
		t.Fatalf("code = %d, stderr = %q, want a refusal naming the leaf", code, frames.stderr(t))
	}
	if len(env.ctl.execArgs(t)) != 0 {
		t.Fatal("a refused call ran agtermctl")
	}
	code, _ = env.call(t, "w1", "tree", "--json")
	if code != 0 {
		t.Fatalf("tree code = %d, want 0", code)
	}
	if got := env.ctl.execArgs(t); len(got) != 1 || strings.Join(got[0], " ") != "tree --json" {
		t.Fatalf("exec = %q, want tree --json", got)
	}
}

func TestProxyStreamsStdoutAndStderrInOrder(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	env.ctl.execFn = func(_ context.Context, _ []byte, stdout, stderr io.Writer, _ []string) (int, error) {
		_, _ = io.WriteString(stdout, "out1")
		_, _ = io.WriteString(stderr, "err1")
		_, _ = io.WriteString(stdout, "out2")
		return 0, nil
	}
	code, frames := env.call(t, "w1", "tree")
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	want := []string{"o:out1", "e:err1", "o:out2"}
	frames.mu.Lock()
	defer frames.mu.Unlock()
	if strings.Join(frames.events, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %q, want %q", frames.events, want)
	}
}

func TestProxyExitCodes(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name   string
		code   int
		err    error
		want   int
		stderr string
	}{
		{name: "passthrough 2", code: 2, want: 2},
		{name: "passthrough 7", code: 7, want: 7},
		{name: "minus one becomes 1", code: -1, want: 1},
		{name: "start failure becomes 1", code: -1, err: errors.New("agtermctl: not found"), want: 1, stderr: "agtermctl: not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newProxyEnv(t)
			env.ctl.execFn = func(context.Context, []byte, io.Writer, io.Writer, []string) (int, error) { return tc.code, tc.err }
			code, frames := env.call(t, "w1", "tree")
			if code != tc.want || !strings.Contains(frames.stderr(t), tc.stderr) {
				t.Fatalf("code = %d, stderr = %q, want %d / %q", code, frames.stderr(t), tc.want, tc.stderr)
			}
		})
	}
}

func TestProxyForwardsStdinOnlyWithTheFlag(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	req := env.request(t, "w1", "session", "type", "--stdin")
	req.Stdin = []byte("make\n")
	if code, _ := env.callCtx(t, context.Background(), req); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	env.ctl.mu.Lock()
	defer env.ctl.mu.Unlock()
	if got := env.ctl.execs[0]; string(got.stdin) != "make\n" || strings.Join(got.args, " ") != "session type --target="+rowCaller+" --pane-id="+paneCall+" --stdin" {
		t.Fatalf("exec = %q stdin %q, want the typed text on stdin", got.args, got.stdin)
	}
}

func TestProxyRefusesEveryCallOnAnOldAgterm(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	env.sink.version = "0.35.9"
	for _, argv := range [][]string{{"tree"}, {"version"}, {"session", "text"}} {
		code, frames := env.call(t, "w1", argv...)
		if code != 1 || !strings.Contains(frames.stderr(t), "agtermctl (via agr): needs agterm 0.36.0 (found 0.35.9)") {
			t.Fatalf("%v: code = %d, stderr = %q, want the version message", argv, code, frames.stderr(t))
		}
	}
	if len(env.ctl.execArgs(t)) != 0 {
		t.Fatal("agtermctl ran on an old agterm")
	}
	if env.sink.reads != 1 {
		t.Fatalf("version reads = %d, want one per host start", env.sink.reads)
	}
	// Status events do not go through the proxy gate.
	if err := env.d.Status(context.Background(), rowCaller, agterm.StatusArgs{Status: "active"}); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(env.sink.status) != 1 || env.sink.status[0] != rowCaller {
		t.Fatalf("status calls = %q, want the event forwarded", env.sink.status)
	}
}

func TestProxyRetriesAFailedVersionRead(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	env.sink.err = errors.New("agterm is not running")
	code, frames := env.call(t, "w1", "tree")
	if code != 1 || !strings.Contains(frames.stderr(t), "cannot read the agterm version") {
		t.Fatalf("code = %d, stderr = %q, want the read failure", code, frames.stderr(t))
	}
	env.sink.mu.Lock()
	env.sink.err = nil
	env.sink.mu.Unlock()
	if code, _ := env.call(t, "w1", "tree"); code != 0 {
		t.Fatalf("code after agterm came back = %d, want 0", code)
	}
}

func TestProxyCancelKillsTheCallAndRunsTheCloseHook(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	started := make(chan struct{})
	env.ctl.execFn = func(ctx context.Context, _ []byte, _, _ io.Writer, args []string) (int, error) {
		if args[2] != "open" {
			return 0, nil
		}
		close(started)
		<-ctx.Done()
		return -1, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done, _ := env.callAsync(t, ctx, env.request(t, "w1", "session", "overlay", "open", "--block", "--", "vim"))
	<-started
	cancel()
	receive(t, done, "the canceled call")
	got := env.ctl.execArgs(t)
	if len(got) != 2 || strings.Join(got[1], " ") != "session overlay close --target="+rowCaller {
		t.Fatalf("exec = %q, want the open and then the overlay close hook", got)
	}
	env.ctl.mu.Lock()
	defer env.ctl.mu.Unlock()
	if !env.ctl.execs[1].live {
		t.Fatal("the cancel hook ran on a dead context")
	}
}

func TestProxyAnUnblockedOverlayHasNoCancelHook(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	env.ctl.execFn = func(ctx context.Context, _ []byte, _, _ io.Writer, _ []string) (int, error) {
		cancel()
		<-ctx.Done()
		return -1, ctx.Err()
	}
	done, _ := env.callAsync(t, ctx, env.request(t, "w1", "session", "overlay", "open", "--", "vim"))
	receive(t, done, "the canceled call")
	if got := env.ctl.execArgs(t); len(got) != 1 {
		t.Fatalf("exec = %q, want no hook for a non-blocking open", got)
	}
}

func TestProxyRefusesTheSeventeenthConcurrentCall(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	release := make(chan struct{})
	started := make(chan struct{}, 2*maxCtlCalls)
	env.ctl.execFn = func(ctx context.Context, _ []byte, _, _ io.Writer, _ []string) (int, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return 0, nil
	}
	var dones []<-chan int
	for i := 0; i < maxCtlCalls; i++ {
		done, _ := env.callAsync(t, context.Background(), env.request(t, "w1", "tree"))
		dones = append(dones, done)
	}
	for i := 0; i < maxCtlCalls; i++ {
		<-started
	}
	code, frames := env.call(t, "w1", "tree")
	if code != 1 || !strings.Contains(frames.stderr(t), "too many calls in flight") {
		t.Fatalf("17th call: code = %d, stderr = %q, want a refusal", code, frames.stderr(t))
	}
	close(release)
	for _, done := range dones {
		receive(t, done, "a held call")
	}
	if code, _ := env.call(t, "w1", "tree"); code != 0 {
		t.Fatalf("call after the others ended: code = %d, want 0", code)
	}
}

func TestProxyShutdownEndsABlockingCall(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	hostCtx, stopHost := context.WithCancel(context.Background())
	host := env.d.proxy.newHost(pxHost, hostCtx)
	started := make(chan struct{})
	env.ctl.execFn = func(ctx context.Context, _ []byte, _, _ io.Writer, _ []string) (int, error) {
		close(started)
		<-ctx.Done()
		return -1, ctx.Err()
	}
	done := make(chan int, 1)
	go func() { done <- host.Ctl(hostCtx, env.request(t, "w1", "events"), &recFrames{}) }()
	<-started
	stopHost()
	drained := make(chan struct{})
	go func() { host.shutdown(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not return while a call was blocked")
	}
	receive(t, done, "the blocked call")
	frames := &recFrames{}
	if code := host.Ctl(context.Background(), env.request(t, "w1", "tree"), frames); code != 1 || !strings.Contains(frames.stderr(t), "stopping") {
		t.Fatalf("call after shutdown: code = %d, stderr = %q, want a stopping refusal", code, frames.stderr(t))
	}
}

// TestDaemonStopHostReturnsWhileACallBlocks drives a real host start: the call
// arrives through the host's sink, and stopHost has to return within the
// teardown timeout although the call would otherwise never end.
func TestDaemonStopHostReturnsWhileACallBlocks(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	host := pxHost
	store := bindings.New(dirs)
	if err := store.Bind(bindings.Binding{Row: rowCaller, PaneID: paneCall, Pane: "left", Host: host, Name: "w1"}); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	started := make(chan struct{})
	ctl := &fakeCtl{rows: []agterm.TreeRow{{ID: rowCaller, Name: "w1", Surfaces: oneSurface(t, paneCall), Foreground: attachedAs(t, "w1")}}}
	ctl.execFn = func(ctx context.Context, _ []byte, _, _ io.Writer, _ []string) (int, error) {
		close(started)
		<-ctx.Done()
		return -1, ctx.Err()
	}
	remote := &daemonRemote{ensureCalls: map[string]int{}, homeCalls: map[string]int{}, home: map[string]string{host: "/home/a"}}
	config := daemonConfig(t, dirs, remote, nil, &versionSink{version: "0.36.0"}, &daemonSupervisors{})
	config.Bindings = store
	config.Ctl, config.Authorizer, config.ProxyStore = ctl, proxy.NewPolicy(ctl, store), store
	config.Rewriter = &proxy.Rewriter{Agr: "/agr", Root: dirs.Proxy(), Sessions: sessionList{}, Binds: store}
	config.TeardownTimeout = 3 * time.Second
	daemon := New(config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := daemon.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = daemon.Close() }()
	waitFor(t, "the host to start", func() bool {
		daemon.mu.Lock()
		defer daemon.mu.Unlock()
		runtime := daemon.hosts[host]
		return runtime != nil && runtime.ctl != nil
	})
	daemon.mu.Lock()
	sink := daemon.hosts[host].ctl
	daemon.mu.Unlock()
	caller, _ := store.ByHostName(host, "w1")
	call := make(chan int, 1)
	go func() {
		call <- sink.Ctl(daemon.hosts[host].ctl.ctx, proxy.Request{Host: host, Session: "w1", Caller: caller, Argv: []string{"events"}}, &recFrames{})
	}()
	<-started

	begin := time.Now()
	stopped := make(chan struct{})
	go func() { daemon.stopHost(host); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(4 * time.Second):
		t.Fatal("stopHost did not return while a call was blocked")
	}
	if elapsed := time.Since(begin); elapsed >= 3*time.Second {
		t.Fatalf("stopHost took %s, want it to end the call and return quickly", elapsed)
	}
	receive(t, call, "the blocked call")
}

func TestProxyNewRowIsPreBoundBeforeAgrOpenRuns(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	env.ctl.execFn = func(_ context.Context, _ []byte, stdout, _ io.Writer, args []string) (int, error) {
		if args[0] == "session" && args[1] == "new" {
			env.ctl.mu.Lock()
			env.ctl.rows = append(env.ctl.rows, agterm.TreeRow{ID: rowNew, Name: "w9", Surfaces: oneSurface(t, paneNew), ForegroundShell: "zsh"})
			env.ctl.mu.Unlock()
			_, _ = io.WriteString(stdout, rowNew+"\n")
		}
		return 0, nil
	}
	code, frames := env.call(t, "w1", "session", "new", "--name", "w9", "--cwd", "/home/u/p", "--command", "make test")
	if code != 0 || strings.TrimSpace(frames.stdout(t)) != rowNew {
		t.Fatalf("session new: code = %d, stdout = %q, want the row id passed through", code, frames.stdout(t))
	}
	wantCommand := `--command='/opt/homebrew/bin/agr' 'open' 'homelab' 'w9' '--cwd' '/home/u/p' '--command' 'make test'`
	if got := env.ctl.execArgs(t)[0]; strings.Join(got, "\x00") != strings.Join([]string{"session", "new", "--name=w9", wantCommand}, "\x00") {
		t.Fatalf("exec = %q, want the agr open command", got)
	}
	bound, ok := env.store.ByHostName(pxHost, "w9")
	if !ok || bound.Row != rowNew || bound.PaneID != paneNew || bound.Pane != "left" || bound.Mux != "tmux" {
		t.Fatalf("binding = %+v, %v, want the new row's primary pane pre-bound (mux copied)", bound, ok)
	}

	// `agr open` is not the foreground yet: the retry message.
	code, frames = env.call(t, "w1", "session", "restore", "--target", rowNew, "claude")
	if code != 1 || !strings.Contains(frames.stderr(t), "not attached yet; retry") {
		t.Fatalf("restore before agr open: code = %d, stderr = %q, want the retry message", code, frames.stderr(t))
	}
	env.ctl.setRow(t, rowNew, func(row *agterm.TreeRow) { row.Foreground = attachedAs(t, "w9") })
	code, frames = env.call(t, "w1", "session", "restore", "--target", rowNew, "claude")
	if code != 0 {
		t.Fatalf("restore once attached: code = %d, stderr = %q, want it authorised", code, frames.stderr(t))
	}
	last := env.ctl.execArgs(t)
	wantRestore := "'/opt/homebrew/bin/agr' 'open' 'homelab' 'w9' '--command' 'claude'"
	if got := last[len(last)-1]; got[len(got)-1] != wantRestore {
		t.Fatalf("restore exec = %q, want the agr open command %s", got, wantRestore)
	}
}

func TestProxyNewRowWithoutAnIDWarnsAndSucceeds(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	code, frames := env.call(t, "w1", "session", "new", "--name", "w9")
	if code != 0 || !strings.Contains(frames.stderr(t), "the new row is not bound yet") {
		t.Fatalf("code = %d, stderr = %q, want success and a warning", code, frames.stderr(t))
	}
	if _, ok := env.store.ByHostName(pxHost, "w9"); ok {
		t.Fatal("a row without an id was bound")
	}
}

func TestProxyURLWarningAndStaticURLPages(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	code, frames := env.call(t, "w1", "session", "overlay", "open", "--url", "http://localhost:3000/app")
	if code != 0 || !strings.Contains(frames.stderr(t), "warning: http://localhost:3000/app opens on the Mac") {
		t.Fatalf("code = %d, stderr = %q, want the localhost warning", code, frames.stderr(t))
	}
	code, frames = env.call(t, "w1", "session", "overlay", "open", "--url", "https://example.com/")
	if code != 0 || frames.stderr(t) != "" {
		t.Fatalf("static URL: code = %d, stderr = %q, want it allowed with no warning", code, frames.stderr(t))
	}
	before := len(env.ctl.execArgs(t))
	code, frames = env.call(t, "w1", "session", "overlay", "open", "--url", "https://example.com/", "--js")
	if code != 1 || !strings.Contains(frames.stderr(t), "--js is not allowed with --url") {
		t.Fatalf("--url --js: code = %d, stderr = %q, want a refusal", code, frames.stderr(t))
	}
	if len(env.ctl.execArgs(t)) != before {
		t.Fatal("a refused --url --js ran agtermctl")
	}
}

// pageAgterm scripts agtermctl for page tests: an overlay open with a --url
// adds a page to the row's slots and reports its id; an overlay close removes
// it. Each open's URL is sent to opened.
type pageAgterm struct {
	env    *proxyEnv
	opened chan string
	pageID string
	row    string
}

func installPageAgterm(t *testing.T, env *proxyEnv) *pageAgterm {
	t.Helper()
	script := &pageAgterm{env: env, opened: make(chan string, 32), pageID: "PG1", row: rowCaller}
	env.ctl.execFn = func(_ context.Context, _ []byte, stdout, _ io.Writer, args []string) (int, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "session overlay open"):
			for _, arg := range args {
				if url, found := strings.CutPrefix(arg, "--url="); found {
					script.opened <- url
				}
			}
			env.ctl.setRow(t, script.row, func(row *agterm.TreeRow) {
				row.HTMLOverlays = []agterm.HTMLOverlay{{ID: script.pageID, State: "loaded"}}
			})
			_, _ = io.WriteString(stdout, `{"ok":true,"result":{"pageID":"`+script.pageID+`"}}`+"\n")
		case strings.HasPrefix(joined, "session overlay close"):
			env.ctl.setRow(t, script.row, func(row *agterm.TreeRow) { row.HTMLOverlays = nil })
		}
		return 0, nil
	}
	return script
}

func (s *pageAgterm) url(t *testing.T) string {
	t.Helper()
	select {
	case url := <-s.opened:
		return url
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the page to be opened")
		return ""
	}
}

func htmlRequest(t *testing.T, env *proxyEnv, argv ...string) proxy.Request {
	t.Helper()
	req := env.request(t, "w1", argv...)
	req.Files = [][]byte{[]byte("<html><head><title>t</title></head><body>hi</body></html>")}
	return req
}

func TestProxyHTMLRequestOpensAShimURLNeverHTML(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	page := installPageAgterm(t, env)
	done, frames := env.callAsync(t, context.Background(), htmlRequest(t, env, "session", "overlay", "open", "--html", "@0", "--js", "--block"))
	url := page.url(t)

	args := env.ctl.execArgs(t)[0]
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--html") || strings.Contains(joined, "--block") {
		t.Fatalf("exec = %q, want a URL page, never --html", args)
	}
	if !regexp.MustCompile(`^http://127\.0\.0\.1:\d+/[0-9a-f]{32}/$`).MatchString(url) || !strings.Contains(joined, "--url="+url+" --js") {
		t.Fatalf("exec = %q, url = %q, want --url=<shim url> --js", args, url)
	}
	response, body := get(t, url)
	if response.Header.Get("Content-Security-Policy") != pageCSP || !strings.Contains(body, "<head><script>") || !strings.Contains(body, "hi") {
		t.Fatalf("served page = %q (CSP %q), want the CSP header and the shim first in head", body, response.Header.Get("Content-Security-Policy"))
	}
	files, _ := filepath.Glob(filepath.Join(env.dirs.Proxy(), "*", "0.html"))
	if len(files) != 1 {
		t.Fatalf("temp files = %v, want the page file while the call blocks", files)
	}
	if info, err := os.Stat(files[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("temp file mode = %v, %v, want 0600", info, err)
	}
	if data, _ := os.ReadFile(files[0]); !strings.Contains(string(data), "<body>hi</body>") {
		t.Fatalf("temp file = %q, want the page", data)
	}

	if status := post(t, url+"submit", `{"value":"main"}`).StatusCode; status != http.StatusOK {
		t.Fatalf("submit status = %d, want 200", status)
	}
	if code := receive(t, done, "the blocking call"); code != 0 {
		t.Fatalf("code = %d, want 0 for a submit", code)
	}
	if out := frames.stdout(t); !strings.Contains(out, `"outcome":"submitted"`) || !strings.Contains(out, `"pageID":"PG1"`) || !strings.Contains(out, `"value":"main"`) {
		t.Fatalf("stdout = %q, want the outcome JSON", out)
	}
	all := env.ctl.execArgs(t)
	if last := strings.Join(all[len(all)-1], " "); last != "session overlay close --target="+rowCaller {
		t.Fatalf("last exec = %q, want the daemon to close the overlay after a submit", last)
	}
	if files, _ := filepath.Glob(filepath.Join(env.dirs.Proxy(), "*")); len(files) != 0 {
		t.Fatalf("temp files after a blocking call = %v, want none", files)
	}
	if response, _ := get(t, url); response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after the call: status = %d, want the token gone", response.StatusCode)
	}
}

func TestProxyHTMLRequestDismissals(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, env *proxyEnv, url string)
	}{
		{"close", func(t *testing.T, _ *proxyEnv, url string) { post(t, url+"close", "") }},
		{"window.close through the shim", func(t *testing.T, _ *proxyEnv, url string) { post(t, url+"close", "{}") }},
		{"the page leaves htmlOverlays", func(t *testing.T, env *proxyEnv, _ string) {
			env.ctl.setRow(t, rowCaller, func(row *agterm.TreeRow) { row.HTMLOverlays = nil })
		}},
		{"a host overlay close", func(t *testing.T, env *proxyEnv, _ string) {
			if code, _ := env.call(t, "w1", "session", "overlay", "close"); code != 0 {
				t.Fatalf("overlay close code = %d, want 0", code)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newProxyEnv(t)
			page := installPageAgterm(t, env)
			done, frames := env.callAsync(t, context.Background(), htmlRequest(t, env, "session", "overlay", "open", "--html", "@0", "--js", "--block"))
			url := page.url(t)
			tc.end(t, env, url)
			if code := receive(t, done, "the blocking call"); code != 2 {
				t.Fatalf("code = %d, want 2 for a dismissal", code)
			}
			if out := frames.stdout(t); !strings.Contains(out, `"outcome":"dismissed"`) || strings.Contains(out, "value") {
				t.Fatalf("stdout = %q, want a dismissed outcome", out)
			}
		})
	}
}

func TestProxyHostOverlaySubmitDrivesTheShimPage(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	page := installPageAgterm(t, env)
	done, frames := env.callAsync(t, context.Background(), htmlRequest(t, env, "session", "overlay", "open", "--html", "@0", "--js", "--block"))
	page.url(t)
	if code, _ := env.call(t, "w1", "session", "overlay", "submit", "--value", "from-host"); code != 0 {
		t.Fatalf("overlay submit code = %d, want 0", code)
	}
	if code := receive(t, done, "the blocking call"); code != 0 || !strings.Contains(frames.stdout(t), `"value":"from-host"`) {
		t.Fatalf("code = %d, stdout = %q, want the host's submit as the outcome", code, frames.stdout(t))
	}
	for _, args := range env.ctl.execArgs(t) {
		if len(args) > 2 && args[2] == "submit" {
			t.Fatalf("exec = %q, a submit reached agterm for a shim page", args)
		}
	}
}

func TestProxyNonBlockingHTMLKeepsItsFilesUntilThePageCloses(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	page := installPageAgterm(t, env)
	code, frames := env.callCtx(t, context.Background(), htmlRequest(t, env, "session", "overlay", "open", "--html", "@0", "--js"))
	if code != 0 || !strings.Contains(frames.stdout(t), `"pageID":"PG1"`) {
		t.Fatalf("code = %d, stdout = %q, want agterm's reply", code, frames.stdout(t))
	}
	url := page.url(t)
	files, _ := filepath.Glob(filepath.Join(env.dirs.Proxy(), "*", "0.html"))
	if len(files) != 1 {
		t.Fatalf("temp files = %v, want the file kept after a non-blocking open", files)
	}
	if response, _ := get(t, url); response.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want the page still served (reloads work)", response.StatusCode)
	}
	post(t, url+"close", "")
	waitFor(t, "the temp files to go", func() bool {
		left, _ := filepath.Glob(filepath.Join(env.dirs.Proxy(), "*"))
		return len(left) == 0
	})
	if response, _ := get(t, url); response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after close: status = %d, want 404", response.StatusCode)
	}
}

func TestProxyNinthPageIsRefused(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	installPageAgterm(t, env)
	for i := 0; i < maxPagesPerHost; i++ {
		if code, frames := env.callCtx(t, context.Background(), htmlRequest(t, env, "session", "overlay", "open", "--html", "@0", "--js")); code != 0 {
			t.Fatalf("page %d: code = %d, stderr = %q", i+1, code, frames.stderr(t))
		}
	}
	code, frames := env.callCtx(t, context.Background(), htmlRequest(t, env, "session", "overlay", "open", "--html", "@0", "--js"))
	if code != 1 || !strings.Contains(frames.stderr(t), "too many open pages") {
		t.Fatalf("9th page: code = %d, stderr = %q, want a refusal", code, frames.stderr(t))
	}
	if left, _ := filepath.Glob(filepath.Join(env.dirs.Proxy(), "*")); len(left) != maxPagesPerHost {
		t.Fatalf("temp directories = %d, want %d (the refused page leaves none)", len(left), maxPagesPerHost)
	}
}

func TestProxyBlockedPageCancelClosesTheOverlay(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	page := installPageAgterm(t, env)
	ctx, cancel := context.WithCancel(context.Background())
	done, _ := env.callAsync(t, ctx, htmlRequest(t, env, "session", "overlay", "open", "--html", "@0", "--js", "--block"))
	url := page.url(t)
	cancel()
	receive(t, done, "the canceled call")
	all := env.ctl.execArgs(t)
	if last := strings.Join(all[len(all)-1], " "); last != "session overlay close --target="+rowCaller {
		t.Fatalf("last exec = %q, want the overlay closed", last)
	}
	if left, _ := filepath.Glob(filepath.Join(env.dirs.Proxy(), "*")); len(left) != 0 {
		t.Fatalf("temp directories = %v, want none", left)
	}
	if response, _ := get(t, url); response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after cancel: status = %d, want the token gone", response.StatusCode)
	}
}

func TestProxyStartClearsLeftoversAndSweepsOldDirectories(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	old := filepath.Join(env.dirs.Proxy(), "old")
	fresh := filepath.Join(env.dirs.Proxy(), "fresh")
	for _, dir := range []string{old, fresh} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	aged := env.now().Add(-25 * time.Hour)
	if err := os.Chtimes(old, aged, aged); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	env.d.proxy.now = func() time.Time { return env.now().Add(time.Second) }
	env.d.proxy.sweepTemp()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old directory: %v, want it swept", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh directory: %v, want it kept", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	env.d.startProxy(ctx, time.Hour)
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Fatalf("leftover at start: %v, want it cleared", err)
	}
	cancel()
	env.d.children.Wait()
}

func TestDaemonWithoutProxyConfigHasNoProxyService(t *testing.T) {
	t.Helper()
	daemon := New(Config{Dirs: pathstest.Dirs(t), Logger: log.New(io.Discard, "", 0)})
	if daemon.proxy != nil {
		t.Fatal("proxy service exists without its dependencies")
	}
}
