// Package daemon owns agr's local process, host bridge, and binding lifecycle.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/bridge"
	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/receiver"
	"github.com/k0nsta/agterm-remote/internal/token"
)

// Config contains the daemon's injected dependencies.
type Config struct {
	Dirs        paths.Dirs
	UI          UI
	Remote      Remote
	Events      EventSource
	Rows        Rows
	Sink        StatusSink
	Supervisors Supervisors
	Bindings    *bindings.Store
	Logger      *log.Logger
}

// Daemon supervises all hosts represented by the persistent binding store.
type Daemon struct {
	dirs        paths.Dirs
	ui          UI
	remote      Remote
	events      EventSource
	rows        Rows
	sink        StatusSink
	supervisors Supervisors
	store       *bindings.Store
	logger      *log.Logger

	mu       sync.Mutex
	started  bool
	closing  bool
	ctx      context.Context
	cancel   context.CancelFunc
	lockFile *os.File
	logFile  *os.File
	children sync.WaitGroup
	hosts    map[string]*hostRuntime
	ensured  map[string]bool
	ensureMu sync.Mutex
	control  net.Listener

	recoveryRunning bool
	recoveryLogged  bool
	recoveryDone    chan struct{}
	eventReset      chan struct{}
}

type hostRuntime struct {
	host       string
	hostKey    string
	localSock  string
	listener   *receiver.Listener
	cancel     context.CancelFunc
	supervisor Supervisor
	state      string
	since      time.Time
	attempts   int
}

type versioner interface {
	Version(context.Context) (string, error)
}

type aliveMarker interface {
	MarkAlive()
}

// New constructs a daemon from injected dependencies.
func New(config Config) *Daemon {
	dirs := config.Dirs
	if dirs.Cache == "" {
		dirs = paths.New()
	}
	store := config.Bindings
	if store == nil {
		store = bindings.New(dirs)
	}
	return &Daemon{
		dirs:        dirs,
		ui:          config.UI,
		remote:      config.Remote,
		events:      config.Events,
		rows:        config.Rows,
		sink:        config.Sink,
		supervisors: config.Supervisors,
		store:       store,
		logger:      config.Logger,
		hosts:       make(map[string]*hostRuntime),
		ensured:     make(map[string]bool),
		eventReset:  make(chan struct{}),
	}
}

// NewDaemon is a descriptive alias for New.
func NewDaemon(config Config) *Daemon { return New(config) }

// Start acquires the instance lock, writes the pidfile, performs startup
// checks, and starts one listener and bridge per bound host. Start is useful
// to callers that need to own the surrounding wait loop; Run is the normal
// signal-aware entry point.
func (d *Daemon) Start(ctx context.Context) error {
	if d == nil {
		return errors.New("nil daemon")
	}
	if ctx == nil {
		return errors.New("nil daemon context")
	}

	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return errors.New("daemon is already started")
	}
	if err := d.acquireLock(); err != nil {
		d.mu.Unlock()
		return err
	}
	d.ctx, d.cancel = context.WithCancel(ctx)
	d.started = true
	d.closing = false
	d.mu.Unlock()

	if err := d.openLog(); err != nil {
		_ = d.Close()
		return err
	}
	if err := d.writePID(); err != nil {
		_ = d.Close()
		return err
	}
	if err := d.initialize(d.ctx); err != nil {
		_ = d.Close()
		return err
	}
	return nil
}

// Run starts the daemon and waits for context cancellation or SIGINT/SIGTERM,
// then shuts down all child processes and removes owned state files.
func (d *Daemon) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil daemon context")
	}
	signalCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	if err := d.Start(signalCtx); err != nil {
		return err
	}
	<-signalCtx.Done()
	return d.Close()
}

// Close stops all host children and releases the daemon's single-instance
// lock. It is idempotent and also performs cleanup after a failed Start.
func (d *Daemon) Close() error {
	if d == nil {
		return nil
	}

	d.mu.Lock()
	if !d.started && d.lockFile == nil {
		d.mu.Unlock()
		return nil
	}
	if d.closing {
		d.mu.Unlock()
		return nil
	}
	d.closing = true
	if d.cancel != nil {
		d.cancel()
	}
	hosts := make([]*hostRuntime, 0, len(d.hosts))
	for _, host := range d.hosts {
		hosts = append(hosts, host)
	}
	d.mu.Unlock()

	for _, host := range hosts {
		if host.cancel != nil {
			host.cancel()
		}
		if host.supervisor != nil {
			host.supervisor.Stop()
		}
		if host.listener != nil {
			_ = os.Remove(host.localSock)
		}
	}
	d.mu.Lock()
	control := d.control
	d.control = nil
	d.mu.Unlock()
	if control != nil {
		_ = control.Close()
	}
	d.children.Wait()
	for _, host := range hosts {
		_ = os.Remove(host.localSock)
	}

	var closeErr error
	if err := os.Remove(d.dirs.Sock()); err != nil && !errors.Is(err, os.ErrNotExist) {
		closeErr = err
	}
	if err := os.Remove(d.dirs.Pid()); err != nil && !errors.Is(err, os.ErrNotExist) && closeErr == nil {
		closeErr = err
	}

	d.mu.Lock()
	if d.lockFile != nil {
		if err := syscall.Flock(int(d.lockFile.Fd()), syscall.LOCK_UN); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("unlock daemon: %w", err)
		}
		if err := d.lockFile.Close(); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("close daemon lock: %w", err)
		}
		d.lockFile = nil
	}
	if d.logFile != nil {
		if err := d.logFile.Close(); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("close daemon log: %w", err)
		}
		d.logFile = nil
		d.logger = nil
	}
	d.started = false
	d.closing = false
	d.cancel = nil
	d.ctx = nil
	d.hosts = make(map[string]*hostRuntime)
	d.mu.Unlock()
	return closeErr
}

// Status forwards a status event and lazily removes only a row that agterm
// explicitly says no longer exists. Tree is intentionally not consulted for
// deletion because its result can be limited to the frontmost window.
func (d *Daemon) Status(ctx context.Context, target string, args agterm.StatusArgs) error {
	if d == nil || d.sink == nil {
		return errors.New("daemon status sink is unavailable")
	}
	err := d.sink.Status(ctx, target, args)
	if isConnectionRefused(err) {
		d.beginAgtermRecovery()
	}
	if errors.Is(err, agterm.ErrUnknownTarget) {
		if unbindErr := d.store.UnbindRow(target); unbindErr != nil {
			d.logf("warning: unbind unknown agterm row %q: %v", target, unbindErr)
		}
	}
	return err
}

func (d *Daemon) acquireLock() error {
	if err := os.MkdirAll(d.dirs.Cache, 0o700); err != nil {
		return fmt.Errorf("create daemon cache: %w", err)
	}
	file, err := os.OpenFile(d.dirs.Lock(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return errors.New("daemon is already running")
		}
		return fmt.Errorf("lock daemon: %w", err)
	}
	d.lockFile = file
	return nil
}

func (d *Daemon) writePID() error {
	data := []byte(strconv.Itoa(os.Getpid()) + "\n")
	if err := os.WriteFile(d.dirs.Pid(), data, 0o600); err != nil {
		return fmt.Errorf("write daemon pidfile: %w", err)
	}
	return nil
}

func (d *Daemon) openLog() error {
	if d.logger != nil {
		return nil
	}
	file, err := os.OpenFile(d.dirs.Log(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	d.logFile = file
	d.logger = log.New(file, "", log.LstdFlags)
	return nil
}

func (d *Daemon) initialize(ctx context.Context) error {
	sock := agterm.SocketPath()
	ctl := agterm.CtlPath()
	d.logf("agterm control socket=%q agtermctl=%q", sock, ctl)
	d.handshake(ctx)
	if err := d.startControl(ctx); err != nil {
		return err
	}

	bound, err := d.store.Load()
	if err != nil {
		return err
	}
	// Tree is an informational startup check only. It is deliberately never
	// passed to Store.Reconcile: agterm's tree can be window-scoped.
	if d.rows != nil {
		if live, treeErr := d.rows.Tree(ctx); treeErr != nil {
			d.logf("warning: list agterm rows during startup: %v", treeErr)
		} else {
			d.logf("agterm startup tree returned %d rows; bindings remain lazy", len(live))
		}
	}

	hosts := make(map[string]struct{}, len(bound))
	for _, binding := range bound {
		hosts[binding.Host] = struct{}{}
	}
	for host := range hosts {
		if err := d.startHost(ctx, host); err != nil {
			return err
		}
	}
	d.startEvents(ctx)
	return nil
}

func (d *Daemon) handshake(ctx context.Context) bool {
	v, ok := d.sink.(versioner)
	if !ok {
		return true
	}
	version, err := v.Version(ctx)
	if err != nil {
		d.logf("warning: agterm version handshake: %v", err)
		return false
	}
	if olderVersion(version, agterm.MinTestedVersion) {
		d.logf("warning: agterm %s is older than tested minimum %s", version, agterm.MinTestedVersion)
		return true
	}
	d.logf("agterm version: %s", version)
	return true
}

func olderVersion(got, minimum string) bool {
	gotParts, gotOK := parseVersion(got)
	minParts, minOK := parseVersion(minimum)
	if !gotOK || !minOK {
		return false
	}
	for i := range minParts {
		if gotParts[i] != minParts[i] {
			return gotParts[i] < minParts[i]
		}
	}
	return false
}

func parseVersion(value string) ([3]int, bool) {
	var result [3]int
	value = strings.TrimPrefix(value, "v")
	parts := strings.SplitN(value, ".", 4)
	if len(parts) < 3 {
		return result, false
	}
	for i := range result {
		part := parts[i]
		if i == 2 {
			part = strings.SplitN(part, "-", 2)[0]
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return result, false
		}
		result[i] = n
	}
	return result, true
}

func (d *Daemon) startHost(ctx context.Context, host string) error {
	if !token.ValidHost(host) {
		return fmt.Errorf("invalid host %q", host)
	}
	d.mu.Lock()
	if !d.started || d.closing {
		d.mu.Unlock()
		return errors.New("daemon is stopping")
	}
	if _, exists := d.hosts[host]; exists {
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()
	if d.remote == nil {
		return errors.New("remote dependency is unavailable")
	}
	if d.supervisors == nil {
		return errors.New("supervisor factory is unavailable")
	}
	hostKey := token.FileKey(host)
	d.ensureMu.Lock()
	ensured := d.ensured[host]
	if !ensured {
		if err := d.remote.EnsureDirs(ctx, host); err != nil {
			d.ensureMu.Unlock()
			return fmt.Errorf("ensure remote agr directories for %s: %w", host, err)
		}
		d.ensured[host] = true
	}
	d.ensureMu.Unlock()
	home, err := d.remote.Home(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve remote home for %s: %w", host, err)
	}
	if home == "" {
		return fmt.Errorf("resolve remote home for %s: empty home", host)
	}

	localSock := d.dirs.Recv(hostKey)
	remoteSock := filepath.Join(home, ".cache", "agr", "bridge.sock")
	supervisor := d.supervisors.New(host, hostKey, remoteSock, localSock)
	if supervisor == nil {
		return fmt.Errorf("create supervisor for %s: nil supervisor", host)
	}
	var liveness receiver.Liveness
	if marker, ok := supervisor.(aliveMarker); ok {
		liveness = marker
	}
	listener := receiver.NewListener(host, hostKey, d.dirs, d, d.store, liveness)
	hostCtx, cancel := newHostContext(ctx)
	stateSource, hasStateSource := supervisor.(interface{ Changes() <-chan bridge.State })
	runtime := &hostRuntime{
		host: host, hostKey: hostKey, localSock: localSock,
		listener: listener, cancel: cancel, supervisor: supervisor,
		state: "down", since: time.Now().UTC(), attempts: 1,
	}

	d.mu.Lock()
	if !d.started || d.closing {
		d.mu.Unlock()
		cancel()
		supervisor.Stop()
		return errors.New("daemon is stopping")
	}
	if _, exists := d.hosts[host]; exists {
		d.mu.Unlock()
		cancel()
		supervisor.Stop()
		return nil
	}
	d.hosts[host] = runtime
	d.children.Add(2)
	if hasStateSource {
		d.children.Add(1)
	}
	d.mu.Unlock()

	go func() {
		defer d.children.Done()
		if err := listener.Serve(hostCtx); err != nil && hostCtx.Err() == nil {
			d.logf("warning: receiver for %s stopped: %v", host, err)
		}
	}()
	go func() {
		defer d.children.Done()
		if err := supervisor.Run(hostCtx); err != nil && hostCtx.Err() == nil {
			d.logf("warning: supervisor for %s stopped: %v", host, err)
		}
	}()
	if hasStateSource {
		go func() {
			defer d.children.Done()
			d.watchHostStates(hostCtx, host, stateSource.Changes())
		}()
	}
	return nil
}

func (d *Daemon) stopHost(host string) {
	d.mu.Lock()
	runtime, ok := d.hosts[host]
	if ok {
		delete(d.hosts, host)
	}
	d.mu.Unlock()
	if !ok {
		_ = os.Remove(d.dirs.Recv(token.FileKey(host)))
		return
	}
	if runtime.cancel != nil {
		runtime.cancel()
	}
	if runtime.supervisor != nil {
		runtime.supervisor.Stop()
	}
	_ = os.Remove(runtime.localSock)
}

func isConnectionRefused(err error) bool {
	return err != nil && (errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(strings.ToLower(err.Error()), "connection refused"))
}

func newHostContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}

func (d *Daemon) logf(format string, args ...any) {
	if d != nil && d.logger != nil {
		d.logger.Printf(format, args...)
	}
}

var _ StatusSink = (*Daemon)(nil)
var _ receiver.Liveness = (*Daemon)(nil)

// MarkAlive is present for callers that use a daemon as a liveness sink. The
// host-specific listener normally calls its injected bridge marker directly.
func (d *Daemon) MarkAlive() {}
