package cli

import (
	"context"
)

// OnScratch is the pane.scratch hook. When a scratch terminal appears on a
// row whose primary pane holds an agr session, it replaces the scratch's
// shell with `agr shell`: a plain login shell on the same host, in that
// session's current remote directory. It is not a multiplexer session, so it
// ends with the scratch and leaves nothing to clean up. The exec means leaving
// the remote shell closes the scratch, so the next ⌘J is remote again rather
// than a local shell this hook already handled. Hiding keeps it; a re-shown
// scratch is left alone, and on a local row the scratch stays local.
func OnScratch(ctx context.Context, row, status string, deps PaneHookDependencies) error {
	target, ok, err := paneHookTarget(ctx, row, status, deps, isScratchPane)
	if err != nil || !ok || target.paneBound {
		return err
	}
	return runInNewPane(ctx, deps, row, target, func(dir string, cwdErr error) []string {
		argv := []string{"exec", deps.Agr, "shell", target.binding.Host}
		if cwdErr == nil && dir != "" {
			argv = append(argv, "--cwd", dir)
		}
		return argv
	})
}

func isScratchPane(kind string) bool {
	return kind == "scratch"
}
