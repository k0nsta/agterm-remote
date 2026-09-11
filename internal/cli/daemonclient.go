package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/k0nsta/agterm-remote/internal/daemon"
	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/token"
)

const (
	defaultDaemonWait = 3 * time.Second
	maxDaemonLine     = 1 << 20
)

type daemonRequest struct {
	Op   string `json:"op"`
	Host string `json:"host,omitempty"`
}

type daemonResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// DaemonClient speaks the local agr daemon's one-request-per-connection
// control protocol. It starts at most one detached daemon process per client,
// and only for up and reload-bindings when the control socket cannot be
// dialled (a stale socket node does not block the start); Down and Status
// never start one and return ErrDaemonNotRunning instead.
type DaemonClient struct {
	dirs paths.Dirs

	command string
	wait    time.Duration
	start   func() error

	startMu sync.Mutex
	started bool
}

// NewDaemonClient constructs a daemon client rooted at dirs. An empty dirs
// value uses the normal XDG cache location.
func NewDaemonClient(dirs paths.Dirs) *DaemonClient {
	if dirs.Cache == "" {
		dirs = paths.New()
	}
	client := &DaemonClient{dirs: dirs, wait: defaultDaemonWait}
	client.start = client.startDetached
	return client
}

// ErrDaemonNotRunning reports that no daemon was reachable for an operation
// that deliberately does not start one.
var ErrDaemonNotRunning = errors.New("daemon is not running")

// Up asks the daemon to start or retain the bridge for host, starting a
// daemon first when none is reachable.
func (c *DaemonClient) Up(ctx context.Context, host string) error {
	_, err := c.call(ctx, "up", host)
	return err
}

// Down asks the daemon to stop the bridge for host.
func (c *DaemonClient) Down(ctx context.Context, host string) error {
	_, err := c.call(ctx, "down", host)
	return err
}

// Status returns one status for host, or all running host statuses when host
// is empty.
func (c *DaemonClient) Status(ctx context.Context, host string) ([]daemon.HostStatus, error) {
	result, err := c.call(ctx, "status", host)
	if err != nil {
		return nil, err
	}
	if host != "" {
		var status daemon.HostStatus
		if err := json.Unmarshal(result, &status); err != nil {
			return nil, fmt.Errorf("decode daemon status: %w", err)
		}
		return []daemon.HostStatus{status}, nil
	}
	var statuses []daemon.HostStatus
	if len(result) == 0 || string(result) == "null" {
		return []daemon.HostStatus{}, nil
	}
	if err := json.Unmarshal(result, &statuses); err != nil {
		return nil, fmt.Errorf("decode daemon statuses: %w", err)
	}
	return statuses, nil
}

// ReloadBindings tells the daemon to reload its persistent binding set.
func (c *DaemonClient) ReloadBindings(ctx context.Context) error {
	_, err := c.call(ctx, "reload-bindings", "")
	return err
}

// autoStartOps are the operations that may spawn a daemon. Everything else is
// observational or a teardown: `agr doctor` must stay read-only, and
// `agr down` starting the daemon it was asked to stop is the opposite of the
// request. Those report a not-running daemon instead.
var autoStartOps = map[string]bool{"up": true, "reload-bindings": true}

func (c *DaemonClient) call(ctx context.Context, op, host string) (json.RawMessage, error) {
	if c == nil {
		return nil, errors.New("nil daemon client")
	}
	if ctx == nil {
		return nil, errors.New("nil daemon context")
	}
	if host != "" && !token.ValidHost(host) {
		return nil, fmt.Errorf("invalid remote host %q", host)
	}
	conn, err := c.open(ctx, autoStartOps[op])
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	data, err := json.Marshal(daemonRequest{Op: op, Host: host})
	if err != nil {
		return nil, fmt.Errorf("encode daemon request: %w", err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("write daemon request: %w", err)
	}
	line, err := readDaemonLine(bufio.NewReader(conn))
	if err != nil {
		return nil, fmt.Errorf("read daemon response: %w", err)
	}
	var response daemonResponse
	if err := json.Unmarshal(line, &response); err != nil {
		return nil, fmt.Errorf("decode daemon response: %w", err)
	}
	if !response.OK {
		if response.Error == "" {
			response.Error = "daemon operation failed"
		}
		return nil, errors.New(response.Error)
	}
	return response.Result, nil
}

func (c *DaemonClient) open(ctx context.Context, autoStart bool) (net.Conn, error) {
	conn, err := dialDaemon(ctx, c.dirs.Sock())
	if err == nil {
		return conn, nil
	}
	if !autoStart {
		if daemonAbsent(err) {
			return nil, fmt.Errorf("%w: %v", ErrDaemonNotRunning, err)
		}
		// Anything else says nothing about whether a daemon is running — a
		// cancelled context, a permission error, a slow accept — and callers
		// that treat "not running" as a state (down, doctor) must not read it
		// as one.
		return nil, fmt.Errorf("dial daemon: %w", err)
	}
	// A failed dial is not evidence that a daemon is running: after SIGKILL,
	// a panic or power loss the socket node survives with nothing listening,
	// and refusing to start here left every command failing until it was
	// removed by hand. Start regardless — the daemon's own flock decides who
	// wins, and ListenClean reclaims the stale node.
	if err := c.startOnce(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(c.wait)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	lastErr := err
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("daemon socket did not appear within %s: %w", c.wait, lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
		conn, lastErr = dialDaemon(ctx, c.dirs.Sock())
		if lastErr == nil {
			return conn, nil
		}
	}
}

func (c *DaemonClient) startOnce() error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if c.started {
		return nil
	}
	c.started = true
	if c.start == nil {
		return errors.New("daemon starter is unavailable")
	}
	if err := c.start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	return nil
}

func (c *DaemonClient) startDetached() error {
	command := c.command
	if command == "" {
		var err error
		command, err = os.Executable()
		if err != nil {
			return fmt.Errorf("resolve agr executable: %w", err)
		}
	}
	process := exec.Command(command, "daemon")
	process.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open daemon stdio: %w", err)
	}
	process.Stdin = devNull
	process.Stdout = devNull
	process.Stderr = devNull
	if err := process.Start(); err != nil {
		_ = devNull.Close()
		return err
	}
	_ = devNull.Close()
	return nil
}

// daemonAbsent reports whether a dial failure means no daemon is listening:
// the socket node is missing, or present with nothing behind it (the stale
// node a killed daemon leaves).
func daemonAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}

func dialDaemon(ctx context.Context, path string) (net.Conn, error) {
	dialer := net.Dialer{}
	return dialer.DialContext(ctx, "unix", path)
}

func readDaemonLine(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadBytes('\n')
	if len(line) > maxDaemonLine {
		return nil, errors.New("daemon response exceeds 1 MiB")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, errors.New("empty daemon response")
	}
	return line, nil
}
