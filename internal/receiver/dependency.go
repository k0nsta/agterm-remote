package receiver

import (
	"context"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
)

//go:generate go run go.uber.org/mock/mockgen -source=dependency.go -destination=mocks/dependency_mock.go -package=mocks

// StatusSink pushes one status update to agterm.
type StatusSink interface {
	Status(ctx context.Context, target string, args agterm.StatusArgs) error
}

// Resolver finds a binding for a remote host and multiplexer session.
type Resolver interface {
	ByHostName(host, name string) (bindings.Binding, bool)
}

// Liveness is notified when a syntactically valid event arrives from a host.
type Liveness interface {
	MarkAlive()
}
