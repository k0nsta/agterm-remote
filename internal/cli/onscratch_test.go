package cli

import (
	"context"
	"reflect"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
)

func scratchTree(t *testing.T, visible bool) map[string][]agterm.Surface {
	t.Helper()
	return map[string][]agterm.Surface{"row-1": {
		{Kind: "left", PaneID: "L"},
		{Kind: "scratch", PaneID: "S", Visible: visible},
	}}
}

func TestOnScratchStartsARemoteShellInTheSessionsDirectory(t *testing.T) {
	t.Helper()
	panes := &panesFake{tree: scratchTree(t, true), texts: []string{"$ "}}
	cwd := &cwdFake{dir: "/srv/repo"}
	deps := newPaneHookDeps(t, panes, cwd, leftBinding(t))
	if err := OnScratch(context.Background(), "row-1", "shown", deps); err != nil {
		t.Fatalf("OnScratch() error = %v", err)
	}
	want := []string{`'exec' '/Users/me/go/bin/agr' 'shell' 'homelab' '--cwd' '/srv/repo'`}
	if !reflect.DeepEqual(panes.typed, want) || !reflect.DeepEqual(panes.typedIn, []string{"row-1/S"}) {
		t.Fatalf("typed %#v into %#v, want %#v into row-1/S", panes.typed, panes.typedIn, want)
	}
}

func TestOnScratchFallsBackToTheRemoteHome(t *testing.T) {
	t.Helper()
	panes := &panesFake{tree: scratchTree(t, true), texts: []string{"$ "}}
	deps := newPaneHookDeps(t, panes, &cwdFake{}, leftBinding(t))
	if err := OnScratch(context.Background(), "row-1", "shown", deps); err != nil {
		t.Fatalf("OnScratch() error = %v", err)
	}
	if want := []string{`'exec' '/Users/me/go/bin/agr' 'shell' 'homelab'`}; !reflect.DeepEqual(panes.typed, want) {
		t.Fatalf("typed = %#v, want %#v", panes.typed, want)
	}
}

func TestOnScratchLeavesTheScratchAlone(t *testing.T) {
	t.Helper()
	scratchBound := bindings.Binding{Row: "row-1", PaneID: "S", Pane: "scratch", Host: "homelab", Name: "x", Mux: "tmux"}
	for _, tc := range []struct {
		name  string
		tree  map[string][]agterm.Surface
		bound []bindings.Binding
	}{
		{name: "local row", tree: scratchTree(t, true)},
		{name: "hidden scratch", tree: scratchTree(t, false), bound: []bindings.Binding{leftBinding(t)}},
		{name: "scratch bound", tree: scratchTree(t, true), bound: []bindings.Binding{leftBinding(t), scratchBound}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			panes := &panesFake{tree: tc.tree, texts: []string{"$ "}}
			deps := newPaneHookDeps(t, panes, &cwdFake{dir: "/srv"}, tc.bound...)
			if err := OnScratch(context.Background(), "row-1", "shown", deps); err != nil {
				t.Fatalf("OnScratch() error = %v", err)
			}
			if len(panes.typed) != 0 {
				t.Fatalf("typed %#v, want nothing", panes.typed)
			}
		})
	}
}

func TestOnScratchActsOncePerScratch(t *testing.T) {
	t.Helper()
	panes := &panesFake{tree: scratchTree(t, true), texts: []string{"$ ", "$ "}}
	deps := newPaneHookDeps(t, panes, &cwdFake{dir: "/srv"}, leftBinding(t))
	for range 2 {
		if err := OnScratch(context.Background(), "row-1", "shown", deps); err != nil {
			t.Fatal(err)
		}
	}
	if len(panes.typed) != 1 {
		t.Fatalf("typed %d times, want 1", len(panes.typed))
	}
}
