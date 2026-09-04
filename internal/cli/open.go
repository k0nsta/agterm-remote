package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	Store    *bindings.Store
	Bridge   OpenBridge
	Labeler  Labeler
	HostInfo HostInfoReader
	TTY      remote.TTY
	Out      io.Writer
	ErrOut   io.Writer
	MoshPath func() string
	Reset    func(io.Writer)
}

// ErrPickerUnavailable means the caller asked for picker-based open but
// agtermctl is not available. Open has already rendered ls and its usage.
var ErrPickerUnavailable = errors.New("picker unavailable")

// Open attaches to a remote agr session, optionally adopting the current
// agterm row. A blank name invokes the native picker before any side effects.
func Open(ctx context.Context, host, name string, deps OpenDependencies) error {
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
	agrPath := deps.Remote.AgrPath(host)
	if agrPath == "" {
		return fmt.Errorf("remote agr path is unavailable for %q", host)
	}

	row := os.Getenv("AGTERM_SESSION_ID")
	if row != "" && !token.Valid(row) {
		return fmt.Errorf("invalid session-id %q (use [A-Za-z0-9_.-])", row)
	}
	if row != "" {
		if deps.Store == nil {
			return errors.New("binding store is unavailable")
		}
		binding := bindings.Binding{
			Row: row, PaneID: os.Getenv("AGTERM_PANE_ID"), Pane: os.Getenv("AGTERM_PANE"),
			Host: host, Name: name, Mux: info.Mux, BoundAt: time.Now().UTC(),
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

	if row != "" && deps.Labeler != nil {
		if err := deps.Labeler.Rename(ctx, row, name); err != nil {
			_, _ = fmt.Fprintf(errout, "agr: rename %s: %v\n", row, err)
		}
		if err := deps.Labeler.Context(ctx, row, host+" · "+name); err != nil {
			_, _ = fmt.Fprintf(errout, "agr: set context %s: %v\n", row, err)
		}
	}

	argv := []string{"ssh", "-t", host, "--", agrPath, "attach", name}
	if info.Mosh && localMoshPath(deps) != "" {
		argv = []string{"mosh", host, "--", agrPath, "attach", name}
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
	result, err := deps.Picker.Pick(ctx, ItemsFor(sessions, bound, tree), "tmux session on "+host+" — or type a new name")
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
func RunOpen(ctx context.Context, host, name string, deps OpenDependencies, errw io.Writer) int {
	deps.ErrOut = errw
	if err := Open(ctx, host, name, deps); err != nil {
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
