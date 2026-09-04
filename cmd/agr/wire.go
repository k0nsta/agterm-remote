package main

import (
	"context"
	"log"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/bridge"
	"github.com/k0nsta/agterm-remote/internal/cli"
	"github.com/k0nsta/agterm-remote/internal/daemon"
	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

type application struct {
	dirs       paths.Dirs
	control    *agterm.Ctl
	status     *agterm.Client
	remote     cli.Sessions
	tty        remote.TTY
	store      *bindings.Store
	bridge     applicationBridge
	daemon     daemonRunner
	picker     cli.Picker
	rows       cli.Rows
	labeler    cli.Labeler
	installer  cli.Installer
	openRemote cli.OpenerRemote
	hostInfo   cli.HostInfoReader
	moshPath   func() string
	doctor     cli.DoctorDependencies
}

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

func newApplication() *application {
	dirs := paths.New()
	ctlPath := agterm.CtlPath()
	sockPath := agterm.SocketPath()
	control := agterm.NewCtl(ctlPath, sockPath, nil, nil, nil)
	status := agterm.NewClient(sockPath, ctlPath, 5*time.Second, &agterm.ExecRunner{})
	remoteRunner := remote.NewRunnerWithVersion(nil, dirs, version)
	store := bindings.New(dirs)
	client := cli.NewDaemonClient(dirs)
	app := &application{
		dirs: dirs, control: control, status: status, remote: remoteRunner,
		tty: &remote.ExecTTY{}, store: store, bridge: client,
		installer: remoteRunner, openRemote: remoteRunner,
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
		app.labeler = control
	}
	app.daemon = daemon.New(daemon.Config{
		Dirs: dirs, UI: control, Remote: remoteRunner, Events: control,
		Rows: control, Sink: status, Supervisors: bridgeFactory{}, Bindings: store,
		Logger: log.Default(),
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
	_ cli.Picker                              = (*agterm.Ctl)(nil)
	_ cli.Rows                                = (*agterm.Ctl)(nil)
	_ cli.Labeler                             = (*agterm.Ctl)(nil)
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
