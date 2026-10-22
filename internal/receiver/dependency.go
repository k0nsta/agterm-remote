package receiver

import (
	"context"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/proxy"
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

// CtlSink runs one agtermctl call from a remote host and returns its exit
// code. It must return once ctx is canceled (client gone or shutdown).
type CtlSink interface {
	Ctl(ctx context.Context, req proxy.Request, frames proxy.FrameWriter) int
}
