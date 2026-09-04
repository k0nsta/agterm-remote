package agterm

import (
	"context"
	"io"
)

//go:generate go run go.uber.org/mock/mockgen -source=dependency.go -destination=mocks/dependency_mock.go -package=mocks

// Runner executes a command whose result is not needed by the caller.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// Outputter executes a command and returns stdout and its exit code
// separately, including when the command exits non-zero.
type Outputter interface {
	Output(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, int, error)
}

// Streamer starts a command whose stdout remains live until the returned stop
// function is called.
type Streamer interface {
	Stream(ctx context.Context, name string, args ...string) (io.ReadCloser, func() error, error)
}
