package daemon

import (
	"context"
	"io"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/proxy"
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
// per-row removal of every pane's binding — on agterm's session.closed event and lazily when a status
// push answers "no such session". There is deliberately no bulk removal here:
// agterm's tree can be window-scoped, so a tree result must never be able to
// delete bindings.
type BindingStore interface {
	Load() ([]bindings.Binding, error)
	ForRow(row string) []bindings.Binding
	ByHostName(host, name string) (bindings.Binding, bool)
	ForHost(host string) []bindings.Binding
	UnbindRow(row string) error
}

// CtlBackend is the agtermctl the proxy runs calls on: the streaming call, the
// tree the page shim and the pre-bind read, and the dialog calls of consent.
type CtlBackend interface {
	Exec(ctx context.Context, stdin []byte, stdout, stderr io.Writer, args ...string) (int, error)
	TreeRows(ctx context.Context) ([]agterm.TreeRow, error)
	AskOpen(ctx context.Context, request agterm.AskRequest) (string, error)
	AskResult(ctx context.Context, id string) (agterm.AskAnswer, error)
	AskCancel(ctx context.Context, id string) error
}

// Authorizer decides whether a host's agtermctl call may run, and re-checks
// the program rule once the user has answered a consent dialog.
type Authorizer interface {
	Authorize(ctx context.Context, host string, caller bindings.Binding, hdr proxy.Header, argv []string, stdin []byte) proxy.Decision
	CheckAgent(ctx context.Context, row, paneID string) error
}

// Rewriter turns an authorized call into the command the daemon runs.
type Rewriter interface {
	Rewrite(ctx context.Context, req proxy.Request, call proxy.Call) (proxy.Prepared, error)
}

// ProxyStore is the binding writes the proxy makes: the pre-bind of a row made
// by `session new`, and the consent grants.
type ProxyStore interface {
	Bind(binding bindings.Binding) error
	AddGrant(host, name string, grant bindings.Grant) error
	DropGrant(host, name string, grant bindings.Grant) error
}
