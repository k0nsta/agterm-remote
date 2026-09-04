package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/k0nsta/agterm-remote/internal/token"
)

// RunUp starts the bridge for one host.
func RunUp(ctx context.Context, host string, bridge Bridge, errw io.Writer) int {
	return runBridge(ctx, "up", host, bridge, errw)
}

// RunDown stops the bridge for one host.
func RunDown(ctx context.Context, host string, bridge Bridge, errw io.Writer) int {
	return runBridge(ctx, "down", host, bridge, errw)
}

func runBridge(ctx context.Context, operation, host string, bridge Bridge, errw io.Writer) int {
	if errw == nil {
		errw = io.Discard
	}
	if !token.ValidHost(host) {
		_, _ = fmt.Fprintf(errw, "agr: invalid host %q\n", host)
		return 1
	}
	if bridge == nil {
		_, _ = fmt.Fprintln(errw, "agr: daemon client is unavailable")
		return 1
	}
	if ctx == nil {
		_, _ = fmt.Fprintln(errw, "agr: nil command context")
		return 1
	}
	var err error
	if operation == "up" {
		err = bridge.Up(ctx, host)
	} else {
		err = bridge.Down(ctx, host)
	}
	if err != nil {
		_, _ = fmt.Fprintf(errw, "agr: %s %s: %v\n", operation, host, err)
		return 1
	}
	return 0
}
