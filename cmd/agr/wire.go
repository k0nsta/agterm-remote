package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agrpath"
	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/bridge"
	"github.com/k0nsta/agterm-remote/internal/cli"
	"github.com/k0nsta/agterm-remote/internal/daemon"
	"github.com/k0nsta/agterm-remote/internal/handled"
	"github.com/k0nsta/agterm-remote/internal/pasteboard"
	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/proxy"
	"github.com/k0nsta/agterm-remote/internal/receiver"
	"github.com/k0nsta/agterm-remote/internal/remote"
	"golang.org/x/term"
)

type application struct {
	dirs        paths.Dirs
	control     *agterm.Ctl
	status      *agterm.Client
	remote      cli.Sessions
	tty         remote.TTY
	store       *bindings.Store
	bridge      applicationBridge
	daemon      daemonRunner
	picker      cli.Picker
	rows        cli.Rows
	paneTree    cli.PaneTree
	labeler     cli.Labeler
	installer   cli.Installer
	openRemote  cli.OpenerRemote
	cwd         cli.RemoteCwd
	pasteRemote cli.PasteUploader
	clipboard   cli.PasteClipboard
	screenshots cli.PasteScreenshots
	hostInfo    cli.HostInfoReader
	moshPath    func() string
	doctor      cli.DoctorDependencies
}

// stableAgr is where hooks, keymap commands and setup learn the path to run
// agr by. It is a variable so a test can pin it.
var stableAgr = agrpath.Stable

type applicationBridge interface {
	cli.Bridge
	cli.OpenBridge
}

type daemonRunner interface {
	Run(context.Context) error
}

type bridgeFactory struct{}

func (bridgeFactory) New(host, hostKey, remoteSock, localSock string) daemon.Supervisor {
	return bridge.New(host, hostKey, remoteSock, localSock, &bridge.ExecRunner{})
}

// paneHooks wires the pane-hook dependencies. The control client is the
// application's own: SocketPath already falls back to the AGT_SOCKET agterm
// hands a hook.
func (a *application) paneHooks() cli.PaneHookDependencies {
	ctl := a.control
	if ctl == nil {
		ctl = agterm.NewCtl("", "", nil, nil, nil)
	}
	return cli.PaneHookDependencies{
		Panes: ctl, Store: a.store, Remote: a.cwd, Handled: handled.New(a.dirs.Handled()), Agr: stableAgr(),
	}
}

// end wires agr end on the application's control client, the same one the
// pane hooks use (SocketPath covers a keymap command's AGT_SOCKET).
func (a *application) end() cli.EndDependencies {
	ctl := a.control
	if ctl == nil {
		ctl = agterm.NewCtl("", "", nil, nil, nil)
	}
	return cli.EndDependencies{Store: a.store, Reaper: a.remote, Confirm: ctl, Rows: ctl}
}

// paste wires the command's local pasteboard/screenshot sources and the
// remote runner to the application's agterm control client.
func (a *application) paste() cli.PasteDependencies {
	ctl := a.control
	if ctl == nil {
		ctl = agterm.NewCtl("", "", nil, nil, nil)
	}
	return cli.PasteDependencies{
		Store: a.store, Clipboard: a.clipboard, Screenshots: a.screenshots,
		Remote: a.pasteRemote, Panes: ctl,
	}
}

// setup wires agr setup. Without agtermctl it still writes the files, to the
// default config directory, and leaves the reload to agterm's next start. The
// key for End remote session is asked only on a terminal; Paste never prompts.
func (a *application) setup(out io.Writer, endKey, pasteKey string) cli.SetupDependencies {
	home, _ := os.UserHomeDir()
	deps := cli.SetupDependencies{Agr: stableAgr(), Home: home, Out: out, EndKey: endKey, PasteKey: pasteKey, Version: version}
	if home != "" {
		deps.SkillDir = filepath.Join(home, ".claude", "skills", "agr")
	}
	if a.control != nil && agterm.CtlPath() != "" {
		deps.Config = a.control
	}
	// /dev/null is a character device too, so ask the terminal itself.
	if term.IsTerminal(int(os.Stdin.Fd())) {
		deps.AskKey = func() (string, error) {
			_, _ = fmt.Fprint(out, "Key for End remote session (e.g. ctrl+a>x, Enter for none): ")
			answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return answer, err
		}
	}
	return deps
}

// logHookFailure appends one line to the hook log; a failure to log is
// dropped, there is nowhere left to report it.
func (a *application) logHookFailure(verb string, failure error) {
	file, err := os.OpenFile(a.dirs.HookLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = fmt.Fprintf(file, "%s %s: %v\n", time.Now().UTC().Format(time.RFC3339), verb, failure)
}

func newApplication() *application {
	dirs := paths.New()
	ctlPath := agterm.CtlPath()
	sockPath := agterm.SocketPath()
	control := agterm.NewCtl(ctlPath, sockPath, nil, nil, nil)
	status := agterm.NewClient(sockPath, ctlPath, 5*time.Second, &agterm.ExecRunner{})
	remoteRunner := remote.NewRunnerWithVersion(nil, dirs, version)
	remoteRunner.SetTerm(os.Getenv("TERM"))
	store := bindings.New(dirs)
	client := cli.NewDaemonClient(dirs)
	app := &application{
		dirs: dirs, control: control, status: status, remote: remoteRunner,
		tty: &remote.ExecTTY{}, store: store, bridge: client, pasteRemote: remoteRunner,
		clipboard: pasteboard.NewClipboard(nil), screenshots: pasteboard.NewScreenshots(nil, ""),
		installer: remoteRunner, openRemote: remoteRunner, cwd: remoteRunner,
		doctor: cli.DoctorDependencies{
			LocalVersion: version, SocketPath: sockPath, AppVersion: status,
			Agterm: control, CtlPath: ctlPath, Remote: remoteRunner, Daemon: client,
		},
		hostInfo: func(host string) (remote.HostInfo, error) {
			return remote.LoadHostInfo(dirs, host)
		},
	}
	if ctlPath != "" {
		app.picker = control
		app.rows = control
		app.paneTree = control
		app.labeler = control
	}
	app.daemon = daemon.New(daemon.Config{
		Dirs: dirs, UI: control, Remote: remoteRunner, Events: control,
		Rows: control, Sink: status, Supervisors: bridgeFactory{}, Bindings: store,
		Ctl: control, Authorizer: proxy.NewPolicy(control, store), ProxyStore: store,
		Rewriter: &proxy.Rewriter{Agr: stableAgr(), Root: dirs.Proxy(), Sessions: remoteRunner, Binds: store},
		// No Logger: injecting one makes Daemon.openLog return early, so
		// dirs.Log() is never created. The daemon is normally started detached
		// with stdio on /dev/null, so a stderr logger discards every
		// diagnostic it writes. Leaving this nil is what puts the log on disk.
	})
	return app
}

// These assertions deliberately live at the final wiring point, where every
// producer and consumer interface from the preceding tasks exists.
var (
	_ daemon.Remote                           = (*remote.Runner)(nil)
	_ daemon.UI                               = (*agterm.Ctl)(nil)
	_ daemon.EventSource                      = (*agterm.Ctl)(nil)
	_ daemon.Rows                             = (*agterm.Ctl)(nil)
	_ daemon.StatusSink                       = (*agterm.Client)(nil)
	_ daemon.CtlBackend                       = (*agterm.Ctl)(nil)
	_ daemon.Authorizer                       = (*proxy.Policy)(nil)
	_ daemon.Rewriter                         = (*proxy.Rewriter)(nil)
	_ daemon.ProxyStore                       = (*bindings.Store)(nil)
	_ proxy.TreeReader                        = (*agterm.Ctl)(nil)
	_ proxy.BindingLoader                     = (*bindings.Store)(nil)
	_ proxy.SessionLister                     = (*remote.Runner)(nil)
	_ receiver.Resolver                       = (*bindings.Store)(nil)
	_ cli.Picker                              = (*agterm.Ctl)(nil)
	_ cli.Rows                                = (*agterm.Ctl)(nil)
	_ cli.PaneTree                            = (*agterm.Ctl)(nil)
	_ cli.Labeler                             = (*agterm.Ctl)(nil)
	_ cli.Panes                               = (*agterm.Ctl)(nil)
	_ cli.RemoteCwd                           = (*remote.Runner)(nil)
	_ cli.PasteUploader                       = (*remote.Runner)(nil)
	_ cli.PastePanes                          = (*agterm.Ctl)(nil)
	_ cli.HandledPanes                        = (*handled.Store)(nil)
	_ cli.RowBindings                         = (*bindings.Store)(nil)
	_ cli.Confirmer                           = (*agterm.Ctl)(nil)
	_ cli.RowCloser                           = (*agterm.Ctl)(nil)
	_ cli.AgtermConfig                        = (*agterm.Ctl)(nil)
	_ cli.Sessions                            = (*remote.Runner)(nil)
	_ cli.Installer                           = (*remote.Runner)(nil)
	_ cli.Versioner                           = (*agterm.Client)(nil)
	_ cli.AgtermInspector                     = (*agterm.Ctl)(nil)
	_ cli.Prober                              = (*remote.Runner)(nil)
	_ cli.DaemonStatus                        = (*cli.DaemonClient)(nil)
	_ cli.Bridge                              = (*cli.DaemonClient)(nil)
	_ daemon.Supervisors                      = bridgeFactory{}
	_ daemon.Supervisor                       = (*bridge.Supervisor)(nil)
	_ interface{ Run(context.Context) error } = (*daemon.Daemon)(nil)
)
