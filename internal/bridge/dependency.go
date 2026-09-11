// Package bridge supervises the SSH reverse tunnel for one remote host.
package bridge

import (
	"context"
	"time"
)

//go:generate go run go.uber.org/mock/mockgen -source=dependency.go -destination=mocks/dependency_mock.go -package=mocks

// ProcessRunner starts a supervised process and connects its output to logPath.
type ProcessRunner interface {
	Start(ctx context.Context, argv []string, logPath string) (Process, error)
}

// Process is the small process lifecycle needed by Supervisor.
type Process interface {
	Wait() error
	Stop(grace time.Duration) error
}
