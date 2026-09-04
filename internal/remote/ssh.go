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

	commandArgs := make([]string, 0, 5+len(argv))
	commandArgs = append(commandArgs, "-o", "BatchMode=yes", host, "--")
	commandArgs = append(commandArgs, argv...)
	command := exec.CommandContext(ctx, e.executable(), commandArgs...)
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
