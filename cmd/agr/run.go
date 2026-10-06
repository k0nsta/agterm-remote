package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/cli"
)

var version = "dev"

const usage = "usage: agr <command> [args...]"

const help = `usage: agr <command> [args...]

Commands:
  open <host> [name] [--cwd <dir>] [--parent <name>]
                          Attach to a remote session; without name, use the picker
  ls <host>                List agr-owned remote sessions
  kill <host> <name>…      Kill one or more agr-owned remote sessions
  up <host>                Start the host's status bridge
  down <host>              Stop the host's status bridge
  install <host> [--mux zmx|tmux]
                          Install the remote script and agent hooks
  daemon                  Run the local bridge daemon
  doctor <host>            Check local and remote prerequisites
  shell <host> [--cwd <dir>]
                          Open a plain remote login shell (no session)
  end [row]                Kill a row's remote sessions and close the row

Options:
  --help, -h               Show this help
  --version                Show the agr version`

func run(args []string, out, errw io.Writer) int {
	return runWithApplication(args, out, errw, nil)
}

type commandHandler func(context.Context, *application, []string, io.Writer, io.Writer) int

var commandHandlers = map[string]commandHandler{
	"open":    runOpen,
	"ls":      runList,
	"kill":    runKill,
	"up":      runUp,
	"down":    runDown,
	"install": runInstall,
	"daemon":  runDaemon,
	"doctor":  runDoctor,
	"shell":   runShell,
	"end":     runEnd,
	// Hook entry points, run by agterm's hooks.conf rather than by hand, so
	// they stay out of --help.
	"on-split":   runOnSplit,
	"on-scratch": runOnScratch,
}

func runWithApplication(args []string, out, errw io.Writer, app *application) int {
	if len(args) == 0 {
		writeUsage(out)
		return 0
	}

	if args[0] == "--version" {
		_, _ = fmt.Fprintln(out, version)
		return 0
	}
	if args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprintln(out, help)
		return 0
	}
	if handler, ok := commandHandlers[args[0]]; ok {
		if app == nil {
			app = newApplication()
		}
		return handler(context.Background(), app, args[1:], out, errw)
	}

	writeUsage(errw)
	return 2
}

func runOnScratch(ctx context.Context, app *application, args []string, _, errw io.Writer) int {
	return runPaneHook(ctx, app, args, errw, "on-scratch", cli.OnScratch)
}

func runShell(ctx context.Context, app *application, args []string, out, errw io.Writer) int {
	positional, opts, ok := parseOpenArgs(args)
	if !ok || len(positional) != 1 || opts.Parent != "" {
		_, _ = fmt.Fprintln(errw, "usage: agr shell <host> [--cwd <dir>]")
		return 2
	}
	return cli.RunShell(ctx, positional[0], opts.Cwd, cli.ShellDependencies{
		TTY: app.tty, HostInfo: app.hostInfo, MoshPath: app.moshPath, Out: out,
	}, errw)
}

// runEnd takes the row from its argument (a keymap command passes
// {AGT_SESSION_ID}) or, run by hand inside agterm, from the environment.
func runEnd(ctx context.Context, app *application, args []string, _, errw io.Writer) int {
	if len(args) > 1 {
		_, _ = fmt.Fprintln(errw, "usage: agr end [row]")
		return 2
	}
	row := optionalArg(args, 0)
	for _, key := range []string{"AGT_SESSION_ID", "AGTERM_SESSION_ID"} {
		if row == "" {
			row = os.Getenv(key)
		}
	}
	if row == "" {
		_, _ = fmt.Fprintln(errw, "usage: agr end [row] (no agterm row in the environment)")
		return 2
	}
	return cli.RunEnd(ctx, row, app.end(), errw)
}

func runOnSplit(ctx context.Context, app *application, args []string, _, errw io.Writer) int {
	return runPaneHook(ctx, app, args, errw, "on-split", cli.OnSplit)
}

// runPaneHook reads the event from agterm's hook environment and reports a
// failure to the hook log as well as stderr: agterm runs hooks detached, so
// stderr alone reaches no one.
func runPaneHook(ctx context.Context, app *application, args []string, errw io.Writer, verb string,
	hook func(context.Context, string, string, cli.PaneHookDependencies) error) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintf(errw, "usage: agr %s (run from an agterm hook)\n", verb)
		return 2
	}
	row, status := cli.HookEvent(hookStdin(), os.Getenv)
	if status != "shown" && status != "hidden" {
		// Not a pane event this hook understands: say so, or a mismatch with
		// agterm's event shape would look exactly like a quiet no-op.
		app.logHookFailure(verb, fmt.Errorf("ignored event row=%q status=%q", row, status))
		return 0
	}
	deps := app.paneHooks()
	if err := hook(ctx, row, status, deps); err != nil {
		_, _ = fmt.Fprintf(errw, "agr: %s: %v\n", verb, err)
		app.logHookFailure(verb, err)
		return 1
	}
	return 0
}

// hookStdin is the event agterm pipes to a hook, or nil when stdin is a
// terminal (run by hand), where reading would block.
func hookStdin() io.Reader {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice != 0 {
		return nil
	}
	return os.Stdin
}

func runOpen(ctx context.Context, app *application, args []string, out, errw io.Writer) int {
	positional, opts, ok := parseOpenArgs(args)
	if !ok || len(positional) < 1 || len(positional) > 2 {
		_, _ = fmt.Fprintln(errw, "usage: agr open <host> [name] [--cwd <dir>] [--parent <name>]")
		return 2
	}
	return cli.RunOpen(ctx, positional[0], optionalArg(positional, 1), opts, cli.OpenDependencies{
		Dirs: app.dirs, Remote: app.openRemote, Picker: app.picker, Rows: app.rows,
		Store: app.store, Bridge: app.bridge, Labeler: app.labeler,
		HostInfo: app.hostInfo, TTY: app.tty, Out: out, ErrOut: errw, MoshPath: app.moshPath,
	}, errw)
}

func runList(ctx context.Context, app *application, args []string, out, errw io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(errw, "usage: agr ls <host>")
		return 2
	}
	text, err := cli.List(ctx, args[0], app.remote, app.store, app.rows)
	if err != nil {
		_, _ = fmt.Fprintf(errw, "agr: ls: %v\n", err)
		return 1
	}
	_, _ = io.WriteString(out, text)
	return 0
}

func runKill(ctx context.Context, app *application, args []string, _, errw io.Writer) int {
	if len(args) < 2 {
		_, _ = fmt.Fprintln(errw, "usage: agr kill <host> <name>…")
		return 2
	}
	return (&cli.Killer{Sessions: app.remote, Errors: errw}).Run(ctx, args[0], args[1:])
}

func runUp(ctx context.Context, app *application, args []string, _, errw io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(errw, "usage: agr up <host>")
		return 2
	}
	return cli.RunUp(ctx, args[0], app.bridge, errw)
}

func runDown(ctx context.Context, app *application, args []string, _, errw io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(errw, "usage: agr down <host>")
		return 2
	}
	return cli.RunDown(ctx, args[0], app.bridge, errw)
}

func runInstall(ctx context.Context, app *application, args []string, out, errw io.Writer) int {
	return cli.RunInstall(ctx, args, app.installer, out, errw)
}

func runDaemon(ctx context.Context, app *application, args []string, _, errw io.Writer) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintln(errw, "usage: agr daemon")
		return 2
	}
	if app.daemon == nil {
		_, _ = fmt.Fprintln(errw, "agr: daemon is unavailable")
		return 1
	}
	if err := app.daemon.Run(ctx); err != nil {
		_, _ = fmt.Fprintf(errw, "agr: daemon: %v\n", err)
		return 1
	}
	return 0
}

func runDoctor(ctx context.Context, app *application, args []string, out, errw io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(errw, "usage: agr doctor <host>")
		return 2
	}
	if app == nil {
		_, _ = fmt.Fprintln(errw, "agr: doctor is unavailable")
		return 1
	}
	deps := app.doctor
	if deps.LocalVersion == "" {
		deps.LocalVersion = version
	}
	if deps.SocketPath == "" {
		deps.SocketPath = agterm.SocketPath()
	}
	return cli.RunDoctor(ctx, args[0], deps, out, errw)
}

// parseOpenArgs splits open's positionals from its two valued flags, which
// may appear anywhere after the verb. A flag without a value, or repeated,
// is a usage error.
func parseOpenArgs(args []string) ([]string, cli.OpenOptions, bool) {
	var positional []string
	var opts cli.OpenOptions
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cwd", "--parent":
			if i+1 >= len(args) || seen[args[i]] {
				return nil, cli.OpenOptions{}, false
			}
			seen[args[i]] = true
			if args[i] == "--cwd" {
				opts.Cwd = args[i+1]
			} else {
				opts.Parent = args[i+1]
			}
			i++
		default:
			positional = append(positional, args[i])
		}
	}
	return positional, opts, true
}

func optionalArg(args []string, index int) string {
	if index < 0 || index >= len(args) {
		return ""
	}
	return args[index]
}

func writeUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, usage)
}
