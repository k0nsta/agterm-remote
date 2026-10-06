package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/remote"
	"github.com/k0nsta/agterm-remote/internal/token"
)

// EndDependencies are the collaborators of End.
type EndDependencies struct {
	Store   RowBindings
	Reaper  Reaper
	Confirm Confirmer
	Rows    RowCloser
}

// ErrEndCancelled means the confirm dialog was not answered with End.
var ErrEndCancelled = errors.New("end cancelled")

// End finishes the work under one agterm row: after a confirm dialog it kills
// every remote session bound to the row — the remote reap also takes the
// sessions recorded as their children, the side panes — drops the row's
// bindings and closes the row. Closing a row by itself (⌘W) only detaches;
// this is the explicit kill. A session already gone counts as ended; any
// other failure keeps the row and its bindings, so it can be retried.
func End(ctx context.Context, row string, deps EndDependencies) error {
	if !token.Valid(row) {
		return fmt.Errorf("invalid session-id %q", row)
	}
	if deps.Store == nil || deps.Reaper == nil || deps.Confirm == nil || deps.Rows == nil {
		return errors.New("end dependencies are unavailable")
	}
	all, err := deps.Store.Load()
	if err != nil {
		return err
	}
	sessions := rowSessions(all, row)
	if len(sessions) == 0 {
		return fmt.Errorf("row %s holds no agr session", row)
	}
	title, message := endQuestion(sessions)
	confirmed, err := deps.Confirm.Confirm(ctx, row, title, message, "end", "End")
	if err != nil {
		return err
	}
	if !confirmed {
		return ErrEndCancelled
	}
	var failures []string
	for _, session := range sessions {
		err := deps.Reaper.Reap(ctx, session.Host, session.Name)
		if err != nil && !errors.Is(err, remote.ErrNoSession) {
			failures = append(failures, fmt.Sprintf("%s on %s: %v", session.Name, session.Host, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("row kept, could not end %s", strings.Join(failures, "; "))
	}
	// Close before unbinding: a close that fails leaves the row and its
	// bindings, so `agr end` can simply be run again (every reap is now a
	// missing session, which counts as done). A row that did close is
	// unbound here, and the daemon unbinds a closed row as well.
	if err := deps.Rows.CloseRow(ctx, row); err != nil {
		return fmt.Errorf("sessions ended, close row: %w", err)
	}
	if err := deps.Store.UnbindRow(row); err != nil {
		return fmt.Errorf("unbind row: %w", err)
	}
	return nil
}

// rowSessions returns the row's distinct host/session pairs in binding order,
// so the primary pane's session — usually the parent — is reaped first.
func rowSessions(all []bindings.Binding, row string) []bindings.Binding {
	seen := map[string]bool{}
	var sessions []bindings.Binding
	for _, binding := range all {
		key := binding.Host + "\x00" + binding.Name
		if binding.Row != row || seen[key] {
			continue
		}
		seen[key] = true
		sessions = append(sessions, binding)
	}
	return sessions
}

// endQuestion names exactly what End will kill: one host is said once, a row
// whose panes reach different hosts names each session with its host.
func endQuestion(sessions []bindings.Binding) (string, string) {
	oneHost := true
	for _, session := range sessions {
		oneHost = oneHost && session.Host == sessions[0].Host
	}
	names := make([]string, 0, len(sessions))
	for _, session := range sessions {
		if oneHost {
			names = append(names, session.Name)
		} else {
			names = append(names, session.Name+" on "+session.Host)
		}
	}
	title := "End " + strings.Join(names, ", ") + "?"
	if oneHost {
		title = fmt.Sprintf("End %s on %s?", strings.Join(names, ", "), sessions[0].Host)
	}
	return title, "Kills the remote agent, its side-pane sessions, and closes this row."
}

// RunEnd adapts End to agr's integer-exit CLI convention.
func RunEnd(ctx context.Context, row string, deps EndDependencies, errw io.Writer) int {
	err := End(ctx, row, deps)
	// A cancel is not a failure: bound with --error-hud, a non-zero exit
	// would flash an empty error over the row the user just chose to keep.
	if err == nil || errors.Is(err, ErrEndCancelled) {
		return 0
	}
	if errw != nil {
		_, _ = fmt.Fprintf(errw, "agr: end: %v\n", err)
	}
	return 1
}
