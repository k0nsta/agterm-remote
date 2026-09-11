// Package pathstest holds the shared test fixture for paths.Dirs. It lives
// outside internal/paths so the shipped binary does not link the testing
// package for a symbol it never calls.
package pathstest

import (
	"os"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/paths"
)

// Dirs returns a short-lived directory suitable for tests that create
// Unix sockets. It deliberately uses /tmp instead of t.TempDir: macOS limits
// Unix socket paths to 104 bytes, and the testing package's temp path can
// already consume most of that budget.
func Dirs(t *testing.T) paths.Dirs {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "agr-")
	if err != nil {
		t.Fatalf("create test directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(root)
	})
	return paths.Dirs{Cache: root}
}
