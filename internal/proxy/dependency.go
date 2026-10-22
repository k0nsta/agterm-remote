package proxy

import (
	"context"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

//go:generate go run go.uber.org/mock/mockgen -source=dependency.go -destination=mocks/dependency_mock.go -package=mocks

// TreeReader reads the live rows of agterm with their panes and foregrounds.
// Authorize reads it once per call.
type TreeReader interface {
	TreeRows(ctx context.Context) ([]agterm.TreeRow, error)
}

// BindingLoader reads every pane binding. Authorize reads it once per call.
type BindingLoader interface {
	Load() ([]bindings.Binding, error)
}

// SessionLister lists the agr sessions that exist on a host, so a new session
// never takes the name of one that does.
type SessionLister interface {
	Sessions(ctx context.Context, host string) ([]remote.Session, error)
}
