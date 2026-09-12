package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/k0nsta/agterm-remote/internal/token"
)

// RunInstall parses agr install's host and optional multiplexer override, runs
// the install, and confirms on out what was installed and which multiplexer
// and relay the probe selected — the two facts the user needs next.
func RunInstall(ctx context.Context, args []string, installer Installer, out, errw io.Writer) int {
	if out == nil {
		out = io.Discard
	}
	if errw == nil {
		errw = io.Discard
	}
	host, mux, err := parseInstallArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(errw, "agr: %v\n", err)
		return 2
	}
	if installer == nil {
		_, _ = fmt.Fprintln(errw, "agr: remote installer is unavailable")
		return 1
	}
	if ctx == nil {
		_, _ = fmt.Fprintln(errw, "agr: nil command context")
		return 1
	}
	result, err := installer.InstallResult(ctx, host, mux)
	if err != nil {
		_, _ = fmt.Fprintf(errw, "agr: install %s: %v\n", host, err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "installed agr %s on %s (mux %s, relay %s)\n", result.Version, host, result.Mux, result.Relay)
	return 0
}

func parseInstallArgs(args []string) (host, mux string, err error) {
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--mux":
			if mux != "" {
				return "", "", fmt.Errorf("--mux specified more than once")
			}
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--mux requires zmx or tmux")
			}
			i++
			mux = args[i]
		case strings.HasPrefix(arg, "--mux="):
			if mux != "" {
				return "", "", fmt.Errorf("--mux specified more than once")
			}
			mux = strings.TrimPrefix(arg, "--mux=")
		default:
			if strings.HasPrefix(arg, "-") {
				return "", "", fmt.Errorf("unknown install option %q", arg)
			}
			positional = append(positional, arg)
		}
	}
	if len(positional) != 1 {
		return "", "", fmt.Errorf("usage: agr install <host> [--mux zmx|tmux]")
	}
	host = positional[0]
	if !token.ValidHost(host) {
		return "", "", fmt.Errorf("invalid host %q", host)
	}
	if mux != "" && mux != "zmx" && mux != "tmux" {
		return "", "", fmt.Errorf("unsupported multiplexer %q (choose zmx or tmux)", mux)
	}
	return host, mux, nil
}
