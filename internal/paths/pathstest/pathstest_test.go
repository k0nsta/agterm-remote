package pathstest

import (
	"path/filepath"
	"testing"
)

func TestDirsUsesShortCleanedRoot(t *testing.T) {
	t.Helper()
	dirs := Dirs(t)
	if dirs.Cache == "" || filepath.Dir(dirs.Cache) != "/tmp" {
		t.Fatalf("Dirs().Cache = %q, want a direct child of /tmp", dirs.Cache)
	}
}
