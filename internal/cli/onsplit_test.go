package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
)

type panesFake struct {
	tree     map[string][]agterm.Surface
	treeErr  error
	texts    []string
	typed    []string
	typedIn  []string
	textHits int
}

func (f *panesFake) Surfaces(context.Context) (map[string][]agterm.Surface, error) {
	return f.tree, f.treeErr
}

// PaneText plays texts in order and then keeps returning the last one, the
// way a drawn prompt stays on screen.
func (f *panesFake) PaneText(context.Context, string, string) (string, error) {
	f.textHits++
	if len(f.texts) == 0 {
		return "", nil
	}
	text := f.texts[0]
	if len(f.texts) > 1 {
		f.texts = f.texts[1:]
	}
	return text, nil
}

func (f *panesFake) TypeLine(_ context.Context, row, paneID, text string) error {
	f.typed = append(f.typed, text)
	f.typedIn = append(f.typedIn, row+"/"+paneID)
	return nil
}

type cwdFake struct {
	dir   string
	err   error
	calls []string
}

func (f *cwdFake) Cwd(_ context.Context, host, name string) (string, error) {
	f.calls = append(f.calls, host+" "+name)
	return f.dir, f.err
}

type handledFake struct{ claimed map[string]bool }

func (f *handledFake) Claim(paneID string, _ []string) (bool, error) {
	if f.claimed == nil {
		f.claimed = map[string]bool{}
	}
	already := f.claimed[paneID]
	f.claimed[paneID] = true
	return already, nil
}

func splitTree(t *testing.T) map[string][]agterm.Surface {
	t.Helper()
	return map[string][]agterm.Surface{"row-1": {
		{Kind: "left", PaneID: "L", Visible: true},
		{Kind: "right", PaneID: "R", Visible: true},
	}}
}

func newPaneHookDeps(t *testing.T, panes *panesFake, cwd *cwdFake, bound ...bindings.Binding) PaneHookDependencies {
	t.Helper()
	store := bindings.New(pathstest.Dirs(t))
	for _, binding := range bound {
		if err := store.Bind(binding); err != nil {
			t.Fatalf("Bind() error = %v", err)
		}
	}
	return PaneHookDependencies{
		Panes: panes, Store: store, Remote: cwd, Handled: &handledFake{},
		Agr: "/Users/me/go/bin/agr", PromptWait: time.Second, PromptPoll: 100 * time.Millisecond,
		Sleep: func(time.Duration) {},
	}
}

func leftBinding(t *testing.T) bindings.Binding {
	t.Helper()
	return bindings.Binding{Row: "row-1", PaneID: "L", Pane: "left", Host: "homelab", Name: "a1", Mux: "tmux"}
}

func TestOnSplitOpensChildSessionInTheParentsDirectory(t *testing.T) {
	t.Helper()
	panes := &panesFake{tree: splitTree(t), texts: []string{"", "$ "}}
	cwd := &cwdFake{dir: "/srv/it's here"}
	deps := newPaneHookDeps(t, panes, cwd, leftBinding(t))

	if err := OnSplit(context.Background(), "row-1", "shown", deps); err != nil {
		t.Fatalf("OnSplit() error = %v", err)
	}
	want := []string{`'/Users/me/go/bin/agr' 'open' 'homelab' 'a1-2' '--cwd' '/srv/it'\''s here' '--parent' 'a1'`}
	if !reflect.DeepEqual(panes.typed, want) || !reflect.DeepEqual(panes.typedIn, []string{"row-1/R"}) {
		t.Fatalf("typed %#v into %#v, want %#v into row-1/R", panes.typed, panes.typedIn, want)
	}
	if !reflect.DeepEqual(cwd.calls, []string{"homelab a1"}) {
		t.Fatalf("cwd calls = %#v", cwd.calls)
	}
	if panes.textHits != 3 {
		t.Fatalf("prompt polls = %d, want to stop once the prompt reads the same twice", panes.textHits)
	}
}

func TestOnSplitOmitsAttachOptionsForARemoteWithoutCwd(t *testing.T) {
	t.Helper()
	panes := &panesFake{tree: splitTree(t), texts: []string{"$ "}}
	deps := newPaneHookDeps(t, panes, &cwdFake{err: errors.New("remote agr is not installed")}, leftBinding(t))
	if err := OnSplit(context.Background(), "row-1", "shown", deps); err != nil {
		t.Fatalf("OnSplit() error = %v", err)
	}
	want := []string{`'/Users/me/go/bin/agr' 'open' 'homelab' 'a1-2'`}
	if !reflect.DeepEqual(panes.typed, want) {
		t.Fatalf("typed = %#v, want %#v", panes.typed, want)
	}
}

func TestOnSplitKeepsParentWhenTheDirectoryIsUnknown(t *testing.T) {
	t.Helper()
	panes := &panesFake{tree: splitTree(t), texts: []string{"$ "}}
	deps := newPaneHookDeps(t, panes, &cwdFake{}, leftBinding(t))
	if err := OnSplit(context.Background(), "row-1", "shown", deps); err != nil {
		t.Fatalf("OnSplit() error = %v", err)
	}
	want := []string{`'/Users/me/go/bin/agr' 'open' 'homelab' 'a1-2' '--parent' 'a1'`}
	if !reflect.DeepEqual(panes.typed, want) {
		t.Fatalf("typed = %#v, want %#v", panes.typed, want)
	}
}

func TestOnSplitTypesEvenWhenThePromptNeverDraws(t *testing.T) {
	t.Helper()
	panes := &panesFake{tree: splitTree(t)}
	deps := newPaneHookDeps(t, panes, &cwdFake{dir: "/srv"}, leftBinding(t))
	if err := OnSplit(context.Background(), "row-1", "shown", deps); err != nil {
		t.Fatalf("OnSplit() error = %v", err)
	}
	if len(panes.typed) != 1 || panes.textHits != 10 {
		t.Fatalf("typed %d lines after %d polls, want 1 after 10", len(panes.typed), panes.textHits)
	}
}

func TestOnSplitWaitsOutAScrollingBanner(t *testing.T) {
	t.Helper()
	panes := &panesFake{tree: splitTree(t), texts: []string{"Last login", "Last login\nmotd", "Last login\nmotd\n$ "}}
	deps := newPaneHookDeps(t, panes, &cwdFake{dir: "/srv"}, leftBinding(t))
	if err := OnSplit(context.Background(), "row-1", "shown", deps); err != nil {
		t.Fatalf("OnSplit() error = %v", err)
	}
	if panes.textHits != 4 || len(panes.typed) != 1 {
		t.Fatalf("polls = %d, typed = %d; want 4 polls, then one line", panes.textHits, len(panes.typed))
	}
}

func TestOnSplitRefusesANonPrintableLine(t *testing.T) {
	t.Helper()
	panes := &panesFake{tree: splitTree(t), texts: []string{"$ "}}
	deps := newPaneHookDeps(t, panes, &cwdFake{dir: "/srv"}, leftBinding(t))
	deps.Agr = "/bin/agr\x1b[2J"
	if err := OnSplit(context.Background(), "row-1", "shown", deps); err == nil {
		t.Fatal("OnSplit() typed a line with an escape byte")
	}
	if len(panes.typed) != 0 {
		t.Fatalf("typed = %#v, want nothing", panes.typed)
	}
}

func TestOnSplitLeavesThePaneAlone(t *testing.T) {
	t.Helper()
	hiddenSplit := map[string][]agterm.Surface{"row-1": {{Kind: "left", PaneID: "L", Visible: true}, {Kind: "right", PaneID: "R"}}}
	noSplit := map[string][]agterm.Surface{"row-1": {{Kind: "left", PaneID: "L", Visible: true}}}
	rightBound := bindings.Binding{Row: "row-1", PaneID: "R", Pane: "right", Host: "homelab", Name: "other", Mux: "tmux"}
	for _, tc := range []struct {
		name   string
		status string
		tree   map[string][]agterm.Surface
		bound  []bindings.Binding
	}{
		{name: "hidden event", status: "hidden", tree: splitTree(t), bound: []bindings.Binding{leftBinding(t)}},
		{name: "local row", status: "shown", tree: splitTree(t)},
		{name: "hidden split pane", status: "shown", tree: hiddenSplit, bound: []bindings.Binding{leftBinding(t)}},
		{name: "no split pane", status: "shown", tree: noSplit, bound: []bindings.Binding{leftBinding(t)}},
		{name: "row gone", status: "shown", tree: map[string][]agterm.Surface{}, bound: []bindings.Binding{leftBinding(t)}},
		{name: "split already bound", status: "shown", tree: splitTree(t), bound: []bindings.Binding{leftBinding(t), rightBound}},
		{name: "only the right pane bound", status: "shown", tree: splitTree(t), bound: []bindings.Binding{rightBound}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			panes := &panesFake{tree: tc.tree, texts: []string{"$ "}}
			cwd := &cwdFake{dir: "/srv"}
			deps := newPaneHookDeps(t, panes, cwd, tc.bound...)
			if err := OnSplit(context.Background(), "row-1", tc.status, deps); err != nil {
				t.Fatalf("OnSplit() error = %v", err)
			}
			if len(panes.typed) != 0 || len(cwd.calls) != 0 {
				t.Fatalf("typed %#v, cwd calls %#v; want nothing", panes.typed, cwd.calls)
			}
		})
	}
}

func TestOnSplitActsOncePerPane(t *testing.T) {
	t.Helper()
	panes := &panesFake{tree: splitTree(t), texts: []string{"$ ", "$ "}}
	deps := newPaneHookDeps(t, panes, &cwdFake{dir: "/srv"}, leftBinding(t))
	for range 2 {
		if err := OnSplit(context.Background(), "row-1", "shown", deps); err != nil {
			t.Fatalf("OnSplit() error = %v", err)
		}
	}
	if len(panes.typed) != 1 {
		t.Fatalf("typed %d times across a hide/show, want 1", len(panes.typed))
	}
}

func TestOnSplitMatchesALegacyBindingBySlot(t *testing.T) {
	t.Helper()
	legacy := bindings.Binding{Row: "row-1", Host: "homelab", Name: "a1", Mux: "tmux"}
	panes := &panesFake{tree: splitTree(t), texts: []string{"$ "}}
	deps := newPaneHookDeps(t, panes, &cwdFake{dir: "/srv"}, legacy)
	if err := OnSplit(context.Background(), "row-1", "shown", deps); err != nil {
		t.Fatalf("OnSplit() error = %v", err)
	}
	if len(panes.typed) != 1 {
		t.Fatalf("typed = %#v, want the child open for a pre-token binding", panes.typed)
	}
}

func TestOnSplitRejectsAnInvalidRowAndReportsTreeErrors(t *testing.T) {
	t.Helper()
	deps := newPaneHookDeps(t, &panesFake{treeErr: errors.New("no agterm")}, &cwdFake{})
	if err := OnSplit(context.Background(), "../row", "shown", deps); err == nil {
		t.Fatal("OnSplit() with invalid row error = nil")
	}
	if err := OnSplit(context.Background(), "row-1", "shown", deps); err == nil {
		t.Fatal("OnSplit() with unreadable tree error = nil")
	}
}

func TestHookEventPrefersStdinAndFallsBackToEnv(t *testing.T) {
	t.Helper()
	env := func(key string) string {
		return map[string]string{"AGT_SESSION_ID": "env-row", "AGT_EVENT_STATUS": "hidden"}[key]
	}
	event := `{"kind":"pane.split","payload":{"name":"x","status":"shown"},"session":"stdin-row","seq":1}`
	if row, status := HookEvent(strings.NewReader(event), env); row != "stdin-row" || status != "shown" {
		t.Fatalf("HookEvent(stdin) = (%q, %q)", row, status)
	}
	if row, status := HookEvent(strings.NewReader("not json"), env); row != "env-row" || status != "hidden" {
		t.Fatalf("HookEvent(bad stdin) = (%q, %q), want env", row, status)
	}
	if row, status := HookEvent(nil, env); row != "env-row" || status != "hidden" {
		t.Fatalf("HookEvent(nil) = (%q, %q), want env", row, status)
	}
	if row, status := HookEvent(strings.NewReader(`{"payload":{}}`), env); row != "env-row" || status != "hidden" {
		t.Fatalf("HookEvent(partial) = (%q, %q), want env fill-in", row, status)
	}
}
