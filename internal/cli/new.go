package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/remote"
	"github.com/k0nsta/agterm-remote/internal/settings"
	"github.com/k0nsta/agterm-remote/internal/token"
)

// NewDependencies are the collaborators of New. It runs from an agterm keymap
// command: detached, with no terminal and the GUI PATH.
type NewDependencies struct {
	// Hosts lists the hosts agr has probed (install, open).
	Hosts      func() ([]string, error)
	Store      BindingStore
	Remote     OpenerRemote
	Picker     Picker
	Rows       Rows
	Workspaces WorkspaceLister
	Opener     RowOpener
	Closer     RowCloser
	Panes      Panes
	// StartCommands are offered for a new session besides a shell.
	StartCommands []settings.StartCommand
	// Agr is the absolute path of this binary, typed into the new row: its
	// shell may not have it on PATH.
	Agr string
	// SessionsTimeout bounds listing the host's sessions, so an offline host
	// fails instead of holding the keymap command for the TCP timeout.
	SessionsTimeout time.Duration
	PromptWait      time.Duration
	PromptPoll      time.Duration
	Sleep           func(time.Duration)
}

const defaultSessionsTimeout = 15 * time.Second

// New opens remote work in a new row: it asks for a host, a session on it,
// for a new session what it starts (see pickStart), and a workspace, then creates the row (right after row when its workspace
// is kept) and types `<agr> open <host> <name>` into its shell. The shell is
// the user's login shell, so mosh is on PATH and a failed attach stays
// readable there. Every question comes before the row exists, so a cancel
// leaves nothing behind. row may be empty (no current row).
func New(ctx context.Context, row string, deps NewDependencies) error {
	if row != "" && !token.Valid(row) {
		return fmt.Errorf("invalid session-id %q", row)
	}
	if deps.Picker == nil || deps.Remote == nil || deps.Workspaces == nil || deps.Opener == nil ||
		deps.Closer == nil || deps.Panes == nil {
		return errors.New("new dependencies are unavailable")
	}
	deps.Store = storeOrNil(deps.Store)
	host, err := pickHost(ctx, row, deps)
	if err != nil {
		return err
	}
	timeout := deps.SessionsTimeout
	if timeout <= 0 {
		timeout = defaultSessionsTimeout
	}
	name, existing, err := chooseOpenSession(ctx, host, OpenDependencies{
		Remote: timedSessions{OpenerRemote: deps.Remote, timeout: timeout},
		Picker: deps.Picker, Rows: deps.Rows, Store: deps.Store,
	}, io.Discard, io.Discard)
	if err != nil {
		return err
	}
	if !token.Valid(name) {
		return fmt.Errorf("invalid name %q (use [A-Za-z0-9_.-])", name)
	}
	argv := []string{deps.Agr, "open", host, name}
	if !existing {
		command, err := pickStart(ctx, name, deps.StartCommands, deps.Picker)
		if err != nil {
			return err
		}
		if command != "" {
			argv = append(argv, "--command", command)
		}
	}
	place, err := pickWorkspace(ctx, row, name, deps)
	if err != nil {
		return err
	}
	line := remote.QuoteRemoteCommand(argv...)
	if !remote.Printable(line) {
		return fmt.Errorf("refusing to type a non-printable command %q", line)
	}
	created, err := deps.Opener.NewRow(ctx, place)
	if err != nil {
		return err
	}
	// From here a failure closes the row, which would otherwise be left as an
	// empty local shell where the user is now looking.
	if err := typeIntoNewRow(ctx, created, line, deps); err != nil {
		if closeErr := deps.Closer.CloseRow(ctx, created); closeErr != nil {
			return fmt.Errorf("%w (and close row %s: %v)", err, created, closeErr)
		}
		return err
	}
	return nil
}

func typeIntoNewRow(ctx context.Context, row, line string, deps NewDependencies) error {
	tree, err := deps.Panes.Surfaces(ctx)
	if err != nil {
		return err
	}
	pane := ""
	for _, surface := range tree[row] {
		if isPrimaryPane(surface.Kind) {
			pane = surface.PaneID
		}
	}
	if pane == "" {
		return fmt.Errorf("new row %s has no pane", row)
	}
	waitForPrompt(ctx, PaneHookDependencies{
		Panes: deps.Panes, PromptWait: deps.PromptWait, PromptPoll: deps.PromptPoll, Sleep: deps.Sleep,
	}, row, pane)
	return deps.Panes.TypeLine(ctx, row, pane, line)
}

// pickHost offers every probed or bound host, the row's own first so Enter
// keeps it; a typed name is any other ssh destination.
func pickHost(ctx context.Context, row string, deps NewDependencies) (string, error) {
	seen := map[string]bool{}
	var hosts []string
	add := func(host string) {
		if host != "" && !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	var rest []string
	if deps.Store != nil {
		bound, err := deps.Store.Load()
		if err != nil {
			return "", err
		}
		for _, binding := range bound {
			if row != "" && binding.Row == row {
				add(binding.Host)
			} else {
				rest = append(rest, binding.Host)
			}
		}
	}
	if deps.Hosts != nil {
		cached, err := deps.Hosts()
		if err != nil {
			return "", err
		}
		rest = append(rest, cached...)
	}
	sort.Strings(rest)
	for _, host := range rest {
		add(host)
	}
	items := make([]agterm.PickItem, 0, len(hosts))
	for _, host := range hosts {
		items = append(items, agterm.PickItem{ID: host, Label: host})
	}
	host, err := pickOrType(ctx, deps.Picker, items, "remote host — or type one")
	if err != nil {
		return "", err
	}
	if !token.ValidHost(host) {
		return "", fmt.Errorf("invalid remote host %q", host)
	}
	return host, nil
}

// startShell is the start picker's first entry: no command, a login shell.
const startShell = "shell"

// pickStart asks what a new session runs: a plain shell (first, so Enter
// keeps the default), one of the user's start commands, or a command typed
// into the picker. Without start commands it asks nothing. {name} becomes the
// session name, so a program can take the row's name. It returns the
// --command for agr open, empty for a shell. The host runs --command with
// sh -c, so the command goes through the user's login and interactive shell
// to see their PATH.
func pickStart(ctx context.Context, name string, starts []settings.StartCommand, picker Picker) (string, error) {
	if len(starts) == 0 {
		return "", nil
	}
	items := []agterm.PickItem{{ID: startShell, Label: startShell, Subtitle: "login shell"}}
	commands := map[string]string{}
	for _, start := range starts {
		if start.Name == "" || start.Name == startShell || commands[start.Name] != "" {
			return "", fmt.Errorf("start_commands: invalid or repeated name %q", start.Name)
		}
		command := expandStart(start.Command, name)
		if strings.TrimSpace(command) == "" {
			return "", fmt.Errorf("start_commands: %q has no command", start.Name)
		}
		commands[start.Name] = command
		items = append(items, agterm.PickItem{ID: start.Name, Label: start.Name, Subtitle: command})
	}
	result, err := picker.Pick(ctx, items, "start "+name+" with — or type a command")
	if err != nil {
		return "", err
	}
	command := ""
	switch result.Kind {
	case "picked":
		if result.ID == startShell {
			return "", nil
		}
		command = commands[result.ID]
	case "custom":
		command = expandStart(strings.TrimSpace(result.Query), name)
	default:
		return "", fmt.Errorf("unexpected picker result %q", result.Kind)
	}
	if command == "" || !remote.Printable(command) {
		return "", fmt.Errorf("invalid start command %q", command)
	}
	return `"${SHELL:-/bin/sh}" -lic ` + remote.QuoteRemoteCommand(command), nil
}

func expandStart(command, name string) string {
	return strings.ReplaceAll(command, "{name}", name)
}

// pickWorkspace offers the window's workspaces, the row's own first; keeping
// it places the new row right after row, and a typed name reuses or creates
// the workspace of that name.
func pickWorkspace(ctx context.Context, row, name string, deps NewDependencies) (agterm.NewRow, error) {
	workspaces, err := deps.Workspaces.Workspaces(ctx)
	if err != nil {
		return agterm.NewRow{}, err
	}
	current := ""
	for _, workspace := range workspaces {
		for _, id := range workspace.Rows {
			if row != "" && id == row {
				current = workspace.ID
			}
		}
	}
	items := make([]agterm.PickItem, 0, len(workspaces))
	for _, workspace := range workspaces {
		item := agterm.PickItem{ID: workspace.ID, Label: workspace.Name}
		if workspace.ID == current {
			item.Subtitle = "current"
			items = append([]agterm.PickItem{item}, items...)
			continue
		}
		items = append(items, item)
	}
	result, err := deps.Picker.Pick(ctx, items, "workspace for "+name+" — or type a new one")
	if err != nil {
		return agterm.NewRow{}, err
	}
	switch result.Kind {
	case "picked":
		if result.ID == current {
			return agterm.NewRow{Name: name, After: row}, nil
		}
		return agterm.NewRow{Name: name, Workspace: result.ID}, nil
	case "custom":
		workspace := strings.TrimSpace(result.Query)
		if workspace == "" || !remote.Printable(workspace) {
			return agterm.NewRow{}, fmt.Errorf("invalid workspace name %q", result.Query)
		}
		return agterm.NewRow{Name: name, WorkspaceName: workspace}, nil
	default:
		return agterm.NewRow{}, fmt.Errorf("unexpected picker result %q", result.Kind)
	}
}

// pickOrType returns the picked id or the typed query, trimmed.
func pickOrType(ctx context.Context, picker Picker, items []agterm.PickItem, prompt string) (string, error) {
	result, err := picker.Pick(ctx, items, prompt)
	if err != nil {
		return "", err
	}
	switch result.Kind {
	case "picked":
		return result.ID, nil
	case "custom":
		return strings.TrimSpace(result.Query), nil
	default:
		return "", fmt.Errorf("unexpected picker result %q", result.Kind)
	}
}

// timedSessions bounds the session listing alone, not the pickers around it.
type timedSessions struct {
	OpenerRemote
	timeout time.Duration
}

func (s timedSessions) Sessions(ctx context.Context, host string) ([]remote.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	sessions, err := s.OpenerRemote.Sessions(ctx, host)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("list sessions on %s: no answer within %s", host, s.timeout)
	}
	return sessions, err
}

// RunNew adapts New to agr's integer-exit CLI convention.
func RunNew(ctx context.Context, row string, deps NewDependencies, errw io.Writer) int {
	err := New(ctx, row, deps)
	// A cancel is not a failure: bound with --error-hud, a non-zero exit
	// would flash an empty error over the row.
	if err == nil || errors.Is(err, agterm.ErrCancelled) {
		return 0
	}
	if errw != nil {
		_, _ = fmt.Fprintf(errw, "agr: new: %v\n", err)
	}
	return 1
}
