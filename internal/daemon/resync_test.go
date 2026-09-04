package daemon

import (
	"context"
	"reflect"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/bridge"
	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

func TestDaemonResyncsLevelsAndHUDAcrossBridgeTransitions(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
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
