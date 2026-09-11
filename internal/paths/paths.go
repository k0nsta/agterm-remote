// Package paths defines agr's local state and cache paths.
package paths

import (
	"os"
	"path/filepath"
)

// Dirs contains the root directory used by agr for runtime state and cache
// files. Keeping it injectable makes socket-using tests independent of the
// user's real agr state.
type Dirs struct {
	Cache string
}

// New returns the default agr directories. XDG_CACHE_HOME takes precedence
// over the conventional home-directory cache location.
func New() Dirs {
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			cacheHome = filepath.Join(home, ".cache")
		} else {
			cacheHome = ".cache"
		}
	}
	return Dirs{Cache: filepath.Join(cacheHome, "agr")}
}

// Sock is the daemon's control socket.
func (d Dirs) Sock() string { return filepath.Join(d.Cache, "agr.sock") }

// Lock is the daemon's single-instance lock.
func (d Dirs) Lock() string { return filepath.Join(d.Cache, "agr.lock") }

// Pid is the daemon's pid file.
func (d Dirs) Pid() string { return filepath.Join(d.Cache, "agr.pid") }

// Log is the daemon log file.
func (d Dirs) Log() string { return filepath.Join(d.Cache, "agr.log") }

// Bindings is the row-to-remote-session binding database.
func (d Dirs) Bindings() string { return filepath.Join(d.Cache, "bindings.json") }

// Recv is the local Unix socket receiving events for one host.
func (d Dirs) Recv(hostKey string) string {
	return filepath.Join(d.Cache, "recv-"+hostKey+".sock")
}

// BridgeLog is the SSH bridge log for one host.
func (d Dirs) BridgeLog(hostKey string) string {
	return filepath.Join(d.Cache, "bridge-"+hostKey+".log")
}

// HostInfo is the cached probe information for one host.
func (d Dirs) HostInfo(hostKey string) string {
	return filepath.Join(d.Cache, "hosts", hostKey+".json")
}
