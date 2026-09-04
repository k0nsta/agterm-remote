package main

import (
	"context"
	"fmt"
	"io"

	"github.com/k0nsta/agterm-remote/internal/cli"
)

var version = "dev"

const usage = "usage: agr <command> [args...]"

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
	"doctor":  runDoctorStub,
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
	if handler, ok := commandHandlers[args[0]]; ok {
		if app == nil {
			app = newApplication()
		}
		return handler(context.Background(), app, args[1:], out, errw)
	}

	writeUsage(errw)
	return 2
}

func runOpen(ctx context.Context, app *application, args []string, out, errw io.Writer) int {
	if len(args) < 1 || len(args) > 2 {
		_, _ = fmt.Fprintln(errw, "usage: agr open <host> [name]")
		return 2
	}
	return cli.RunOpen(ctx, args[0], optionalArg(args, 1), cli.OpenDependencies{
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
	if len(args) < 1 {
		_, _ = fmt.Fprintln(errw, "usage: agr kill <host> <name>…")
		return 2
	}
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

func runInstall(ctx context.Context, app *application, args []string, _, errw io.Writer) int {
	return cli.RunInstall(ctx, args, app.installer, errw)
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

func runDoctorStub(_ context.Context, _ *application, _ []string, _ io.Writer, errw io.Writer) int {
	_, _ = fmt.Fprintln(errw, "not implemented yet")
	return 1
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
