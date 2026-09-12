// Package cli contains agr's Mac-side command implementations.
package cli

import (
	"context"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/daemon"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

//go:generate go run go.uber.org/mock/mockgen -source=dependency.go -destination=mocks/dependency_mock.go -package=mocks

// Picker opens the native agterm picker.
type Picker interface {
	Pick(ctx context.Context, items []agterm.PickItem, prompt string) (agterm.PickResult, error)
}

// Rows lists live agterm rows.
type Rows interface {
	Tree(ctx context.Context) ([]string, error)
}

// Labeler updates the optional display metadata of an agterm row.
type Labeler interface {
	Rename(ctx context.Context, row, name string) error
	Context(ctx context.Context, row, text string) error
}

// Sessions lists and removes remote agr-owned sessions.
type Sessions interface {
	Sessions(ctx context.Context, host string) ([]remote.Session, error)
	Reap(ctx context.Context, host, name string) error
}

// Installer installs the embedded remote agr script and its hooks.
type Installer interface {
	InstallResult(ctx context.Context, host, mux string) (remote.InstallResult, error)
}

// Versioner reads the running agterm app version through its control socket.
type Versioner interface {
	Version(ctx context.Context) (string, error)
}

// AgtermInspector probes agterm capabilities that are newer than the base
// status protocol.
type AgtermInspector interface {
	RestoreMode(ctx context.Context) (string, error)
	SupportsContext(ctx context.Context) (bool, error)
}

// Prober gathers the fixed remote capability report used by doctor.
type Prober interface {
	Probe(ctx context.Context, host string) (remote.ProbeResult, error)
}

// DaemonStatus reads the daemon's per-host bridge state.
type DaemonStatus interface {
	Status(ctx context.Context, host string) ([]daemon.HostStatus, error)
}

// OpenerRemote contains the remote runner behavior needed by open.
type OpenerRemote interface {
	Sessions
	ResolveAgrPath(ctx context.Context, host string) (string, error)
}

// OpenBridge contains the daemon operations needed by open.
type OpenBridge interface {
	Up(ctx context.Context, host string) error
	ReloadBindings(ctx context.Context) error
}

// Bridge controls the daemon's per-host reverse bridge.
type Bridge interface {
	Up(ctx context.Context, host string) error
	Down(ctx context.Context, host string) error
}

// BindingStore is the slice of the binding store the CLI uses: the listing
// reads it, and open records the binding it just made.
type BindingStore interface {
	Load() ([]bindings.Binding, error)
	Bind(binding bindings.Binding) error
}
