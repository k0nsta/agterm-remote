package unixsock

import (
	"net"
	"os"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
)

func TestListenCleanRemovesLeftoverSocketPath(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	path := dirs.Sock()
	if err := os.WriteFile(path, []byte("left by a crashed daemon"), 0o600); err != nil {
		t.Fatalf("write leftover socket path: %v", err)
	}

	listener, err := ListenClean(path)
	if err != nil {
		t.Fatalf("ListenClean() error = %v, want stale path recovery", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(path)
	}()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket path after ListenClean(): %v", err)
	}
}

func TestListenCleanRefusesLiveSocket(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	path := dirs.Sock()
	first, err := ListenClean(path)
	if err != nil {
		t.Fatalf("first ListenClean() error = %v", err)
	}
	defer func() { _ = first.Close() }()

	second, err := ListenClean(path)
	if err == nil {
		_ = second.Close()
		t.Fatal("second ListenClean() error = nil, want live-socket refusal")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("live socket path was removed: %v", statErr)
	}

	// A real listener must remain usable after the rejected bind attempt.
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial original live socket: %v", err)
	}
	_ = conn.Close()
}
