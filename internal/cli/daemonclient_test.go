package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/daemon"
	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
)

func TestDaemonClientControlRoundTripAndStatus(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	listener, err := net.Listen("unix", dirs.Sock())
	if err != nil {
		t.Fatalf("listen daemon socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	var mu sync.Mutex
	var requests []daemonRequest
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			func() {
				defer func() { _ = conn.Close() }()
				line, readErr := bufio.NewReader(conn).ReadBytes('\n')
				if readErr != nil {
					return
				}
				var request daemonRequest
				if json.Unmarshal(line, &request) != nil {
					return
				}
				mu.Lock()
				requests = append(requests, request)
				mu.Unlock()
				result := any(map[string]any{})
				if request.Op == "status" {
					result = []daemon.HostStatus{{Host: "home", State: "up", Attempts: 2}}
				}
				response, _ := json.Marshal(daemonResponse{OK: true, Result: mustJSON(t, result)})
				_, _ = conn.Write(append(response, '\n'))
			}()
		}
	}()

	client := NewDaemonClient(dirs)
	ctx := context.Background()
	if err := client.Up(ctx, "home"); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if err := client.Down(ctx, "home"); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	if err := client.ReloadBindings(ctx); err != nil {
		t.Fatalf("ReloadBindings() error = %v", err)
	}
	statuses, err := client.Status(ctx, "")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(statuses) != 1 || statuses[0].Host != "home" || statuses[0].Attempts != 2 {
		t.Fatalf("Status() = %#v, want one home status", statuses)
	}

	mu.Lock()
	got := append([]daemonRequest(nil), requests...)
	mu.Unlock()
	want := []daemonRequest{
		{Op: "up", Host: "home"},
		{Op: "down", Host: "home"},
		{Op: "reload-bindings"},
		{Op: "status"},
	}
	if len(got) != len(want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %#v, want %#v", i, got[i], want[i])
		}
	}
	_ = listener.Close()
	<-done
}

func TestDaemonClientStartsOnceAndGivesUpWhenSocketNeverAppears(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	client := NewDaemonClient(dirs)
	client.wait = 40 * time.Millisecond
	starts := 0
	client.start = func() error {
		starts++
		return nil
	}
	ctx := context.Background()
	if err := client.Up(ctx, "home"); err == nil || !strings.Contains(err.Error(), "socket did not appear") {
		t.Fatalf("Up() error = %v, want bounded missing-socket error", err)
	}
	// A second auto-starting operation must reuse the one detached start.
	if err := client.ReloadBindings(ctx); err == nil || !strings.Contains(err.Error(), "socket did not appear") {
		t.Fatalf("ReloadBindings() error = %v, want bounded missing-socket error", err)
	}
	if starts != 1 {
		t.Fatalf("daemon starts = %d, want one detached start", starts)
	}
}

// TestDaemonClientDoesNotStartDaemonForReadOnlyOrStopOps pins the contract that
// only `up` and `reload-bindings` may spawn a daemon: `agr doctor` has to stay
// read-only, and `agr down` starting the daemon it was asked to stop is the
// opposite of the request.
func TestDaemonClientDoesNotStartDaemonForReadOnlyOrStopOps(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name string
		call func(*DaemonClient, context.Context) error
	}{
		{"down", func(c *DaemonClient, ctx context.Context) error { return c.Down(ctx, "home") }},
		{"status", func(c *DaemonClient, ctx context.Context) error {
			_, err := c.Status(ctx, "home")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewDaemonClient(pathstest.Dirs(t))
			client.wait = 40 * time.Millisecond
			started := false
			client.start = func() error {
				started = true
				return nil
			}
			err := tc.call(client, context.Background())
			if !errors.Is(err, ErrDaemonNotRunning) {
				t.Fatalf("%s error = %v, want ErrDaemonNotRunning", tc.name, err)
			}
			if started {
				t.Fatalf("%s started a daemon; only up and reload-bindings may", tc.name)
			}
		})
	}
}

func TestDaemonClientRejectsInvalidHostWithoutStarting(t *testing.T) {
	t.Helper()
	client := NewDaemonClient(pathstest.Dirs(t))
	started := false
	client.start = func() error {
		started = true
		return nil
	}
	if err := client.Up(context.Background(), "-oProxyCommand=bad"); err == nil || !strings.Contains(err.Error(), "invalid remote host") {
		t.Fatalf("Up() error = %v, want invalid host", err)
	}
	if started {
		t.Fatal("invalid host started the daemon")
	}
}

func TestReadDaemonLineRejectsEmptyResponse(t *testing.T) {
	t.Helper()
	if _, err := readDaemonLine(bufio.NewReader(strings.NewReader("\n"))); err == nil {
		t.Fatal("readDaemonLine() error = nil, want empty response error")
	}
	if _, err := readDaemonLine(bufio.NewReader(strings.NewReader("ok"))); err != nil {
		t.Fatalf("readDaemonLine() without newline error = %v", err)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test JSON: %v", err)
	}
	return data
}

// TestDaemonClientStartsDaemonDespiteStaleSocketNode pins the removal of the
// socket-absent gate: after SIGKILL or power loss the socket node survives
// with nothing listening, and `up` must still start a daemon rather than
// failing until the node is removed by hand.
func TestDaemonClientStartsDaemonDespiteStaleSocketNode(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	stale, err := net.Listen("unix", dirs.Sock())
	if err != nil {
		t.Fatalf("create stale socket node: %v", err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = stale.Close()
	if _, err := os.Stat(dirs.Sock()); err != nil {
		t.Fatalf("stale socket node missing: %v", err)
	}

	client := NewDaemonClient(dirs)
	client.wait = 40 * time.Millisecond
	starts := 0
	client.start = func() error {
		starts++
		return nil
	}
	if err := client.Up(context.Background(), "home"); err == nil || !strings.Contains(err.Error(), "socket did not appear") {
		t.Fatalf("Up() error = %v, want bounded missing-socket error", err)
	}
	if starts != 1 {
		t.Fatalf("daemon starts with a stale socket node = %d, want 1", starts)
	}
}

// TestDaemonClientNotRunningOnlyForAbsentDaemon pins the classification behind
// `agr down` exiting 0: a missing socket node or a stale one with nothing
// listening is "not running"; a dial that fails for any other reason — here a
// cancelled context — is not, or down would report success while a daemon and
// its bridge are still up.
func TestDaemonClientNotRunningOnlyForAbsentDaemon(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	client := NewDaemonClient(dirs)
	client.start = func() error { t.Fatal("down must not start a daemon"); return nil }

	stale, err := net.Listen("unix", dirs.Sock())
	if err != nil {
		t.Fatalf("create stale socket node: %v", err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = stale.Close()
	if err := client.Down(context.Background(), "home"); !errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("Down() against a stale socket node = %v, want ErrDaemonNotRunning", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	err = client.Down(cancelled, "home")
	if err == nil {
		t.Fatal("Down() with a cancelled context = nil, want an error")
	}
	if errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("Down() with a cancelled context = %v, must not read as not running", err)
	}
}
