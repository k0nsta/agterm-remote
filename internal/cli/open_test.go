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

	if err := Open(context.Background(), "user@example.com", "api", OpenOptions{}, deps); err != nil {
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

	if err := Open(context.Background(), "host", "api", OpenOptions{}, deps); err != nil {
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

	if code := RunOpen(context.Background(), "host", "", OpenOptions{}, deps, &errout); code != 1 {
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

	if err := Open(context.Background(), "host", "api", OpenOptions{}, deps); err != nil {
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

	if code := RunOpen(context.Background(), "host", "api", OpenOptions{}, deps, &errout); code != 0 {
		t.Fatalf("RunOpen() exit = %d, want zero: %s", code, errout.String())
	}
	if !strings.Contains(errout.String(), "unknown subcommand context") {
		t.Fatalf("stderr = %q, want best-effort warning", errout.String())
	}
}

func TestOpenPassesCwdAndParentToAttach(t *testing.T) {
	t.Helper()
	t.Setenv("AGTERM_SESSION_ID", "")
	store := bindings.New(pathstest.Dirs(t))
	remoteFake := &openRemoteFake{path: "/home/remote/.local/bin/agr"}
	opts := OpenOptions{Cwd: "/srv/it's here", Parent: "api"}

	tty := &openTTYFake{}
	deps := newOpenDependencies(t, remoteFake, &openBridgeFake{store: store}, tty)
	deps.Store = store
	if err := Open(context.Background(), "host", "api-2", opts, deps); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	wantSSH := []string{"ssh", "-t", "host", "--", `'/home/remote/.local/bin/agr' 'attach' 'api-2' '--cwd' '/srv/it'\''s here' '--parent' 'api'`}
	if !reflect.DeepEqual(tty.argv, wantSSH) {
		t.Fatalf("ssh argv = %#v, want %#v", tty.argv, wantSSH)
	}

	deps.HostInfo = func(string) (remote.HostInfo, error) { return remote.HostInfo{Mux: "zmx", Mosh: true}, nil }
	deps.MoshPath = func() string { return "/usr/local/bin/mosh" }
	if err := Open(context.Background(), "host", "api-2", opts, deps); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	wantMosh := []string{"mosh", "host", "--", "/home/remote/.local/bin/agr", "attach", "api-2", "--cwd", "/srv/it's here", "--parent", "api"}
	if !reflect.DeepEqual(tty.argv, wantMosh) {
		t.Fatalf("mosh argv = %#v, want %#v", tty.argv, wantMosh)
	}
}

func TestOpenRejectsBadOptionsBeforeSideEffects(t *testing.T) {
	t.Helper()
	t.Setenv("AGTERM_SESSION_ID", "")
	store := bindings.New(pathstest.Dirs(t))
	for _, opts := range []OpenOptions{{Cwd: "relative"}, {Parent: "../x"}} {
		tty := &openTTYFake{}
		deps := newOpenDependencies(t, &openRemoteFake{path: "/a"}, &openBridgeFake{store: store}, tty)
		deps.Store = store
		if err := Open(context.Background(), "host", "api", opts, deps); err == nil {
			t.Fatalf("Open(%+v) error = nil", opts)
		}
		if tty.calls != 0 {
			t.Fatalf("Open(%+v) attached despite invalid options", opts)
		}
	}
}

func TestOpenWithParentLeavesTheRowLabelAlone(t *testing.T) {
	t.Helper()
	t.Setenv("AGTERM_SESSION_ID", "row-1")
	t.Setenv("AGTERM_PANE_ID", "pane-R")
	t.Setenv("AGTERM_PANE", "right")
	dirs := pathstest.Dirs(t)
	store := bindings.New(dirs)
	deps := newOpenDependencies(t, &openRemoteFake{path: "/a"}, &openBridgeFake{store: store}, &openTTYFake{})
	deps.Dirs, deps.Store = dirs, store
	labeler := &openLabelerFake{}
	deps.Labeler = labeler
	if err := Open(context.Background(), "host", "a1-2", OpenOptions{Parent: "a1"}, deps); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(labeler.calls) != 0 {
		t.Fatalf("labeler calls = %#v, want none for a side-pane session", labeler.calls)
	}
	if bound, ok := store.ByHostName("host", "a1-2"); !ok || bound.PaneID != "pane-R" {
		t.Fatalf("side-pane session binding = %#v, %v", bound, ok)
	}
}

type openPaneTreeFake struct {
	rows []agterm.TreeRow
	err  error
}

func (f *openPaneTreeFake) TreeRows(context.Context) ([]agterm.TreeRow, error) {
	return f.rows, f.err
}

func hostInfoWith(version string) HostInfoReader {
	return func(string) (remote.HostInfo, error) {
		return remote.HostInfo{Mux: "tmux", AgrVersion: version}, nil
	}
}

func setAgtermEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AGTERM_ENABLED", "1")
	t.Setenv("AGTERM_SESSION_ID", "0A1B2C3D-0000-4000-8000-000000000001")
	t.Setenv("AGTERM_PANE", "left")
	t.Setenv("AGTERM_PANE_ID", "0A1B2C3D-0000-4000-8000-0000000000AA")
	t.Setenv("AGTERM_WORKSPACE_ID", "0A1B2C3D-0000-4000-8000-0000000000BB")
	t.Setenv("AGTERM_WINDOW_ID", "0A1B2C3D-0000-4000-8000-0000000000CC")
	t.Setenv("AGTERM_SOCKET", "/tmp/agterm.sock")
}

func openFor(t *testing.T, version string, opts OpenOptions) (*openTTYFake, *bindings.Store, *strings.Builder, error) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	store := bindings.New(dirs)
	tty := &openTTYFake{}
	deps := newOpenDependencies(t, &openRemoteFake{path: "/r/agr"}, &openBridgeFake{store: store}, tty)
	deps.Dirs = dirs
	deps.Store = store
	deps.HostInfo = hostInfoWith(version)
	errout := new(strings.Builder)
	deps.ErrOut = errout
	err := Open(context.Background(), "host", "api", opts, deps)
	return tty, store, errout, err
}

func TestOpenSendsEnvAndCommandToANewEnoughRemote(t *testing.T) {
	t.Helper()
	setAgtermEnv(t)
	for _, version := range []string{"1.3.0", "v1.3.1", "1.10.0", "2.0.0", "dev"} {
		tty, _, _, err := openFor(t, version, OpenOptions{Cwd: "/srv", Parent: "web", Command: "echo 'a b'; $(id)"})
		if err != nil {
			t.Fatalf("%s: Open() error = %v", version, err)
		}
		want := []string{"ssh", "-t", "host", "--", `'/r/agr' 'attach' 'api' '--cwd' '/srv' '--parent' 'web'` +
			` '--env' 'AGTERM_ENABLED=1'` +
			` '--env' 'AGTERM_SESSION_ID=0A1B2C3D-0000-4000-8000-000000000001'` +
			` '--env' 'AGTERM_PANE=left'` +
			` '--env' 'AGTERM_PANE_ID=0A1B2C3D-0000-4000-8000-0000000000AA'` +
			` '--env' 'AGTERM_WORKSPACE_ID=0A1B2C3D-0000-4000-8000-0000000000BB'` +
			` '--env' 'AGTERM_WINDOW_ID=0A1B2C3D-0000-4000-8000-0000000000CC'` +
			` '--env' 'AGTERM_SOCKET=agr-proxy'` +
			` '--command' 'echo '\''a b'\''; $(id)'`}
		if !reflect.DeepEqual(tty.argv, want) {
			t.Fatalf("%s: argv =\n%q\nwant\n%q", version, tty.argv, want)
		}
	}
}

func TestOpenCommandArgvWithoutAgtermStaysPlain(t *testing.T) {
	t.Helper()
	t.Setenv("AGTERM_SESSION_ID", "")
	tty, store, _, err := openFor(t, "1.3.0", OpenOptions{Command: "make dev"})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	want := []string{"ssh", "-t", "host", "--", `'/r/agr' 'attach' 'api' '--command' 'make dev'`}
	if !reflect.DeepEqual(tty.argv, want) {
		t.Fatalf("argv = %q, want no env outside agterm: %q", tty.argv, want)
	}
	if bound, _ := store.Load(); len(bound) != 0 {
		t.Fatalf("bindings = %v, want none outside agterm", bound)
	}
}

func TestOpenOmitsUnsetAndInvalidEnvValues(t *testing.T) {
	t.Helper()
	setAgtermEnv(t)
	t.Setenv("AGTERM_WORKSPACE_ID", "")
	t.Setenv("AGTERM_WINDOW_ID", "x y;$(id)")
	t.Setenv("AGTERM_ENABLED", "")
	tty, _, errout, err := openFor(t, "1.3.0", OpenOptions{})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	joined := tty.argv[len(tty.argv)-1]
	for _, absent := range []string{"AGTERM_WORKSPACE_ID", "AGTERM_WINDOW_ID", "AGTERM_ENABLED", "$(id)"} {
		if strings.Contains(joined, absent) {
			t.Errorf("remote command %q must not carry %s", joined, absent)
		}
	}
	for _, present := range []string{"'AGTERM_SESSION_ID=", "'AGTERM_PANE=left'", "'AGTERM_SOCKET=agr-proxy'"} {
		if !strings.Contains(joined, present) {
			t.Errorf("remote command %q lacks %s", joined, present)
		}
	}
	if !strings.Contains(errout.String(), "AGTERM_WINDOW_ID") {
		t.Errorf("stderr = %q, want a warning for the invalid value", errout.String())
	}
}

func TestOpenOldRemoteGetsNoEnvAndRefusesCommand(t *testing.T) {
	t.Helper()
	setAgtermEnv(t)
	for _, version := range []string{"1.2.0", "1.2.9", "0.9.0", "", "snapshot", "garbage"} {
		tty, store, _, err := openFor(t, version, OpenOptions{})
		if err != nil {
			t.Fatalf("%q: Open() error = %v", version, err)
		}
		if want := []string{"ssh", "-t", "host", "--", `'/r/agr' 'attach' 'api'`}; !reflect.DeepEqual(tty.argv, want) {
			t.Fatalf("%q: argv = %q, want no env for an old remote", version, tty.argv)
		}
		if bound, _ := store.Load(); len(bound) != 1 {
			t.Fatalf("%q: bindings = %v, want the plain open still bound", version, bound)
		}

		tty, store, _, err = openFor(t, version, OpenOptions{Command: "make dev"})
		if err == nil || !strings.Contains(err.Error(), "agr install host") || !strings.Contains(err.Error(), "--command") {
			t.Fatalf("%q: Open(--command) error = %v, want a refusal naming agr install", version, err)
		}
		if tty.calls != 0 {
			t.Fatalf("%q: a refused --command still ran ssh", version)
		}
		if bound, _ := store.Load(); len(bound) != 0 {
			t.Fatalf("%q: a refused --command left a binding: %v", version, bound)
		}
	}
}

func TestSupportsEnvCommandGate(t *testing.T) {
	t.Helper()
	cases := map[string]bool{
		"1.2.0": false, "1.2.99": false, "1.3.0": true, "v1.3.0": true, "1.3.0-rc1": true,
		"1.4.0": true, "2.0.0": true, "dev": true, "": false, "snapshot": false, "1.3": false,
	}
	for version, want := range cases {
		if got := SupportsEnvCommand(version); got != want {
			t.Errorf("SupportsEnvCommand(%q) = %v, want %v", version, got, want)
		}
	}
}

const (
	orchRow  = "0A1B2C3D-0000-4000-8000-0000000000D1"
	orchPane = "0A1B2C3D-0000-4000-8000-0000000000D2"
)

func orchestratorDeps(t *testing.T, rows []agterm.TreeRow, existing ...bindings.Binding) (OpenDependencies, *bindings.Store, *openTTYFake) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	store := bindings.New(dirs)
	for _, binding := range existing {
		if err := store.Bind(binding); err != nil {
			t.Fatal(err)
		}
	}
	tty := &openTTYFake{}
	deps := newOpenDependencies(t, &openRemoteFake{path: "/r/agr"}, &openBridgeFake{store: store}, tty)
	deps.Dirs = dirs
	deps.Store = store
	deps.HostInfo = hostInfoWith("1.3.0")
	deps.Panes = &openPaneTreeFake{rows: rows}
	return deps, store, tty
}

func liveRows() []agterm.TreeRow {
	return []agterm.TreeRow{{ID: orchRow, Surfaces: []agterm.Surface{{Kind: "left", PaneID: orchPane, Visible: true}}}}
}

func TestOpenOrchestratorStoresAGrantOnTheNewBinding(t *testing.T) {
	t.Helper()
	setAgtermEnv(t)
	deps, store, tty := orchestratorDeps(t, liveRows())
	// The ids match case-insensitively; the stored grant carries the tree's.
	spec := strings.ToLower(orchRow) + ":" + strings.ToLower(orchPane)
	if err := Open(context.Background(), "host", "api", OpenOptions{Orchestrator: spec}, deps); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	got, ok := store.ByHostName("host", "api")
	want := []bindings.Grant{{Row: orchRow, PaneID: orchPane, Kind: bindings.GrantOrchestrator}}
	if !ok || !reflect.DeepEqual(got.Grants, want) {
		t.Fatalf("binding = %+v, %v; want grants %+v", got, ok, want)
	}
	if tty.calls != 1 || strings.Contains(tty.argv[len(tty.argv)-1], "orchestrator") {
		t.Fatalf("the grant must stay on the Mac: argv = %q", tty.argv)
	}
}

func TestOpenOrchestratorRefusals(t *testing.T) {
	t.Helper()
	setAgtermEnv(t)
	hostBound := bindings.Binding{Row: orchRow, PaneID: orchPane, Pane: "left", Host: "other", Name: "x", Mux: "tmux"}
	rowOnly := bindings.Binding{Row: orchRow, Pane: "left", Host: "other", Name: "y", Mux: "tmux"}
	otherPane := bindings.Binding{Row: orchRow, PaneID: "0A1B2C3D-0000-4000-8000-0000000000D9", Pane: "right", Host: "other", Name: "z", Mux: "tmux"}
	tests := []struct {
		name     string
		spec     string
		rows     []agterm.TreeRow
		existing []bindings.Binding
		treeErr  error
		want     string
	}{
		{name: "no colon", spec: orchRow, rows: liveRows(), want: "invalid --orchestrator"},
		{name: "empty pane", spec: orchRow + ":", rows: liveRows(), want: "invalid --orchestrator"},
		{name: "empty row", spec: ":" + orchPane, rows: liveRows(), want: "invalid --orchestrator"},
		{name: "extra colon", spec: orchRow + ":" + orchPane + ":x", rows: liveRows(), want: "invalid --orchestrator"},
		{name: "bad characters", spec: "a b:c", rows: liveRows(), want: "invalid --orchestrator"},
		{name: "unknown row", spec: "nope:" + orchPane, rows: liveRows(), want: "no such pane"},
		{name: "unknown pane", spec: orchRow + ":nope", rows: liveRows(), want: "no such pane"},
		{name: "pane of another row", spec: orchRow + ":" + orchPane, rows: []agterm.TreeRow{
			{ID: orchRow}, {ID: "elsewhere", Surfaces: []agterm.Surface{{PaneID: orchPane}}}}, want: "no such pane"},
		{name: "prefix of a pane id", spec: orchRow + ":" + orchPane[:8], rows: liveRows(), want: "no such pane"},
		{name: "host-bound pane", spec: orchRow + ":" + orchPane, rows: liveRows(), existing: []bindings.Binding{hostBound}, want: "bound to host other"},
		{name: "host-bound row without pane id", spec: orchRow + ":" + orchPane, rows: liveRows(), existing: []bindings.Binding{rowOnly}, want: "bound to host other"},
		{name: "this pane itself", spec: orchRow + ":0A1B2C3D-0000-4000-8000-0000000000AA", rows: []agterm.TreeRow{
			{ID: orchRow, Surfaces: []agterm.Surface{{PaneID: "0A1B2C3D-0000-4000-8000-0000000000AA"}}}}, want: "this pane itself"},
		{name: "tree unreadable", spec: orchRow + ":" + orchPane, treeErr: errors.New("socket gone"), want: "read agterm tree"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Helper()
			deps, store, tty := orchestratorDeps(t, test.rows, test.existing...)
			deps.Panes = &openPaneTreeFake{rows: test.rows, err: test.treeErr}
			err := Open(context.Background(), "host", "api", OpenOptions{Orchestrator: test.spec}, deps)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Open() error = %v, want %q", err, test.want)
			}
			if tty.calls != 0 {
				t.Fatal("a refused --orchestrator still ran ssh")
			}
			if _, bound := store.ByHostName("host", "api"); bound {
				t.Fatal("a refused --orchestrator left a binding")
			}
		})
	}
	// A split sibling bound to a host does not make the other pane host-bound.
	deps, store, _ := orchestratorDeps(t, []agterm.TreeRow{{ID: orchRow, Surfaces: []agterm.Surface{{PaneID: orchPane}, {PaneID: otherPane.PaneID}}}}, otherPane)
	if err := Open(context.Background(), "host", "api", OpenOptions{Orchestrator: orchRow + ":" + orchPane}, deps); err != nil {
		t.Fatalf("a local pane next to a host-bound one: Open() error = %v", err)
	}
	if got, _ := store.ByHostName("host", "api"); len(got.Grants) != 1 {
		t.Fatalf("binding = %+v, want the grant", got)
	}
}

func TestOpenOrchestratorNeedsAnAgtermRowAndATree(t *testing.T) {
	t.Helper()
	spec := orchRow + ":" + orchPane
	t.Setenv("AGTERM_SESSION_ID", "")
	deps, _, _ := orchestratorDeps(t, liveRows())
	if err := Open(context.Background(), "host", "api", OpenOptions{Orchestrator: spec}, deps); err == nil || !strings.Contains(err.Error(), "agterm row") {
		t.Fatalf("outside agterm: error = %v", err)
	}
	setAgtermEnv(t)
	deps, _, _ = orchestratorDeps(t, liveRows())
	deps.Panes = nil
	if err := Open(context.Background(), "host", "api", OpenOptions{Orchestrator: spec}, deps); err == nil || !strings.Contains(err.Error(), "agtermctl") {
		t.Fatalf("without a tree reader: error = %v", err)
	}
}

func TestReopenKeepsTheOrchestratorGrant(t *testing.T) {
	t.Helper()
	setAgtermEnv(t)
	deps, store, _ := orchestratorDeps(t, liveRows())
	if err := Open(context.Background(), "host", "api", OpenOptions{Orchestrator: orchRow + ":" + orchPane}, deps); err != nil {
		t.Fatal(err)
	}
	if err := Open(context.Background(), "host", "api", OpenOptions{}, deps); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.ByHostName("host", "api"); len(got.Grants) != 1 {
		t.Fatalf("grants after a plain re-open = %+v, want the original", got.Grants)
	}
}
