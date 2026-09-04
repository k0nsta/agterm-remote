// Package cli contains agr's Mac-side command implementations.
package cli

import (
	"context"

	"github.com/k0nsta/agterm-remote/internal/agterm"
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
