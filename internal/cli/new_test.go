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
	"github.com/k0nsta/agterm-remote/internal/remote"
)

// pickSequenceFake answers each Pick with the next result, recording what
// each picker offered.
type pickSequenceFake struct {
	results []agterm.PickResult
	errs    []error
	items   [][]agterm.PickItem
	prompts []string
}

func (f *pickSequenceFake) Pick(_ context.Context, items []agterm.PickItem, prompt string) (agterm.PickResult, error) {
	i := len(f.prompts)
	f.items = append(f.items, append([]agterm.PickItem(nil), items...))
	f.prompts = append(f.prompts, prompt)
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	if i >= len(f.results) {
		return agterm.PickResult{}, err
	}
	return f.results[i], err
}

type workspacesFake struct{ items []agterm.Workspace }

func (f *workspacesFake) Workspaces(context.Context) ([]agterm.Workspace, error) {
	return f.items, nil
}

type rowOpenerFake struct {
	rows []agterm.NewRow
	err  error
}

func (f *rowOpenerFake) NewRow(_ context.Context, row agterm.NewRow) (string, error) {
	f.rows = append(f.rows, row)
	return "row-new", f.err
}

type newFixture struct {
	deps   NewDependencies
	picker *pickSequenceFake
	remote *openRemoteFake
	opener *rowOpenerFake
	closer *rowCloserFake
	panes  *panesFake
}

func newNewFixture(t *testing.T, picks ...agterm.PickResult) newFixture {
	t.Helper()
	store := bindings.New(pathstest.Dirs(t))
	for _, binding := range []bindings.Binding{
		{Row: "row-0", Host: "zeta", Name: "z1"},
		{Row: "row-1", Host: "myhost", Name: "a1"},
	} {
		if err := store.Bind(binding); err != nil {
			t.Fatalf("Bind() error = %v", err)
		}
	}
	f := newFixture{
		picker: &pickSequenceFake{results: picks},
		remote: &openRemoteFake{items: []remote.Session{{Name: "a1"}}},
		opener: &rowOpenerFake{},
		closer: &rowCloserFake{},
		panes: &panesFake{tree: map[string][]agterm.Surface{"row-new": {{Kind: "left", PaneID: "P", Visible: true}}},
			texts: []string{"$"}},
	}
	f.deps = NewDependencies{
		Hosts: func() ([]string, error) { return []string{"user@box", "myhost"}, nil },
		Store: store, Remote: f.remote, Picker: f.picker,
		Workspaces: &workspacesFake{items: []agterm.Workspace{
			{ID: "W1", Name: "local", Rows: []string{"row-9"}},
			{ID: "W2", Name: "myhost - tools", Rows: []string{"row-0", "row-1"}},
		}},
		Opener: f.opener, Closer: f.closer, Panes: f.panes, Agr: "/opt/homebrew/bin/agr",
		PromptWait: time.Second, PromptPoll: 100 * time.Millisecond, Sleep: func(time.Duration) {},
	}
	return f
}

func picked(t *testing.T, id string) agterm.PickResult {
	t.Helper()
	return agterm.PickResult{Kind: "picked", ID: id}
}

func typed(t *testing.T, query string) agterm.PickResult {
	t.Helper()
	return agterm.PickResult{Kind: "custom", Query: query}
}

func labels(t *testing.T, items []agterm.PickItem) []string {
	t.Helper()
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Label)
	}
	return out
}

func TestNewOpensTheSessionInARowAfterTheCurrentOne(t *testing.T) {
	t.Helper()
	f := newNewFixture(t, picked(t, "myhost"), typed(t, "a2"), picked(t, "W2"))
	if err := New(context.Background(), "row-1", f.deps); err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// The row's own host first, then every other bound or probed one, once.
	if got, want := labels(t, f.picker.items[0]), []string{"myhost", "user@box", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("host picker = %v, want %v", got, want)
	}
	if got := f.remote.calls; !reflect.DeepEqual(got, []string{"sessions myhost"}) {
		t.Fatalf("remote calls = %v", got)
	}
	if got, want := labels(t, f.picker.items[2]), []string{"myhost - tools", "local"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace picker = %v, want %v", got, want)
	}
	if want := []agterm.NewRow{{Name: "a2", After: "row-1"}}; !reflect.DeepEqual(f.opener.rows, want) {
		t.Fatalf("NewRow() = %#v, want %#v", f.opener.rows, want)
	}
	if want := []string{"'/opt/homebrew/bin/agr' 'open' 'myhost' 'a2'"}; !reflect.DeepEqual(f.panes.typed, want) {
		t.Fatalf("typed = %q, want %q", f.panes.typed, want)
	}
	if want := []string{"row-new/P"}; !reflect.DeepEqual(f.panes.typedIn, want) {
		t.Fatalf("typed into %v, want %v", f.panes.typedIn, want)
	}
}

func TestNewPlacesTheRowInTheChosenWorkspace(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name      string
		workspace agterm.PickResult
		want      agterm.NewRow
	}{
		{name: "another workspace", workspace: picked(t, "W1"), want: agterm.NewRow{Name: "a1", Workspace: "W1"}},
		{name: "a new workspace", workspace: typed(t, " staging "), want: agterm.NewRow{Name: "a1", WorkspaceName: "staging"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNewFixture(t, picked(t, "myhost"), picked(t, "a1"), tc.workspace)
			if err := New(context.Background(), "row-1", f.deps); err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if want := []agterm.NewRow{tc.want}; !reflect.DeepEqual(f.opener.rows, want) {
				t.Fatalf("NewRow() = %#v, want %#v", f.opener.rows, want)
			}
		})
	}
}

func TestNewWithoutARowListsHostsSorted(t *testing.T) {
	t.Helper()
	f := newNewFixture(t, typed(t, "admin@new-box"), picked(t, "a1"), picked(t, "W1"))
	if err := New(context.Background(), "", f.deps); err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got, want := labels(t, f.picker.items[0]), []string{"myhost", "user@box", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("host picker = %v, want %v", got, want)
	}
	if f.picker.items[2][0].Subtitle != "" {
		t.Fatalf("workspace picker marks %v current with no row", f.picker.items[2][0])
	}
	if got := f.panes.typed; len(got) != 1 || !strings.Contains(got[0], "'admin@new-box'") {
		t.Fatalf("typed = %q, want the typed host", got)
	}
}

func TestNewCancelAtAnyPickerCreatesNoRow(t *testing.T) {
	t.Helper()
	for i := range 3 {
		f := newNewFixture(t, picked(t, "myhost"), picked(t, "a1"), picked(t, "W2"))
		f.picker.results[i] = agterm.PickResult{}
		f.picker.errs = make([]error, 3)
		f.picker.errs[i] = agterm.ErrCancelled
		var errw strings.Builder
		if code := RunNew(context.Background(), "row-1", f.deps, &errw); code != 0 || errw.Len() != 0 {
			t.Fatalf("cancel at picker %d: RunNew() = %d, %q; want 0 and no error", i, code, errw.String())
		}
		if len(f.opener.rows) != 0 || len(f.panes.typed) != 0 {
			t.Fatalf("cancel at picker %d created %v, typed %q", i, f.opener.rows, f.panes.typed)
		}
	}
}

func TestNewRejectsInvalidAnswers(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name  string
		picks []agterm.PickResult
		want  string
	}{
		{name: "host", picks: []agterm.PickResult{typed(t, "box;rm")}, want: "invalid remote host"},
		{name: "blank host", picks: []agterm.PickResult{typed(t, " ")}, want: "invalid remote host"},
		{name: "name", picks: []agterm.PickResult{picked(t, "myhost"), typed(t, "a b")}, want: "invalid name"},
		{name: "workspace", picks: []agterm.PickResult{picked(t, "myhost"), picked(t, "a1"), typed(t, "  ")}, want: "invalid workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNewFixture(t, tc.picks...)
			err := New(context.Background(), "row-1", f.deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New() error = %v, want %q", err, tc.want)
			}
			if len(f.opener.rows) != 0 {
				t.Fatalf("New() created %v", f.opener.rows)
			}
		})
	}
}

func TestNewClosesTheRowWhenItCannotTypeIntoIt(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name  string
		tree  map[string][]agterm.Surface
		treeE error
		want  string
	}{
		{name: "no pane", tree: map[string][]agterm.Surface{}, want: "has no pane"},
		{name: "tree failed", treeE: errors.New("socket gone"), want: "socket gone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNewFixture(t, picked(t, "myhost"), picked(t, "a1"), picked(t, "W2"))
			f.panes.tree, f.panes.treeErr = tc.tree, tc.treeE
			err := New(context.Background(), "row-1", f.deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New() error = %v, want %q", err, tc.want)
			}
			if want := []string{"row-new"}; !reflect.DeepEqual(f.closer.closed, want) {
				t.Fatalf("closed = %v, want %v", f.closer.closed, want)
			}
		})
	}
}

type slowSessionsFake struct{ openRemoteFake }

func (f *slowSessionsFake) Sessions(ctx context.Context, _ string) ([]remote.Session, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestNewTimesOutAnUnreachableHost(t *testing.T) {
	t.Helper()
	f := newNewFixture(t, picked(t, "myhost"))
	f.deps.Remote = &slowSessionsFake{}
	f.deps.SessionsTimeout = 10 * time.Millisecond
	err := New(context.Background(), "row-1", f.deps)
	if err == nil || !strings.Contains(err.Error(), "no answer within 10ms") {
		t.Fatalf("New() error = %v, want the timeout", err)
	}
	if len(f.opener.rows) != 0 {
		t.Fatalf("New() created %v", f.opener.rows)
	}
}

func TestNewRejectsAnInvalidRow(t *testing.T) {
	t.Helper()
	f := newNewFixture(t)
	if err := New(context.Background(), "row 1", f.deps); err == nil || !strings.Contains(err.Error(), "invalid session-id") {
		t.Fatalf("New() error = %v, want invalid session-id", err)
	}
}
