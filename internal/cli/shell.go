package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/k0nsta/agterm-remote/internal/remote"
	"github.com/k0nsta/agterm-remote/internal/token"
)

// ShellDependencies are the collaborators of Shell.
type ShellDependencies struct {
	TTY      remote.TTY
	HostInfo HostInfoReader
	MoshPath func() string
	Out      io.Writer
	Reset    func(io.Writer)
	// Pause holds the terminal after ssh could not connect, so the error is
	// readable before a scratch that exec'd this command closes. Nil reads one
	// line from stdin.
	Pause func()
}

// shellScript is run by the remote's sh with the directory as $1: change into
// it when it is set and usable, then become the user's login shell. It is a
// constant so no remote value is ever parsed as script.
const shellScript = `[ -z "$1" ] || cd "$1" 2>/dev/null; exec "${SHELL:-/bin/sh}" -l`

// Shell starts a plain interactive login shell on host in dir, with no
// multiplexer, no binding and no bridge: it lives exactly as long as the
// terminal it runs in. on-scratch types it into a row's scratch pane.
func Shell(ctx context.Context, host, dir string, deps ShellDependencies) error {
	if !token.ValidHost(host) {
		return fmt.Errorf("invalid remote host %q", host)
	}
	if dir != "" && !strings.HasPrefix(dir, "/") {
		return fmt.Errorf("invalid --cwd %q (must be absolute)", dir)
	}
	if deps.TTY == nil {
		return errors.New("interactive SSH runner is unavailable")
	}
	mosh := false
	if deps.HostInfo != nil {
		info, err := deps.HostInfo(host)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read host info: %w", err)
		}
		mosh = err == nil && info.Mosh && localMoshPath(OpenDependencies{MoshPath: deps.MoshPath}) != ""
	}
	remoteArgv := []string{"sh", "-c", shellScript, "sh", dir}
	argv := []string{"ssh", "-t", host, "--", remote.QuoteRemoteCommand(remoteArgv...)}
	if mosh {
		argv = append([]string{"mosh", host, "--"}, remoteArgv...)
	}
	if out := deps.Out; out != nil && isTerminalWriter(out) {
		reset := deps.Reset
		if reset == nil {
			reset = resetTerminal
		}
		defer reset(out)
	}
	err := deps.TTY.Interactive(ctx, argv...)
	var exit *exec.ExitError
	if !mosh && errors.As(err, &exit) && exit.ExitCode() == 255 {
		_, _ = fmt.Fprintf(os.Stderr, "agr: could not connect to %s — press Enter to close\n", host)
		pause := deps.Pause
		if pause == nil {
			pause = func() { _, _ = bufio.NewReader(os.Stdin).ReadString('\n') }
		}
		pause()
	}
	return err
}

// RunShell adapts Shell to agr's integer-exit CLI convention.
func RunShell(ctx context.Context, host, dir string, deps ShellDependencies, errw io.Writer) int {
	if err := Shell(ctx, host, dir, deps); err != nil {
		if errw != nil {
			_, _ = fmt.Fprintf(errw, "agr: shell: %v\n", err)
		}
		return 1
	}
	return 0
}
