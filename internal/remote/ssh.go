package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/k0nsta/agterm-remote/internal/token"
)

// ErrUnreachable identifies an SSH connection failure. It is deliberately a
// sentinel so callers can show an offline state without mistaking it for an
// uninstalled remote agr.
var ErrUnreachable = errors.New("remote host unreachable")

// ErrInvalidSSHCommand identifies a malformed local invocation before SSH is
// started.
var ErrInvalidSSHCommand = errors.New("invalid SSH command")

// ExitError reports a remote SSH command that ran and returned a non-zero
// status other than OpenSSH's connection-failure status 255.
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	if e == nil {
		return "remote command exited"
	}
	message := fmt.Sprintf("remote command exited with status %d", e.Code)
	if stderr := strings.TrimSpace(e.Stderr); stderr != "" {
		message += ": " + stderr
	}
	return message
}

// ExecSSH is the concrete non-interactive SSH adapter. Path is optional; a
// zero value resolves ssh from PATH once on its first use, falling back to
// /usr/bin/ssh as the launchd-safe default.
type ExecSSH struct {
	Path string

	resolveOnce sync.Once
	resolved    string
}

// NewExecSSH resolves the executable immediately. Resolving per adapter,
// rather than process-wide, keeps PATH shims and separately configured
// runners isolated from one another.
func NewExecSSH() *ExecSSH {
	return &ExecSSH{Path: resolveExecutable("ssh")}
}

func (e *ExecSSH) executable() string {
	if e == nil {
		return ""
	}
	e.resolveOnce.Do(func() {
		if e.Path != "" {
			e.resolved = e.Path
			return
		}
		e.resolved = resolveExecutable("ssh")
	})
	return e.resolved
}

// Run executes one argv-only remote command. stdout is returned as the body;
// stderr is retained only in errors so banners and login diagnostics cannot
// corrupt the agr data/header protocol.
func (e *ExecSSH) Run(ctx context.Context, host string, stdin []byte, argv ...string) ([]byte, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrInvalidSSHCommand)
	}
	if !token.ValidHost(host) {
		return nil, fmt.Errorf("%w: invalid host %q", ErrInvalidSSHCommand, host)
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, fmt.Errorf("%w: empty remote argv", ErrInvalidSSHCommand)
	}

	// OpenSSH does not preserve argv boundaries: it joins everything after the
	// destination with single spaces and hands one string to the remote login
	// shell, which re-parses it. Passing argv elements separately therefore lets
	// any `;`, quote or glob inside them split the command apart — a script like
	// `sh -c 'a; b'` would run only `a` inside sh and `b` in the login shell.
	// Quote each element into one command string so the remote shell rebuilds
	// exactly the argv intended here.
	command := exec.CommandContext(ctx, e.executable(),
		"-o", "BatchMode=yes", host, "--", shellQuoteArgv(argv))
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stdout.Bytes(), ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		stderrText := strings.TrimSpace(stderr.String())
		if code == 255 {
			if stderrText == "" {
				return stdout.Bytes(), ErrUnreachable
			}
			return stdout.Bytes(), fmt.Errorf("%w: %s", ErrUnreachable, stderrText)
		}
		return stdout.Bytes(), &ExitError{Code: code, Stderr: stderrText}
	}
	if stderrText := strings.TrimSpace(stderr.String()); stderrText != "" {
		return stdout.Bytes(), fmt.Errorf("ssh: %w: %s", err, stderrText)
	}
	return stdout.Bytes(), fmt.Errorf("ssh: %w", err)
}

// ExecTTY runs a complete interactive argv with the current process's
// terminal attached. It intentionally does not set Setpgid: an interactive
// SSH or mosh client must remain in the terminal's foreground process group.
type ExecTTY struct{}

// Interactive starts argv with inherited stdin, stdout, and stderr.
func (e *ExecTTY) Interactive(ctx context.Context, argv ...string) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidSSHCommand)
	}
	if len(argv) == 0 || argv[0] == "" {
		return fmt.Errorf("%w: empty interactive argv", ErrInvalidSSHCommand)
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func resolveExecutable(name string) string {
	path, err := exec.LookPath(name)
	if err == nil {
		return path
	}
	if name == "ssh" {
		return "/usr/bin/ssh"
	}
	return name
}

var _ SSH = (*ExecSSH)(nil)
var _ TTY = (*ExecTTY)(nil)

// QuoteRemoteCommand renders argv as one POSIX-shell command string for a
// transport that hands its remote command to a shell — i.e. ssh. It is
// exported because the interactive path builds its own argv and must apply the
// same quoting: `ssh -t host -- <agrPath> attach <name>` is re-parsed by the
// remote login shell exactly like a non-interactive command is.
//
// Do NOT use it for mosh: mosh-server execs argv directly with no remote
// shell, so quoting there would make the quotes part of the path.
func QuoteRemoteCommand(argv ...string) string { return shellQuoteArgv(argv) }

// shellQuoteArgv renders argv as a single POSIX-shell command string in which
// every element survives one round of shell parsing intact. Single quotes are
// the only fully literal quoting in sh, so an embedded single quote is closed,
// escaped and reopened.
func shellQuoteArgv(argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(quoted, " ")
}
