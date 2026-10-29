package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/cli"
	"github.com/k0nsta/agterm-remote/internal/settings"
)

var version = "dev"

const usage = "usage: agr <command> [args...]"

const help = `usage: agr <command> [args...]

Commands:
  open <host> [name] [--cwd <dir>] [--parent <name>] [--command <cmd>] [--orchestrator <row>:<pane>]
                          Attach to a remote session; without name, use the picker.
                          --command runs only in a new session
  run <host> [--cwd <dir>] -- <argv>…
                          Run a program on the host in one ssh; its exit status is passed through
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
  paste [row] [pane-id]   Send the clipboard or latest screenshot to a remote row
  skill                   Print the agr skill for Claude (agr setup and agr install write it)
  new [row]                Pick a host, session and workspace; open it in a new row
  setup [--end-key <chord>] [--paste-key <chord>] [--new-key <chord>] [--page-size <percent>]
                          Make agterm's ⌘D and ⌘J follow a remote row; add End, Paste and New commands

Options:
  --help, -h               Show this help
  --version                Show the agr version`

func run(args []string, out, errw io.Writer) int {
	return runWithApplication(args, out, errw, nil)
}

type commandHandler func(context.Context, *application, []string, io.Writer, io.Writer) int

var commandHandlers = map[string]commandHandler{
	"open":    runOpen,
	"run":     runRun,
	"ls":      runList,
	"kill":    runKill,
	"up":      runUp,
	"down":    runDown,
	"install": runInstall,
	"daemon":  runDaemon,
	"doctor":  runDoctor,
	"shell":   runShell,
	"end":     runEnd,
	"paste":   runPaste,
	"new":     runNew,
	"setup":   runSetup,
	"skill":   runSkill,
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
	if !ok || len(positional) != 1 || opts.Parent != "" || opts.Command != "" || opts.Orchestrator != "" {
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

// runPaste takes the row and pane id from keymap arguments, or the row from
// the environment when invoked by hand inside agterm.
func runPaste(ctx context.Context, app *application, args []string, _, errw io.Writer) int {
	if len(args) > 2 {
		_, _ = fmt.Fprintln(errw, "usage: agr paste [row] [pane-id]")
		return 2
	}
	row := optionalArg(args, 0)
	for _, key := range []string{"AGT_SESSION_ID", "AGTERM_SESSION_ID"} {
		if row == "" {
			row = os.Getenv(key)
		}
	}
	if row == "" {
		_, _ = fmt.Fprintln(errw, "usage: agr paste [row] [pane-id] (no agterm row in the environment)")
		return 2
	}
	return cli.RunPaste(ctx, row, optionalArg(args, 1), app.paste(), errw)
}

func runSkill(_ context.Context, _ *application, args []string, out, errw io.Writer) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintln(errw, "usage: agr skill")
		return 2
	}
	return cli.RunSkill(version, out, errw)
}

// runNew takes the current row from its argument (a keymap command passes
// {AGT_SESSION_ID}) or the environment; without one the row goes to the
// chosen workspace's end. The window comes from the environment the same way,
// and without one agterm uses the frontmost.
func runNew(ctx context.Context, app *application, args []string, _, errw io.Writer) int {
	if len(args) > 1 {
		_, _ = fmt.Fprintln(errw, "usage: agr new [row]")
		return 2
	}
	row := optionalArg(args, 0)
	for _, key := range []string{"AGT_SESSION_ID", "AGTERM_SESSION_ID"} {
		if row == "" {
			row = os.Getenv(key)
		}
	}
	window := os.Getenv("AGT_WINDOW_ID")
	if window == "" {
		window = os.Getenv("AGTERM_WINDOW_ID")
	}
	return cli.RunNew(ctx, row, app.newRow(window), errw)
}

const setupUsage = "usage: agr setup [--end-key <chord>] [--paste-key <chord>] [--new-key <chord>] [--page-size <percent>]"

func runSetup(ctx context.Context, app *application, args []string, out, errw io.Writer) int {
	keys := map[string]string{}
	pageSize := -1
	for i := 0; i < len(args); i++ {
		_, seen := keys[args[i]]
		known := args[i] == "--end-key" || args[i] == "--paste-key" || args[i] == "--new-key" || args[i] == "--page-size"
		if !known || seen || i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
			_, _ = fmt.Fprintln(errw, setupUsage)
			return 2
		}
		if args[i] == "--page-size" {
			size, err := strconv.Atoi(args[i+1])
			if err != nil || size < 1 || size > 100 {
				_, _ = fmt.Fprintln(errw, setupUsage+" (percent is 1-100)")
				return 2
			}
			pageSize = size
		}
		keys[args[i]] = args[i+1]
		i++
	}
	if pageSize != -1 {
		if code := savePageSize(pageSize, out, errw); code != 0 {
			return code
		}
	}
	return cli.RunSetup(ctx, app.setup(out, keys["--end-key"], keys["--paste-key"], keys["--new-key"]), errw)
}

// savePageSize stores the default size of a host's page; 100 means full.
func savePageSize(size int, out, errw io.Writer) int {
	path := settings.Path()
	current, err := settings.Load(path)
	if err != nil {
		_, _ = fmt.Fprintf(errw, "agr setup: %v\n", err)
		return 1
	}
	current.PageSize = size
	if size == 100 {
		current.PageSize = 0
	}
	if err := settings.Save(path, current); err != nil {
		_, _ = fmt.Fprintf(errw, "agr setup: save %s: %v\n", path, err)
		return 1
	}
	if current.PageSize == 0 {
		_, _ = fmt.Fprintln(out, "host pages: full")
	} else {
		_, _ = fmt.Fprintf(out, "host pages: %d%% on rows a host owns whole (a split with a Mac pane stays full)\n", size)
	}
	return 0
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
		_, _ = fmt.Fprintln(errw, "usage: agr open <host> [name] [--cwd <dir>] [--parent <name>] [--command <cmd>] [--orchestrator <row>:<pane>]")
		return 2
	}
	return cli.RunOpen(ctx, positional[0], optionalArg(positional, 1), opts, cli.OpenDependencies{
		Dirs: app.dirs, Remote: app.openRemote, Picker: app.picker, Rows: app.rows, Panes: app.paneTree,
		Store: app.store, Bridge: app.bridge, Labeler: app.labeler,
		HostInfo: app.hostInfo, TTY: app.tty, Out: out, ErrOut: errw, MoshPath: app.moshPath,
	}, errw)
}

// runRun takes the host, an optional --cwd, and the program after "--". The
// separator is required: without it a program's own flags would be read as
// agr's.
func runRun(ctx context.Context, app *application, args []string, out, errw io.Writer) int {
	const usage = "usage: agr run <host> [--cwd <dir>] -- <argv>…"
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		_, _ = fmt.Fprintln(errw, usage)
		return 2
	}
	host, rest := args[0], args[1:]
	cwd := ""
	if len(rest) >= 2 && rest[0] == "--cwd" {
		cwd, rest = rest[1], rest[2:]
	}
	if len(rest) < 2 || rest[0] != "--" {
		_, _ = fmt.Fprintln(errw, usage)
		return 2
	}
	return cli.RunRun(ctx, host, cwd, rest[1:], cli.RunDependencies{
		TTY: app.tty, Out: out,
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

// parseOpenArgs splits open's positionals from its valued flags, which
// may appear anywhere after the verb. A flag without a value, or repeated,
// is a usage error.
func parseOpenArgs(args []string) ([]string, cli.OpenOptions, bool) {
	var positional []string
	var opts cli.OpenOptions
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cwd", "--parent", "--command", "--orchestrator":
			if i+1 >= len(args) || seen[args[i]] {
				return nil, cli.OpenOptions{}, false
			}
			seen[args[i]] = true
			switch args[i] {
			case "--cwd":
				opts.Cwd = args[i+1]
			case "--parent":
				opts.Parent = args[i+1]
			case "--command":
				opts.Command = args[i+1]
			default:
				opts.Orchestrator = args[i+1]
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
