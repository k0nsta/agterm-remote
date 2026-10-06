package cli

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

type reaperFake struct {
	calls []string
	errs  map[string]error
}

func (f *reaperFake) Reap(_ context.Context, host, name string) error {
	f.calls = append(f.calls, host+" "+name)
	return f.errs[name]
}

type confirmFake struct {
	answer bool
	err    error
	title  string
}

func (f *confirmFake) Confirm(_ context.Context, _, title, _, confirmID, _ string) (bool, error) {
	f.title = title
	if confirmID != "end" {
		return false, fmt.Errorf("confirm id %q", confirmID)
	}
	return f.answer, f.err
}

type rowCloserFake struct {
	closed []string
	err    error
}

func (f *rowCloserFake) CloseRow(_ context.Context, row string) error {
	f.closed = append(f.closed, row)
	return f.err
}

func newEndDeps(t *testing.T, confirm *confirmFake, reaper *reaperFake, bound ...bindings.Binding) (EndDependencies, *bindings.Store, *rowCloserFake) {
	t.Helper()
	store := bindings.New(pathstest.Dirs(t))
	for _, binding := range bound {
		if err := store.Bind(binding); err != nil {
			t.Fatalf("Bind() error = %v", err)
		}
	}
	rows := &rowCloserFake{}
	return EndDependencies{Store: store, Reaper: reaper, Confirm: confirm, Rows: rows}, store, rows
}

func sidePaneBindings(t *testing.T) []bindings.Binding {
	t.Helper()
	return []bindings.Binding{
		{Row: "row-1", PaneID: "L", Pane: "left", Host: "homelab", Name: "a1", Mux: "tmux"},
		{Row: "row-1", PaneID: "R", Pane: "right", Host: "homelab", Name: "a1-2", Mux: "tmux"},
		{Row: "row-2", PaneID: "X", Pane: "left", Host: "homelab", Name: "b1", Mux: "tmux"},
	}
}

func TestEndReapsTheRowsSessionsThenClosesTheRow(t *testing.T) {
	t.Helper()
	// a1-2 went with a1's cascade, so its own reap finds nothing: still ended.
	reaper := &reaperFake{errs: map[string]error{"a1-2": fmt.Errorf("%w: a1-2", remote.ErrNoSession)}}
	confirm := &confirmFake{answer: true}
	deps, store, rows := newEndDeps(t, confirm, reaper, sidePaneBindings(t)...)
	if err := End(context.Background(), "row-1", deps); err != nil {
		t.Fatalf("End() error = %v", err)
	}
	if want := []string{"homelab a1", "homelab a1-2"}; !reflect.DeepEqual(reaper.calls, want) {
		t.Fatalf("reaped %#v, want %#v", reaper.calls, want)
	}
	if !reflect.DeepEqual(rows.closed, []string{"row-1"}) {
		t.Fatalf("closed %#v, want row-1", rows.closed)
	}
	if len(store.ForRow("row-1")) != 0 || len(store.ForRow("row-2")) != 1 {
		t.Fatal("End() did not drop exactly the row's bindings")
	}
	if confirm.title != "End a1, a1-2 on homelab?" {
		t.Fatalf("title = %q", confirm.title)
	}
}

func TestEndKeepsTheRowWhenAReapFails(t *testing.T) {
	t.Helper()
	reaper := &reaperFake{errs: map[string]error{"a1": errors.New("remote command exited with status 1")}}
	deps, store, rows := newEndDeps(t, &confirmFake{answer: true}, reaper, sidePaneBindings(t)...)
	err := End(context.Background(), "row-1", deps)
	if err == nil || !strings.Contains(err.Error(), "row kept") {
		t.Fatalf("End() error = %v, want row kept", err)
	}
	if len(rows.closed) != 0 || len(store.ForRow("row-1")) != 2 {
		t.Fatal("a failed reap closed the row or dropped its bindings")
	}
}

func TestEndDoesNothingUnlessConfirmed(t *testing.T) {
	t.Helper()
	for _, confirm := range []*confirmFake{{answer: false}, {err: errors.New("dialog failed")}} {
		reaper := &reaperFake{}
		deps, _, rows := newEndDeps(t, confirm, reaper, sidePaneBindings(t)...)
		if err := End(context.Background(), "row-1", deps); err == nil {
			t.Fatal("End() without a confirm returned nil")
		}
		if len(reaper.calls) != 0 || len(rows.closed) != 0 {
			t.Fatalf("unconfirmed End() reaped %#v, closed %#v", reaper.calls, rows.closed)
		}
	}
	deps, _, _ := newEndDeps(t, &confirmFake{}, &reaperFake{}, sidePaneBindings(t)...)
	var errw strings.Builder
	if code := RunEnd(context.Background(), "row-1", deps, &errw); code != 0 || errw.Len() != 0 {
		t.Fatalf("RunEnd() on cancel = %d, %q; want 0 and silence", code, errw.String())
	}
}

func TestEndRefusesALocalOrInvalidRow(t *testing.T) {
	t.Helper()
	confirm := &confirmFake{answer: true}
	deps, _, _ := newEndDeps(t, confirm, &reaperFake{}, sidePaneBindings(t)...)
	for _, row := range []string{"row-9", "../x"} {
		if err := End(context.Background(), row, deps); err == nil {
			t.Fatalf("End(%q) error = nil", row)
		}
	}
	if confirm.title != "" {
		t.Fatal("End() asked to confirm a row with nothing to end")
	}
}

func TestEndNamesEveryHostWhenTheRowSpansHosts(t *testing.T) {
	t.Helper()
	bound := []bindings.Binding{
		{Row: "row-1", PaneID: "L", Pane: "left", Host: "homelab", Name: "a1", Mux: "tmux"},
		{Row: "row-1", PaneID: "R", Pane: "right", Host: "otherhost", Name: "x", Mux: "tmux"},
	}
	confirm := &confirmFake{}
	deps, _, _ := newEndDeps(t, confirm, &reaperFake{}, bound...)
	_ = End(context.Background(), "row-1", deps)
	if want := "End a1 on homelab, x on otherhost?"; confirm.title != want {
		t.Fatalf("title = %q, want %q", confirm.title, want)
	}
}

func TestEndKeepsTheBindingsWhenTheRowWillNotClose(t *testing.T) {
	t.Helper()
	deps, store, rows := newEndDeps(t, &confirmFake{answer: true}, &reaperFake{}, sidePaneBindings(t)...)
	rows.err = errors.New("agtermctl session close (exit 1)")
	if err := End(context.Background(), "row-1", deps); err == nil {
		t.Fatal("End() error = nil, want the close failure")
	}
	if len(store.ForRow("row-1")) != 2 {
		t.Fatal("a failed close dropped the bindings, so a retry cannot find the row's sessions")
	}
}
