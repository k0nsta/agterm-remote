package agterm_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/agterm"
)

func TestSocketPathPrecedence(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", "/tmp/agr-home")
	tests := []struct {
		name    string
		control string
		agterm  string
		state   string
		want    string
	}{
		{name: "control socket", control: "/run/control.sock", agterm: "/run/agterm.sock", state: "/run/state", want: "/run/control.sock"},
		{name: "agterm socket", agterm: "/run/agterm.sock", state: "/run/state", want: "/run/agterm.sock"},
		{name: "state directory", state: "/run/state", want: "/run/state/agterm.sock"},
		{name: "default", want: filepath.Join("/tmp/agr-home", "Library", "Application Support", "agterm", "agterm.sock")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			for _, key := range []string{"AGTERM_CONTROL_SOCKET", "AGTERM_SOCKET", "AGTERM_STATE_DIR"} {
				t.Setenv(key, "")
			}
			if tt.control != "" {
				t.Setenv("AGTERM_CONTROL_SOCKET", tt.control)
			}
			if tt.agterm != "" {
				t.Setenv("AGTERM_SOCKET", tt.agterm)
			}
			if tt.state != "" {
				t.Setenv("AGTERM_STATE_DIR", tt.state)
			}
			if got := agterm.SocketPath(); got != tt.want {
				t.Fatalf("SocketPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSocketPathUsesExplicitStateSocket(t *testing.T) {
	t.Helper()
	stateDir := t.TempDir()
	t.Setenv("AGTERM_CONTROL_SOCKET", "")
	t.Setenv("AGTERM_SOCKET", "")
	t.Setenv("AGTERM_STATE_DIR", stateDir)
	got := agterm.SocketPath()
	if want := filepath.Join(stateDir, "agterm.sock"); got != want {
		t.Fatalf("SocketPath() = %q, want %q", got, want)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("SocketPath() unexpectedly created %q", got)
	}
}
