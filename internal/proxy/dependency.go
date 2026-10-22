package proxy

import (
	"context"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
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
