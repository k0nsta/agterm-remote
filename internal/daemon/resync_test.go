package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/bridge"
	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
	"github.com/k0nsta/agterm-remote/internal/remote"
	"github.com/k0nsta/agterm-remote/internal/token"
)

func TestDaemonResyncsLevelsAndHUDAcrossBridgeTransitions(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	trace := newTask10Trace(t)
	r := newTask10Remote(t, trace)
	host := "host-a"
	r.home[host] = "/home/test"
	r.sessions[host] = []remote.Session{
		{Name: "api", State: "active"},
		{Name: "worker", State: "completed"},
		{Name: "idle", State: ""},
	}
	store := bindings.New(dirs)
	active := daemonBinding(t, "row-api", host, "api")
	active.Pane = "right"
	active.PaneID = "surface-api"
	completed := daemonBinding(t, "row-worker", host, "worker")
	dangling := daemonBinding(t, "row-gone", host, "gone")
	if err := store.Save([]bindings.Binding{active, completed, dangling}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	ui := newTask10UI(t, trace)
	sink := newTask10Sink(t, trace)
	factory := &task10Supervisors{}
	d := New(task10Config(t, dirs, r, ui, nil, sink, factory, store))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()
	supervisor := factory.latest(t)
	supervisor.changes <- bridge.StateUp

	waitForTask10(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.calls) == 2
	})
	gotTrace := trace.snapshot(t)
	wantPrefix := []string{"hud-close row-api", "hud-close row-worker", "hud-close row-gone", "sessions", "status row-api", "status row-worker"}
	if len(gotTrace) < len(wantPrefix) || !reflect.DeepEqual(gotTrace[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("resync trace = %#v, want prefix %#v", gotTrace, wantPrefix)
	}
	sink.mu.Lock()
	calls := append([]task10StatusCall(nil), sink.calls...)
	sink.mu.Unlock()
	if calls[0].target != active.Row || calls[0].args.Status != "active" || calls[0].args.Pane != "right" || calls[0].args.PaneID != "surface-api" || calls[0].args.AutoReset != nil {
		t.Fatalf("active resync call = %#v, want exact active args", calls[0])
	}
	if calls[1].target != completed.Row || calls[1].args.Status != "completed" || calls[1].args.AutoReset == nil || !*calls[1].args.AutoReset || calls[1].args.Blink != nil {
		t.Fatalf("completed resync call = %#v, want auto-reset without blink", calls[1])
	}

	supervisor.changes <- bridge.StateDown
	call := waitForTask10Call(t, ui.calls, "open", active.Row)
	if call.msg != host+": reconnecting…" {
		t.Fatalf("reconnecting HUD message = %q, want host message", call.msg)
	}
	waitForTask10Call(t, ui.calls, "open", completed.Row)
	if got := d.hostStatus(host).State; got != "down" {
		t.Fatalf("host state after down = %q, want down", got)
	}
}

// TestConnectingStateIsReportedButTriggersNothing pins the cosmetic finding
// from the first real-host run: a freshly started bridge read "down" for five
// seconds. It now reads "connecting" — and only reads: no resync (the bridge is
// unproven) and no reconnecting HUD (nothing that was up has been lost).
func TestConnectingStateIsReportedButTriggersNothing(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	trace := newTask10Trace(t)
	host := "host-c"
	r := newTask10Remote(t, trace)
	r.home[host] = "/home/test"
	store := bindings.New(dirs)
	if err := store.Save([]bindings.Binding{daemonBinding(t, "row-c", host, "api")}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	ui := newTask10UI(t, trace)
	factory := &task10Supervisors{}
	d := New(task10Config(t, dirs, r, ui, nil, newTask10Sink(t, trace), factory, store))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()
	supervisor := factory.latest(t)

	before := len(trace.snapshot(t))
	supervisor.changes <- bridge.StateConnecting
	waitForTask10(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.hosts[host] != nil && d.hosts[host].state == string(bridge.StateConnecting)
	})
	time.Sleep(50 * time.Millisecond)
	if got := trace.snapshot(t); len(got) != before {
		t.Fatalf("connecting triggered work: %#v", got[before:])
	}

	// Promotion from connecting resyncs exactly as promotion from down did.
	supervisor.changes <- bridge.StateUp
	waitForTask10(t, func() bool {
		for _, entry := range trace.snapshot(t) {
			if entry == "sessions" {
				return true
			}
		}
		return false
	})
}

func TestDaemonReconnectingHUDOncePerSplitRow(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	trace := newTask10Trace(t)
	r := newTask10Remote(t, trace)
	host := "host-a"
	r.home[host] = "/home/test"
	r.sessions[host] = []remote.Session{{Name: "a", State: "active"}, {Name: "b", State: "completed"}}
	store := bindings.New(dirs)
	left := daemonBinding(t, "row-1", host, "a")
	right := daemonBinding(t, "row-1", host, "b")
	right.Pane = "right"
	right.PaneID = "pane-row-1-right"
	if err := store.Save([]bindings.Binding{left, right}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	ui := newTask10UI(t, trace)
	sink := newTask10Sink(t, trace)
	factory := &task10Supervisors{}
	d := New(task10Config(t, dirs, r, ui, nil, sink, factory, store))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()
	supervisor := factory.latest(t)
	supervisor.changes <- bridge.StateUp
	waitForTask10(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.calls) == 2
	})
	gotTrace := trace.snapshot(t)
	wantPrefix := []string{"hud-close row-1", "sessions", "status row-1", "status row-1"}
	if len(gotTrace) < len(wantPrefix) || !reflect.DeepEqual(gotTrace[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("resync trace = %#v, want prefix %#v", gotTrace, wantPrefix)
	}
	sink.mu.Lock()
	panes := []string{sink.calls[0].args.PaneID, sink.calls[1].args.PaneID}
	sink.mu.Unlock()
	if !reflect.DeepEqual(panes, []string{left.PaneID, right.PaneID}) {
		t.Fatalf("resync status panes = %#v, want one per pane", panes)
	}

	supervisor.changes <- bridge.StateDown
	waitForTask10Call(t, ui.calls, "open", "row-1")
	time.Sleep(100 * time.Millisecond)
	opens := 0
	for _, entry := range trace.snapshot(t) {
		if entry == "hud-open row-1" {
			opens++
		}
	}
	if opens != 1 {
		t.Fatalf("reconnecting HUD opened %d times for one split row, want 1; trace=%#v", opens, trace.snapshot(t))
	}
}

func TestFinishedSince(t *testing.T) {
	t.Helper()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	leftUp := now.Add(-time.Hour)
	tests := []struct {
		name     string
		idleSecs int
		want     bool
	}{
		{name: "unknown age replays", idleSecs: -1, want: true},
		{name: "after the bridge went down", idleSecs: 60, want: true},
		{name: "within the slack before it went down", idleSecs: 3605, want: true},
		{name: "long before it went down", idleSecs: 7200, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := finishedSince(now, tt.idleSecs, leftUp); got != tt.want {
				t.Fatalf("finishedSince(idle %d) = %v, want %v", tt.idleSecs, got, tt.want)
			}
		})
	}
}

// TestResyncSkipsFinishesAlreadyShown: a completed state older than the
// bridge's last time up was shown then, so a reconnect must not bring it back.
func TestResyncSkipsFinishesAlreadyShown(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	trace := newTask10Trace(t)
	r := newTask10Remote(t, trace)
	host := "host-a"
	r.home[host] = "/home/test"
	r.sessions[host] = []remote.Session{
		{Name: "stale", State: "completed", IdleSecs: 7200},
		{Name: "fresh", State: "completed", IdleSecs: 5},
		{Name: "busy", State: "active", IdleSecs: 7200},
	}
	store := bindings.New(dirs)
	rows := []bindings.Binding{
		daemonBinding(t, "row-stale", host, "stale"),
		daemonBinding(t, "row-fresh", host, "fresh"),
		daemonBinding(t, "row-busy", host, "busy"),
	}
	if err := store.Save(rows); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	leftUpPath := dirs.HostLeftUp(token.FileKey(host))
	if err := os.MkdirAll(filepath.Dir(leftUpPath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	stamp := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if err := os.WriteFile(leftUpPath, []byte(stamp+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	ui := newTask10UI(t, trace)
	sink := newTask10Sink(t, trace)
	factory := &task10Supervisors{}
	d := New(task10Config(t, dirs, r, ui, nil, sink, factory, store))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = d.Close() }()
	supervisor := factory.latest(t)
	supervisor.changes <- bridge.StateUp

	waitForTask10(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.calls) == 2
	})
	time.Sleep(50 * time.Millisecond)
	sink.mu.Lock()
	var got []string
	for _, call := range sink.calls {
		got = append(got, call.target+" "+call.args.Status)
	}
	sink.mu.Unlock()
	want := []string{"row-fresh completed", "row-busy active"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resync statuses = %#v, want %#v", got, want)
	}

	before := time.Now()
	supervisor.changes <- bridge.StateDown
	waitForTask10(t, func() bool {
		recorded, ok := d.leftUp(token.FileKey(host))
		return ok && !recorded.Before(before.Add(-time.Second))
	})
}
