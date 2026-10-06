package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/remote"
	"github.com/k0nsta/agterm-remote/internal/token"
)

// PaneHookDependencies are the collaborators of the agterm pane hooks
// (on-split, on-scratch). They run detached under agterm's hook runner, with
// no terminal and the GUI PATH.
type PaneHookDependencies struct {
	Panes   Panes
	Store   BindingStore
	Remote  RemoteCwd
	Handled HandledPanes
	// Agr is the absolute path of this binary, typed into the pane: the
	// pane's shell may not have it on PATH.
	Agr string
	// PromptWait bounds how long a hook waits for the new pane's shell to draw
	// something before typing anyway; PromptPoll is the polling interval.
	PromptWait time.Duration
	PromptPoll time.Duration
	Sleep      func(time.Duration)
}

const (
	defaultPromptWait = 5 * time.Second
	defaultPromptPoll = 100 * time.Millisecond
	// cwdTimeout bounds the remote round trip; a slow host must not hold the
	// pane blank for long, and the session starts in the remote home instead.
	cwdTimeout = 10 * time.Second
)

// OnSplit is the pane.split hook. When a split pane appears on a row whose
// primary pane holds an agr session, it opens `<name>-2` on the same host in
// the new pane, as a child of `<name>`, starting in `<name>`'s current remote
// directory. A local row, a re-shown pane and an already bound pane are left
// alone.
func OnSplit(ctx context.Context, row, status string, deps PaneHookDependencies) error {
	target, ok, err := paneHookTarget(ctx, row, status, deps, isSplitPane)
	if err != nil || !ok {
		return err
	}
	if target.paneBound {
		return nil
	}
	child := target.binding.Name + "-2"
	if !token.Valid(child) {
		return fmt.Errorf("derived session name %q is invalid", child)
	}
	return runInNewPane(ctx, deps, row, target, func(dir string, cwdErr error) []string {
		argv := []string{deps.Agr, "open", target.binding.Host, child}
		if cwdErr != nil {
			// The remote did not answer `cwd`: an older script, whose attach
			// refuses any argument after the name.
			return argv
		}
		if dir != "" {
			argv = append(argv, "--cwd", dir)
		}
		// The parent goes even when the directory is unknown: it is what lets
		// `agr end` and `agr kill` take this session with its parent.
		return append(argv, "--parent", target.binding.Name)
	})
}

// HookEvent reads the row and status of a pane event. agterm hands a hook the
// event as one JSON object on stdin, in the `agtermctl events --json` shape
// ({"session":…,"payload":{"status":…}}); the AGT_SESSION_ID and
// AGT_EVENT_STATUS variables are the fallback for whatever the object lacks.
// event may be nil (stdin is a terminal or unreadable).
func HookEvent(event io.Reader, env func(string) string) (row, status string) {
	if event != nil {
		var decoded struct {
			Session string `json:"session"`
			Payload struct {
				Status string `json:"status"`
			} `json:"payload"`
		}
		if json.NewDecoder(io.LimitReader(event, 1<<16)).Decode(&decoded) == nil {
			row, status = decoded.Session, decoded.Payload.Status
		}
	}
	if row == "" {
		row = env("AGT_SESSION_ID")
	}
	if status == "" {
		status = env("AGT_EVENT_STATUS")
	}
	return row, status
}

type paneHookResult struct {
	paneID    string
	binding   bindings.Binding
	paneBound bool
}

// paneHookTarget does the checks both pane hooks share: a shown event on a
// live row, a pane of the wanted kind that no earlier event claimed, and an
// agr binding on the row's primary pane. ok is false when the hook has
// nothing to do.
func paneHookTarget(ctx context.Context, row, status string, deps PaneHookDependencies, wanted func(string) bool) (paneHookResult, bool, error) {
	if status != "shown" {
		return paneHookResult{}, false, nil
	}
	if !token.Valid(row) {
		return paneHookResult{}, false, fmt.Errorf("invalid session-id %q", row)
	}
	if deps.Panes == nil || deps.Store == nil || deps.Handled == nil {
		return paneHookResult{}, false, errors.New("pane hook dependencies are unavailable")
	}
	tree, err := deps.Panes.Surfaces(ctx)
	if err != nil {
		return paneHookResult{}, false, err
	}
	surfaces, live := tree[row], liveSurfaceIDs(tree)
	var primary, pane string
	for _, surface := range surfaces {
		switch {
		case isPrimaryPane(surface.Kind):
			primary = surface.PaneID
		case wanted(surface.Kind) && surface.Visible:
			pane = surface.PaneID
		}
	}
	if pane == "" {
		return paneHookResult{}, false, nil
	}
	already, err := deps.Handled.Claim(pane, live)
	if err != nil || already {
		return paneHookResult{}, false, err
	}
	all, err := deps.Store.Load()
	if err != nil {
		return paneHookResult{}, false, err
	}
	result := paneHookResult{paneID: pane}
	found := false
	for _, binding := range all {
		if binding.Row != row {
			continue
		}
		if binding.PaneID == pane {
			result.paneBound = true
		}
		if !found && isPrimaryBinding(binding, primary) {
			result.binding, found = binding, true
		}
	}
	return result, found, nil
}

// isPrimaryBinding matches by the pane token when both sides have one; a
// binding written before pane tokens matches by its slot name.
func isPrimaryBinding(binding bindings.Binding, primaryPaneID string) bool {
	if binding.PaneID != "" && primaryPaneID != "" {
		return binding.PaneID == primaryPaneID
	}
	return binding.Pane == "" || isPrimaryPane(binding.Pane)
}

func isPrimaryPane(kind string) bool {
	return kind == "left" || kind == "top" || kind == "primary"
}

func isSplitPane(kind string) bool {
	return kind == "right" || kind == "bottom" || kind == "split"
}

func liveSurfaceIDs(tree map[string][]agterm.Surface) []string {
	ids := make([]string, 0)
	for _, surfaces := range tree {
		for _, surface := range surfaces {
			if surface.PaneID != "" {
				ids = append(ids, surface.PaneID)
			}
		}
	}
	return ids
}

func remoteCwd(ctx context.Context, cwd RemoteCwd, binding bindings.Binding) (string, error) {
	if cwd == nil {
		return "", errors.New("remote runner is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, cwdTimeout)
	defer cancel()
	return cwd.Cwd(ctx, binding.Host, binding.Name)
}

type cwdAnswer struct {
	dir string
	err error
}

// runInNewPane asks the remote for the bound session's directory while the
// new pane's shell starts, then types the command build returns. Every
// element is shell-quoted, and a line that is not printable is refused
// outright: quoting stops the shell, not the line editor that receives the
// keystrokes.
func runInNewPane(ctx context.Context, deps PaneHookDependencies, row string, target paneHookResult,
	build func(dir string, cwdErr error) []string) error {
	answer := make(chan cwdAnswer, 1)
	go func() {
		dir, err := remoteCwd(ctx, deps.Remote, target.binding)
		answer <- cwdAnswer{dir: dir, err: err}
	}()
	waitForPrompt(ctx, deps, row, target.paneID)
	got := <-answer
	line := remote.QuoteRemoteCommand(build(got.dir, got.err)...)
	if !remote.Printable(line) {
		return fmt.Errorf("refusing to type a non-printable command into pane %s", target.paneID)
	}
	return deps.Panes.TypeLine(ctx, row, target.paneID, line)
}

// waitForPrompt returns once the pane shows the same non-blank screen twice
// in a row — a drawn prompt rather than a login banner still scrolling — or
// when PromptWait runs out. The pane is fresh, so its only program is the
// shell starting in it; typing after a timeout is not lost, the pty buffers
// it, it merely echoes before the prompt.
func waitForPrompt(ctx context.Context, deps PaneHookDependencies, row, paneID string) {
	wait, poll, sleep := deps.PromptWait, deps.PromptPoll, deps.Sleep
	if wait <= 0 {
		wait = defaultPromptWait
	}
	if poll <= 0 {
		poll = defaultPromptPoll
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	previous := ""
	for waited := time.Duration(0); waited < wait; waited += poll {
		text, err := deps.Panes.PaneText(ctx, row, paneID)
		text = strings.TrimSpace(text)
		if err == nil && text != "" && text == previous {
			return
		}
		previous = text
		sleep(poll)
	}
}
