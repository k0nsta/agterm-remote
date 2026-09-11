package unixsock

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ListenClean opens a Unix socket, removing a path left behind by a crashed
// process. A successful probe means another process owns the socket and the
// path is preserved.
//
// The listener returned never unlinks its path — not on Close either, which is
// Go's default for Unix listeners. Only the caller knows whether the path still
// belongs to this listener or to a successor that bound it after this one was
// closed, so the caller removes it. Stale paths left by a crash are reclaimed
// by the next ListenClean.
func ListenClean(path string) (net.Listener, error) {
	if path == "" {
		return nil, errors.New("empty Unix socket path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}

	if _, err := os.Stat(path); err == nil {
		conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("socket already in use: %s", path)
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale socket: %w", removeErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect socket: %w", err)
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on socket: %w", err)
	}
	if unixListener, ok := listener.(*net.UnixListener); ok {
		unixListener.SetUnlinkOnClose(false)
	}
	return listener, nil
}
