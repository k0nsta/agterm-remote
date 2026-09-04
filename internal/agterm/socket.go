package agterm

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

const bundledCtlPath = "/Applications/agterm.app/Contents/MacOS/agtermctl"

// SocketPath returns agterm's control socket according to its documented
// environment-variable precedence.
func SocketPath() string {
	if path := os.Getenv("AGTERM_CONTROL_SOCKET"); path != "" {
		return path
	}
	if path := os.Getenv("AGTERM_SOCKET"); path != "" {
		return path
	}
	if stateDir := os.Getenv("AGTERM_STATE_DIR"); stateDir != "" {
		return filepath.Join(stateDir, "agterm.sock")
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "agterm", "agterm.sock")
}

var ctlPathOnce struct {
	sync.Once
	path string
}

// CtlPath resolves agtermctl once. Launchd does not provide a useful PATH, so
// an executable in the application bundle wins over PATH lookup.
func CtlPath() string {
	ctlPathOnce.Do(func() {
		if info, err := os.Stat(bundledCtlPath); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			ctlPathOnce.path = bundledCtlPath
			return
		}
		ctlPathOnce.path, _ = exec.LookPath("agtermctl")
	})
	return ctlPathOnce.path
}
