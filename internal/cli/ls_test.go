package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

type fakeCLISessions struct {
	items []remote.Session
	err   error
	calls []string
}

func (f *fakeCLISessions) Sessions(_ context.Context, _ string) ([]remote.Session, error) {
	return f.items, f.err
}

func (f *fakeCLISessions) Reap(_ context.Context, _, name string) error {
	f.calls = append(f.calls, name)
	return nil
}

type fakeCLIRows struct {
	items []string
	err   error
}

func (f *fakeCLIRows) Tree(_ context.Context) ([]string, error) {
	return f.items, f.err
}

func TestListRendersGoldenWithFooter(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	store := bindings.New(dirs)
	for _, item := range []bindings.Binding{
		{Row: "row-live", Host: "home", Name: "api"},
		{Row: "row-stale", Host: "home", Name: "infra"},
		{Row: "other-row", Host: "other", Name: "infra"},
	} {
		if err := store.Bind(item); err != nil {
			t.Fatalf("Bind(%q): %v", item.Name, err)
		}
	}
	source := &fakeCLISessions{items: []remote.Session{
		{Name: "api", Attached: 1, IdleSecs: 42, State: "active", Cmds: "claude"},
		{Name: "infra", IdleSecs: 180, Cmds: "-"},
	}}
	output, err := List(context.Background(), "home", source, store, &fakeCLIRows{items: []string{"row-live"}})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	wantData, err := os.ReadFile(filepath.Join("testdata", "ls_golden.txt"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if output != string(wantData) {
		t.Fatalf("List() = %q, want %q", output, wantData)
	}
}

func TestListKeepsRowsUnknownWhenTreeUnavailable(t *testing.T) {
	t.Helper()
	store := bindings.New(paths.TestDirs(t))
	if err := store.Bind(bindings.Binding{Row: "row-live", Host: "home", Name: "api"}); err != nil {
		t.Fatalf("Bind(): %v", err)
	}
	output, err := List(context.Background(), "home", &fakeCLISessions{items: []remote.Session{{Name: "api", IdleSecs: -1, State: "blocked", Cmds: "codex"}}}, store, &fakeCLIRows{err: errors.New("agterm unavailable")})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !strings.Contains(output, "NAME             ATT  IDLE   STATE      CMD              ROW\n") || !strings.Contains(output, "api              -    -      blocked    codex            -\n") {
		t.Fatalf("tree-unavailable output = %q, want row state '-': output", output)
	}
	if strings.Contains(output, "rows without a session") {
		t.Fatalf("tree-unavailable output = %q, must not claim stale rows", output)
	}
}

func TestListEmptySessions(t *testing.T) {
	t.Helper()
	output, err := List(context.Background(), "home", &fakeCLISessions{}, bindings.New(paths.TestDirs(t)), nil)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if output != "no agr sessions on 'home'\n" {
		t.Fatalf("empty List() = %q, want no-session message", output)
	}
}

func TestHumanizeSecs(t *testing.T) {
	t.Helper()
	tests := map[int]string{-1: "-", 0: "0s", 59: "59s", 60: "1m", 3599: "59m", 3600: "1h", 86399: "23h", 86400: "1d"}
	for seconds, want := range tests {
		if got := HumanizeSecs(seconds); got != want {
			t.Errorf("HumanizeSecs(%d) = %q, want %q", seconds, got, want)
		}
	}
}
