// Package agtermtest provides the shared short-path fake agterm server used
// by unit tests in packages that speak the control socket.
package agtermtest

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/agterm"
)

// NewFakeAgterm starts a fake agterm control socket and returns its path.
func NewFakeAgterm(t *testing.T, handler func(agterm.Request) agterm.Response) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "agr-agterm-")
	if err != nil {
		t.Fatalf("create fake agterm directory: %v", err)
	}
	sock := filepath.Join(dir, "agterm.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("listen on fake agterm socket: %v", err)
	}

	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.RemoveAll(dir)
	})
	go acceptConnections(listener, handler)
	return sock
}

func acceptConnections(listener net.Listener, handler func(agterm.Request) agterm.Response) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go serveConnection(conn, handler)
	}
}

func serveConnection(conn net.Conn, handler func(agterm.Request) agterm.Response) {
	defer func() { _ = conn.Close() }()
	requestLine, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	var request agterm.Request
	if err := json.Unmarshal(requestLine, &request); err != nil {
		return
	}
	response := agterm.Response{}
	if handler != nil {
		response = handler(request)
	}
	_ = json.NewEncoder(conn).Encode(response)
}
