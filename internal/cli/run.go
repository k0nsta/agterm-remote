package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/k0nsta/agterm-remote/internal/remote"
	"github.com/k0nsta/agterm-remote/internal/token"
)

// RunDependencies are the collaborators of Run.
type RunDependencies struct {
	TTY   remote.TTY
	Out   io.Writer
	Reset func(io.Writer)
}

// runScript is run by the remote's sh with the directory as $1 and the program
// and its arguments after it: change into the directory when one is given
// (failing if it is unusable, since the program was meant to run there), then
// become the program. It is a constant so no remote value is parsed as script.
const runScript = `d=$1; shift; if [ -n "$d" ]; then cd "$d" || exit 1; fi; exec "$@"`

// Run runs argv on host, in dir when given, in one interactive ssh with no
// multiplexer, binding or bridge. The program's exit status is what ssh
// returns, which RunRun passes through.
func Run(ctx context.Context, host, dir string, argv []string, deps RunDependencies) error {
	if !token.ValidHost(host) {
		return fmt.Errorf("invalid remote host %q", host)
	}
	if dir != "" && !strings.HasPrefix(dir, "/") {
		return fmt.Errorf("invalid --cwd %q (must be absolute)", dir)
	}
	if len(argv) == 0 || argv[0] == "" {
		return errors.New("no command to run")
	}
	if deps.TTY == nil {
		return errors.New("interactive SSH runner is unavailable")
	}
	remoteArgv := append([]string{"sh", "-c", runScript, "sh", dir}, argv...)
	command := []string{"ssh", "-t", host, "--", remote.QuoteRemoteCommand(remoteArgv...)}
	if out := deps.Out; out != nil && isTerminalWriter(out) {
		reset := deps.Reset
		if reset == nil {
			reset = resetTerminal
		}
		defer reset(out)
	}
	return deps.TTY.Interactive(ctx, command...)
}

// RunRun adapts Run to agr's integer-exit CLI convention: the program's own
// exit status passes through, and a failure of agr itself is 1.
func RunRun(ctx context.Context, host, dir string, argv []string, deps RunDependencies, errw io.Writer) int {
	err := Run(ctx, host, dir, argv, deps)
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() > 0 {
		return exit.ExitCode()
	}
	if errw != nil {
		_, _ = fmt.Fprintf(errw, "agr: run: %v\n", err)
	}
	return 1
}
