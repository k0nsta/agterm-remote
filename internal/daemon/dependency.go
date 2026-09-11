package daemon

import (
	"context"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

//go:generate go run go.uber.org/mock/mockgen -source=dependency.go -destination=mocks/dependency_mock.go -package=mocks

// UI controls non-status agterm row UI.
type UI interface {
	HudOpen(ctx context.Context, row, message string) error
	HudClose(ctx context.Context, row string) error
}

// Remote is the remote host functionality needed by the daemon lifecycle.
type Remote interface {
	Home(ctx context.Context, host string) (string, error)
	Sessions(ctx context.Context, host string) ([]remote.Session, error)
	EnsureDirs(ctx context.Context, host string) error
}

// EventSource provides agterm row-close events.
type EventSource interface {
	ClosedRows(ctx context.Context) (<-chan string, error)
}

// Rows lists live agterm row ids.
type Rows interface {
	Tree(ctx context.Context) ([]string, error)
}

// StatusSink pushes status to agterm.
type StatusSink interface {
	Status(ctx context.Context, target string, args agterm.StatusArgs) error
}

// Supervisor is the lifecycle contract the daemon needs from one host's SSH
// bridge. Liveness promotion is deliberately optional: the bridge package can
// provide MarkAlive without making it part of the daemon's small interface.
type Supervisor interface {
	Run(ctx context.Context) error
	Stop()
}

// Supervisors constructs one host bridge at a time. Keeping construction
// behind this consumer-owned interface prevents the daemon from depending on
// bridge.Supervisor's concrete implementation.
type Supervisors interface {
	New(host, hostKey, remoteSock, localSock string) Supervisor
}

// BindingStore is the slice of the binding store the daemon uses: reads for the
// control listing and resync, ByHostName for the receivers it wires up, and
// per-row removal — on agterm's session.closed event and lazily when a status
// push answers "no such session". There is deliberately no bulk removal here:
// agterm's tree can be window-scoped, so a tree result must never be able to
// delete bindings.
type BindingStore interface {
	Load() ([]bindings.Binding, error)
	ByRow(row string) (bindings.Binding, bool)
	ByHostName(host, name string) (bindings.Binding, bool)
	ForHost(host string) []bindings.Binding
	UnbindRow(row string) error
}
