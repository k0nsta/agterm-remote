package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/remote"
	"github.com/k0nsta/agterm-remote/internal/token"
)

// HostInfoReader reads locally cached remote capabilities.
type HostInfoReader func(host string) (remote.HostInfo, error)

// OpenDependencies are the injected collaborators for Open.
type OpenDependencies struct {
	Dirs     paths.Dirs
	Remote   OpenerRemote
	Picker   Picker
	Rows     Rows
	Store    BindingStore
	Bridge   OpenBridge
	Labeler  Labeler
	HostInfo HostInfoReader
	// Panes reads the live rows, to validate --orchestrator.
	Panes    PaneTree
	TTY      remote.TTY
	Out      io.Writer
	ErrOut   io.Writer
	MoshPath func() string
	Reset    func(io.Writer)
}

// OpenOptions are where a new remote session starts and which session it
// belongs to. Both only reach a remote that knows them (the cwd verb's era):
// an older attach refuses any argument after the name.
type OpenOptions struct {
	Cwd    string
	Parent string
	// Command runs in a session that does not exist yet, then leaves a shell.
	// Attaching to an existing session keeps it and does not run it again.
	Command string
	// Orchestrator is "<row>:<pane>" of a live local pane whose agent started
	// this session: the host may type into it (an orchestrator grant).
	Orchestrator string
}

// minEnvAgrVersion is the first remote agr whose attach takes --env and
// --command. "dev" is a local build, which has them.
const minEnvAgrVersion = "1.3.0"

// agrVersionPattern is the shape of a release version; anything else but "dev"
// is treated as too old, since the gate must not guess a remote can do more.
var agrVersionPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+`)

// SupportsEnvCommand reports whether a remote agr of this version accepts
// attach --env and --command.
func SupportsEnvCommand(agrVersion string) bool {
	if agrVersion == "dev" {
		return true
	}
	return agrVersionPattern.MatchString(agrVersion) && !agterm.VersionLess(agrVersion, minEnvAgrVersion)
}

// proxyEnvKeys are the agterm variables exported into the remote session, read
// from agr open's own environment.
var proxyEnvKeys = []string{
	"AGTERM_ENABLED", "AGTERM_SESSION_ID", "AGTERM_PANE", "AGTERM_PANE_ID",
	"AGTERM_WORKSPACE_ID", "AGTERM_WINDOW_ID",
}

// proxyEnvSocket marks a remote row's agterm socket as the agr proxy rather
// than a path that does not exist there.
const proxyEnvSocket = "agr-proxy"

// openEnv returns the K=V pairs to export, in a fixed order. A variable that
// is unset is left out; one that is not a token (the remote refuses anything
// else) is left out with a warning.
func openEnv(getenv func(string) string, errout io.Writer) []string {
	var pairs []string
	for _, key := range proxyEnvKeys {
		value := getenv(key)
		if value == "" {
			continue
		}
		if !validEnvValue(value) {
			_, _ = fmt.Fprintf(errout, "agr: not exporting %s: value is not an id\n", key)
			continue
		}
		pairs = append(pairs, key+"="+value)
	}
	return append(pairs, "AGTERM_SOCKET="+proxyEnvSocket)
}

func validEnvValue(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// ErrPickerUnavailable means the caller asked for picker-based open but
// agtermctl is not available. Open has already rendered ls and its usage.
var ErrPickerUnavailable = errors.New("picker unavailable")

// Open attaches to a remote agr session, optionally adopting the current
// agterm row. A blank name invokes the native picker before any side effects.
func Open(ctx context.Context, host, name string, opts OpenOptions, deps OpenDependencies) error {
	deps.Store = storeOrNil(deps.Store)
	if ctx == nil {
		return errors.New("nil open context")
	}
	if !token.ValidHost(host) {
		return fmt.Errorf("invalid remote host %q", host)
	}
	out := deps.Out
	if out == nil {
		out = io.Discard
	}
	errout := deps.ErrOut
	if errout == nil {
		errout = io.Discard
	}

	if name == "" {
		selected, err := chooseOpenName(ctx, host, deps, out, errout)
		if err != nil {
			return err
		}
		name = selected
	}
	if !token.Valid(name) {
		return fmt.Errorf("invalid name %q (use [A-Za-z0-9_.-])", name)
	}
	if opts.Parent != "" && !token.Valid(opts.Parent) {
		return fmt.Errorf("invalid parent %q (use [A-Za-z0-9_.-])", opts.Parent)
	}
	if opts.Cwd != "" && !strings.HasPrefix(opts.Cwd, "/") {
		return fmt.Errorf("invalid --cwd %q (must be absolute)", opts.Cwd)
	}
	if deps.Remote == nil {
		return errors.New("remote runner is unavailable")
	}
	if deps.Bridge == nil {
		return errors.New("daemon client is unavailable")
	}
	if deps.TTY == nil {
		return errors.New("interactive SSH runner is unavailable")
	}

	info := remote.HostInfo{Mux: "tmux"}
	if deps.HostInfo != nil {
		cached, err := deps.HostInfo(host)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read host info: %w", err)
		}
		if err == nil {
			info = cached
			if info.Mux == "" {
				info.Mux = "tmux"
			}
		}
	}
	supportsEnv := SupportsEnvCommand(info.AgrVersion)
	if opts.Command != "" && !supportsEnv {
		return fmt.Errorf("--command needs agr 1.3.0 or newer on %s (it has %q): run agr install %s",
			host, info.AgrVersion, host)
	}
	row := os.Getenv("AGTERM_SESSION_ID")
	if row != "" && !token.Valid(row) {
		return fmt.Errorf("invalid session-id %q (use [A-Za-z0-9_.-])", row)
	}
	var grants []bindings.Grant
	if opts.Orchestrator != "" {
		grant, err := orchestratorGrant(ctx, opts.Orchestrator, row, deps)
		if err != nil {
			return err
		}
		grants = append(grants, grant)
	}
	// The path goes into argv, where neither transport expands a "$HOME/..."
	// — ssh because the argv is quoted (that is what stops injection), mosh
	// because it execs argv with no shell — so it must be absolute.
	agrPath, err := deps.Remote.ResolveAgrPath(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve remote agr path for %q: %w", host, err)
	}
	if agrPath == "" {
		return fmt.Errorf("remote agr path is unavailable for %q", host)
	}

	if row != "" {
		if deps.Store == nil {
			return errors.New("binding store is unavailable")
		}
		binding := bindings.Binding{
			Row: row, PaneID: os.Getenv("AGTERM_PANE_ID"), Pane: os.Getenv("AGTERM_PANE"),
			Host: host, Name: name, Mux: info.Mux, BoundAt: time.Now().UTC(),
			Grants: grants,
		}
		if err := deps.Store.Bind(binding); err != nil {
			return fmt.Errorf("bind agterm row: %w", err)
		}
		if err := deps.Bridge.ReloadBindings(ctx); err != nil {
			return fmt.Errorf("reload daemon bindings: %w", err)
		}
	}
	if err := deps.Bridge.Up(ctx, host); err != nil {
		return fmt.Errorf("bring bridge up: %w", err)
	}

	// A parented open is a side-pane session inside a row that already names
	// its parent: relabelling would rename the whole row after the child.
	if row != "" && deps.Labeler != nil && opts.Parent == "" {
		if err := deps.Labeler.Rename(ctx, row, name); err != nil {
			_, _ = fmt.Fprintf(errout, "agr: rename %s: %v\n", row, err)
		}
		if err := deps.Labeler.Context(ctx, row, host+" · "+name); err != nil {
			_, _ = fmt.Fprintf(errout, "agr: set context %s: %v\n", row, err)
		}
	}

	// ssh hands its remote command to the login shell, which re-parses it, so
	// the remote argv has to be quoted here the same way ExecSSH.Run quotes
	// it — a home like `/srv/user data` or one containing `;` would otherwise
	// break the attach or execute as a separate remote command.
	var env []string
	if supportsEnv && row != "" {
		env = openEnv(os.Getenv, errout)
	}
	remoteArgv := attachArgv(agrPath, name, opts, env)
	argv := []string{"ssh", "-t", host, "--", remote.QuoteRemoteCommand(remoteArgv...)}
	if info.Mosh && localMoshPath(deps) != "" {
		// mosh-server execs argv directly — no remote shell — so these stay
		// separate and unquoted.
		argv = append([]string{"mosh", host, "--"}, remoteArgv...)
	}
	if isTerminalWriter(out) {
		reset := deps.Reset
		if reset == nil {
			reset = resetTerminal
		}
		defer reset(out)
	}
	if err := deps.TTY.Interactive(ctx, argv...); err != nil {
		return err
	}
	return nil
}

func attachArgv(agrPath, name string, opts OpenOptions, env []string) []string {
	argv := []string{agrPath, "attach", name}
	if opts.Cwd != "" {
		argv = append(argv, "--cwd", opts.Cwd)
	}
	if opts.Parent != "" {
		argv = append(argv, "--parent", opts.Parent)
	}
	for _, pair := range env {
		argv = append(argv, "--env", pair)
	}
	if opts.Command != "" {
		argv = append(argv, "--command", opts.Command)
	}
	return argv
}

// orchestratorGrant checks "<row>:<pane>" against the live tree and returns
// the grant. Both halves must be the full ids of an existing pane that no host
// owns: a pane bound to a host (or the one this open is about to bind) is never
// a grant target.
func orchestratorGrant(ctx context.Context, spec, ownRow string, deps OpenDependencies) (bindings.Grant, error) {
	rowID, paneID, ok := strings.Cut(spec, ":")
	if !ok || !token.Valid(rowID) || !token.Valid(paneID) {
		return bindings.Grant{}, fmt.Errorf("invalid --orchestrator %q (want <row-id>:<pane-id>)", spec)
	}
	if ownRow == "" {
		return bindings.Grant{}, errors.New("--orchestrator needs an agterm row to bind (run it inside agterm)")
	}
	if deps.Panes == nil {
		return bindings.Grant{}, errors.New("--orchestrator needs agtermctl to read the pane tree")
	}
	rows, err := deps.Panes.TreeRows(ctx)
	if err != nil {
		return bindings.Grant{}, fmt.Errorf("read agterm tree: %w", err)
	}
	var grant bindings.Grant
	for _, row := range rows {
		if !strings.EqualFold(row.ID, rowID) {
			continue
		}
		for _, surface := range row.Surfaces {
			if strings.EqualFold(surface.PaneID, paneID) {
				grant = bindings.Grant{Row: row.ID, PaneID: surface.PaneID, Kind: bindings.GrantOrchestrator}
			}
		}
	}
	if grant.Row == "" {
		return bindings.Grant{}, fmt.Errorf("--orchestrator %s: no such pane in agterm", spec)
	}
	if strings.EqualFold(grant.PaneID, os.Getenv("AGTERM_PANE_ID")) {
		return bindings.Grant{}, errors.New("--orchestrator is this pane itself, which is about to be bound to the host")
	}
	if deps.Store == nil {
		return bindings.Grant{}, errors.New("binding store is unavailable")
	}
	bound, err := deps.Store.Load()
	if err != nil {
		return bindings.Grant{}, fmt.Errorf("read bindings: %w", err)
	}
	for _, binding := range bound {
		if !strings.EqualFold(binding.Row, grant.Row) {
			continue
		}
		// An old binding with no pane id covers the whole row.
		if binding.PaneID == "" || strings.EqualFold(binding.PaneID, grant.PaneID) {
			return bindings.Grant{}, fmt.Errorf("--orchestrator %s is bound to host %s: only a local pane can be one",
				spec, binding.Host)
		}
	}
	return grant, nil
}

func chooseOpenName(ctx context.Context, host string, deps OpenDependencies, out, errout io.Writer) (string, error) {
	if deps.Picker == nil {
		if deps.Remote == nil || deps.Store == nil {
			return "", ErrPickerUnavailable
		}
		listing, err := List(ctx, host, deps.Remote, deps.Store, deps.Rows)
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(out, listing)
		_, _ = fmt.Fprintf(errout, "no agtermctl for the picker — usage: agr open <host> <name>\n")
		return "", ErrPickerUnavailable
	}
	if deps.Remote == nil {
		return "", errors.New("remote runner is unavailable")
	}
	sessions, err := deps.Remote.Sessions(ctx, host)
	if err != nil {
		return "", err
	}
	var bound []bindings.Binding
	if deps.Store != nil {
		bound, err = deps.Store.Load()
		if err != nil {
			return "", err
		}
	}
	var tree []string
	if deps.Rows != nil {
		tree, _ = deps.Rows.Tree(ctx)
	}
	result, err := deps.Picker.Pick(ctx, ItemsFor(host, sessions, bound, tree),
		"session on "+host+" — or type a new name")
	if err != nil {
		return "", err
	}
	switch result.Kind {
	case "picked":
		return result.ID, nil
	case "custom":
		return result.Query, nil
	case "cancelled":
		return "", agterm.ErrCancelled
	default:
		return "", fmt.Errorf("unexpected picker result %q", result.Kind)
	}
}

func localMoshPath(deps OpenDependencies) string {
	if deps.MoshPath != nil {
		return deps.MoshPath()
	}
	path, err := exec.LookPath("mosh")
	if err != nil {
		return ""
	}
	return path
}

func isTerminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func resetTerminal(writer io.Writer) {
	_, _ = io.WriteString(writer, "\033[?1000l\033[?1002l\033[?1003l\033[?1006l\033[?2004l")
	command := exec.Command("stty", "sane")
	command.Stdin = os.Stdin
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	_ = command.Run()
}

// RunOpen adapts Open to agr's integer-exit CLI convention.
func RunOpen(ctx context.Context, host, name string, opts OpenOptions, deps OpenDependencies, errw io.Writer) int {
	deps.ErrOut = errw
	if err := Open(ctx, host, name, opts, deps); err != nil {
		if errors.Is(err, agterm.ErrCancelled) {
			return 1
		}
		if errors.Is(err, ErrPickerUnavailable) {
			return 1
		}
		if errw == nil {
			errw = io.Discard
		}
		_, _ = fmt.Fprintf(errw, "agr: open: %v\n", err)
		return 1
	}
	return 0
}

var _ OpenerRemote = (*remote.Runner)(nil)

// storeOrNil folds a typed-nil *bindings.Store into a nil interface, so the
// "no store wired" checks below keep skipping the optional binding work the
// way they did when the field was the concrete pointer.
func storeOrNil(store BindingStore) BindingStore {
	if concrete, ok := store.(*bindings.Store); ok && concrete == nil {
		return nil
	}
	return store
}
