// Package remote contains the local adapters that invoke agr on a remote
// host.
package remote

import "context"

//go:generate go run go.uber.org/mock/mockgen -source=dependency.go -destination=mocks/dependency_mock.go -package=mocks

// SSH runs a non-interactive command on a remote host. argv is kept separate
// from the SSH destination so callers never need to build a shell command.
type SSH interface {
	Run(ctx context.Context, host string, stdin []byte, argv ...string) ([]byte, error)
}

// TTY runs an interactive command with the caller's terminal attached. argv
// contains the complete local command line, including its executable.
type TTY interface {
	Interactive(ctx context.Context, argv ...string) error
}

// TerminfoSource dumps the local terminal's terminfo entry so install can carry
// it to a host that lacks it.
type TerminfoSource interface {
	Infocmp(ctx context.Context, term string) ([]byte, error)
}
