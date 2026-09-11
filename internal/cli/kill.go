package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/k0nsta/agterm-remote/internal/token"
)

// Killer executes agr kill and reports every invalid name or remote failure
// while continuing with the remaining names.
type Killer struct {
	Sessions Sessions
	Errors   io.Writer
}

// Run returns zero only when every requested name was reaped successfully.
func (k *Killer) Run(ctx context.Context, host string, names []string) int {
	errw := k.Errors
	if errw == nil {
		errw = io.Discard
	}
	if !token.ValidHost(host) {
		_, _ = fmt.Fprintf(errw, "agr: invalid host %q\n", host)
		return 1
	}
	if len(names) == 0 {
		_, _ = fmt.Fprintln(errw, "agr: kill requires at least one session name")
		return 1
	}
	if k.Sessions == nil {
		_, _ = fmt.Fprintln(errw, "agr: remote session client is unavailable")
		return 1
	}
	failed := false
	for _, name := range names {
		if !token.Valid(name) {
			_, _ = fmt.Fprintf(errw, "agr: invalid name %q (use [A-Za-z0-9_.-])\n", name)
			failed = true
			continue
		}
		if err := k.Sessions.Reap(ctx, host, name); err != nil {
			_, _ = fmt.Fprintf(errw, "agr: kill %s: %v\n", name, err)
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}
