package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/daemon"
	"github.com/k0nsta/agterm-remote/internal/paths"
)

func TestDaemonClientControlRoundTripAndStatus(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
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
	dirs := paths.TestDirs(t)
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
	if err := client.Down(ctx, "home"); err == nil || !strings.Contains(err.Error(), "socket did not appear") {
		t.Fatalf("Down() error = %v, want bounded missing-socket error", err)
	}
	if starts != 1 {
		t.Fatalf("daemon starts = %d, want one detached start", starts)
	}
}

func TestDaemonClientRejectsInvalidHostWithoutStarting(t *testing.T) {
	t.Helper()
	client := NewDaemonClient(paths.TestDirs(t))
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
