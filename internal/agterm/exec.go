package agterm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// ExecRunner executes agtermctl without invoking a shell. Callers pass the
// absolute path resolved by CtlPath, which is important for launchd where PATH
// is empty.
type ExecRunner struct{}

// Run executes a command and includes captured stderr in failures.
func (r *ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, name, args...)
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return fmt.Errorf("agtermctl: %w: %s", err, message)
		}
		return fmt.Errorf("agtermctl: %w", err)
	}
	return nil
}

// Output executes a command, returning stdout and the process exit code even
// when the process exits unsuccessfully. A non-process failure uses -1.
func (r *ExecRunner) Output(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, int, error) {
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = &stdout
	command.Stderr = &stderr
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}

	err := command.Run()
	exit := 0
	if err != nil {
		exit = -1
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exit = exitError.ExitCode()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			err = fmt.Errorf("agtermctl: %w: %s", err, message)
		} else {
			err = fmt.Errorf("agtermctl: %w", err)
		}
	}
	return stdout.Bytes(), exit, err
}

// Stream starts a command and returns its live stdout plus a stop function.
func (r *ExecRunner) Stream(ctx context.Context, name string, args ...string) (io.ReadCloser, func() error, error) {
	streamContext, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(streamContext, name, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if err := command.Start(); err != nil {
		cancel()
		_ = stdout.Close()
		return nil, nil, err
	}

	var stopOnce sync.Once
	var stopErr error
	stop := func() error {
		stopOnce.Do(func() {
			cancel()
			_ = stdout.Close()
			stopErr = command.Wait()
			if stopErr != nil && (streamContext.Err() != nil || strings.Contains(stopErr.Error(), "signal: killed")) {
				stopErr = nil
			}
		})
		return stopErr
	}
	return stdout, stop, nil
}

var _ Runner = (*ExecRunner)(nil)
var _ Outputter = (*ExecRunner)(nil)
var _ Streamer = (*ExecRunner)(nil)
