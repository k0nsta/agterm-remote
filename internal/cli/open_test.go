package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

type openRemoteFake struct {
	items   []remote.Session
	path    string
	calls   []string
	err     error
	pathErr error
}

func (f *openRemoteFake) Sessions(_ context.Context, host string) ([]remote.Session, error) {
	f.calls = append(f.calls, "sessions "+host)
	return f.items, f.err
}

func (f *openRemoteFake) Reap(context.Context, string, string) error { return nil }

func (f *openRemoteFake) ResolveAgrPath(_ context.Context, host string) (string, error) {
	f.calls = append(f.calls, "path "+host)
	if f.pathErr != nil {
		return "", f.pathErr
	}
	return f.path, nil
}

type openBridgeFake struct {
	store *bindings.Store
	calls []string
	err   error
}

func (f *openBridgeFake) ReloadBindings(context.Context) error {
	bound, err := f.store.Load()
	if err != nil {
		return err
	}
	name := ""
	if len(bound) > 0 {
		name = bound[0].Name
	}
	f.calls = append(f.calls, "reload "+name)
	return f.err
}

func (f *openBridgeFake) Up(_ context.Context, host string) error {
	bound, err := f.store.Load()
	if err != nil {
		return err
	}
	name := ""
	if len(bound) > 0 {
		name = bound[0].Name
	}
	f.calls = append(f.calls, "up "+host+" "+name)
	return f.err
}

type openTTYFake struct {
	argv  []string
	calls int
	err   error
}

func (f *openTTYFake) Interactive(_ context.Context, argv ...string) error {
	f.argv = append([]string(nil), argv...)
	f.calls++
	return f.err
}

type openPickerFake struct {
	result agterm.PickResult
	err    error
	items  []agterm.PickItem
	prompt string
}

func (f *openPickerFake) Pick(_ context.Context, items []agterm.PickItem, prompt string) (agterm.PickResult, error) {
	f.items = append([]agterm.PickItem(nil), items...)
	f.prompt = prompt
	return f.result, f.err
}

type openLabelerFake struct {
	calls       []string
	renameError error
	contextErr  error
}

func (f *openLabelerFake) Rename(_ context.Context, row, name string) error {
	f.calls = append(f.calls, "rename "+row+" "+name)
	return f.renameError
}

func (f *openLabelerFake) Context(_ context.Context, row, purpose string) error {
	f.calls = append(f.calls, "context "+row+" "+purpose)
	return f.contextErr
}

type openRowsFake struct{ items []string }

func (f *openRowsFake) Tree(context.Context) ([]string, error) {
	return append([]string(nil), f.items...), nil
}

func newOpenDependencies(t *testing.T, remoteFake *openRemoteFake, bridgeFake *openBridgeFake, tty *openTTYFake) OpenDependencies {
	t.Helper()
	return OpenDependencies{
		Dirs: pathstest.Dirs(t), Remote: remoteFake, Bridge: bridgeFake, TTY: tty,
		HostInfo: func(string) (remote.HostInfo, error) { return remote.HostInfo{Mux: "tmux"}, nil },
		Out:      new(strings.Builder), ErrOut: new(strings.Builder),
	}
}

func TestOpenBindsBeforeReloadAndUpAndUsesSSHArgv(t *testing.T) {
	t.Helper()
	t.Setenv("AGTERM_SESSION_ID", "row-1")
	t.Setenv("AGTERM_PANE_ID", "pane-1")
	t.Setenv("AGTERM_PANE", "left")

	dirs := pathstest.Dirs(t)
	store := bindings.New(dirs)
	remoteFake := &openRemoteFake{path: "/home/remote/.local/bin/agr"}
	bridgeFake := &openBridgeFake{store: store}
	tty := &openTTYFake{}
	deps := newOpenDependencies(t, remoteFake, bridgeFake, tty)
	deps.Dirs = dirs
	deps.Store = store
	labeler := &openLabelerFake{}
	deps.Labeler = labeler

	if err := Open(context.Background(), "user@example.com", "api", deps); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got, want := bridgeFake.calls, []string{"reload api", "up user@example.com api"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bridge calls = %#v, want %#v", got, want)
	}
	bound, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	wantBinding := bindings.Binding{Row: "row-1", PaneID: "pane-1", Pane: "left", Host: "user@example.com", Name: "api", Mux: "tmux"}
	if len(bound) != 1 || bound[0].Row != wantBinding.Row || bound[0].PaneID != wantBinding.PaneID || bound[0].Pane != wantBinding.Pane || bound[0].Host != wantBinding.Host || bound[0].Name != wantBinding.Name || bound[0].Mux != wantBinding.Mux {
		t.Fatalf("binding = %#v, want %#v", bound, wantBinding)
	}
	// The ssh remote command is one quoted string: the remote login shell
	// re-parses whatever ssh sends, so a path with a space or `;` must not
	// arrive as separate words. The mosh case below stays unquoted, because
	// mosh-server execs argv directly.
	wantArgv := []string{"ssh", "-t", "user@example.com", "--", `'/home/remote/.local/bin/agr' 'attach' 'api'`}
	if !reflect.DeepEqual(tty.argv, wantArgv) {
		t.Fatalf("TTY argv = %#v, want %#v", tty.argv, wantArgv)
	}
	if got, want := labeler.calls, []string{"rename row-1 api", "context row-1 user@example.com · api"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("labeler calls = %#v, want %#v", got, want)
	}
}

func TestOpenUsesMoshWhenBothSidesSupportIt(t *testing.T) {
	t.Helper()
	t.Setenv("AGTERM_SESSION_ID", "")
	dirs := pathstest.Dirs(t)
	store := bindings.New(dirs)
	remoteFake := &openRemoteFake{path: "/home/remote/.local/bin/agr"}
	bridgeFake := &openBridgeFake{store: store}
	tty := &openTTYFake{}
	deps := newOpenDependencies(t, remoteFake, bridgeFake, tty)
	deps.Store = store
	deps.HostInfo = func(string) (remote.HostInfo, error) {
		return remote.HostInfo{Mux: "zmx", Mosh: true}, nil
	}
	deps.MoshPath = func() string { return "/usr/local/bin/mosh" }

	if err := Open(context.Background(), "host", "api", deps); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	want := []string{"mosh", "host", "--", "/home/remote/.local/bin/agr", "attach", "api"}
	if !reflect.DeepEqual(tty.argv, want) {
		t.Fatalf("TTY argv = %#v, want %#v", tty.argv, want)
	}
	if _, ok := func() (bindings.Binding, bool) { return store.ByRow("row-1") }(); ok {
		t.Fatal("outside-agterm open wrote a binding")
	}
}

func TestOpenPickerCancelHasNoSideEffects(t *testing.T) {
	t.Helper()
	t.Setenv("AGTERM_SESSION_ID", "row-1")
	dirs := pathstest.Dirs(t)
	store := bindings.New(dirs)
	remoteFake := &openRemoteFake{items: []remote.Session{{Name: "api", Cmds: "claude"}}, path: "/agr"}
	bridgeFake := &openBridgeFake{store: store}
	tty := &openTTYFake{}
	picker := &openPickerFake{err: agterm.ErrCancelled}
	deps := newOpenDependencies(t, remoteFake, bridgeFake, tty)
	deps.Store, deps.Picker, deps.Rows = store, picker, &openRowsFake{items: []string{"row-1"}}
	var errout strings.Builder
	deps.ErrOut = &errout

	if code := RunOpen(context.Background(), "host", "", deps, &errout); code != 1 {
		t.Fatalf("RunOpen() exit = %d, want 1", code)
	}
	if tty.calls != 0 || len(bridgeFake.calls) != 0 {
		t.Fatalf("side effects after cancellation: tty=%d bridge=%v", tty.calls, bridgeFake.calls)
	}
	bound, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(bound) != 0 {
		t.Fatalf("bindings after cancellation = %#v, want empty", bound)
	}
}

func TestOpenOutsideAgtermDoesNotBindOrLabel(t *testing.T) {
	t.Helper()
	t.Setenv("AGTERM_SESSION_ID", "")
	dirs := pathstest.Dirs(t)
	store := bindings.New(dirs)
	remoteFake := &openRemoteFake{path: "/agr"}
	bridgeFake := &openBridgeFake{store: store}
	tty := &openTTYFake{}
	labeler := &openLabelerFake{}
	deps := newOpenDependencies(t, remoteFake, bridgeFake, tty)
	deps.Store, deps.Labeler = store, labeler

	if err := Open(context.Background(), "host", "api", deps); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got, want := bridgeFake.calls, []string{"up host "}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bridge calls = %#v, want %#v", got, want)
	}
	if len(labeler.calls) != 0 {
		t.Fatalf("labeler calls = %#v, want none", labeler.calls)
	}
	bound, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(bound) != 0 {
		t.Fatalf("bindings = %#v, want empty", bound)
	}
}

func TestOpenContextFailureIsBestEffort(t *testing.T) {
	t.Helper()
	t.Setenv("AGTERM_SESSION_ID", "row-1")
	dirs := pathstest.Dirs(t)
	store := bindings.New(dirs)
	bridgeFake := &openBridgeFake{store: store}
	labeler := &openLabelerFake{contextErr: errors.New("unknown subcommand context")}
	deps := newOpenDependencies(t, &openRemoteFake{path: "/agr"}, bridgeFake, &openTTYFake{})
	deps.Store, deps.Labeler = store, labeler
	var errout strings.Builder
	deps.ErrOut = &errout

	if code := RunOpen(context.Background(), "host", "api", deps, &errout); code != 0 {
		t.Fatalf("RunOpen() exit = %d, want zero: %s", code, errout.String())
	}
	if !strings.Contains(errout.String(), "unknown subcommand context") {
		t.Fatalf("stderr = %q, want best-effort warning", errout.String())
	}
}
