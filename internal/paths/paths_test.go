package paths

import (
	"path/filepath"
	"testing"
)

func TestNewHonoursXDGCacheHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", "/tmp/agr-cache")
	t.Setenv("HOME", "/tmp/agr-home")

	if got, want := New().Cache, filepath.Join("/tmp/agr-cache", "agr"); got != want {
		t.Fatalf("New().Cache = %q, want %q", got, want)
	}
}

func TestNewFallsBackToHomeCache(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/tmp/agr-home")

	if got, want := New().Cache, filepath.Join("/tmp/agr-home", ".cache", "agr"); got != want {
		t.Fatalf("New().Cache = %q, want %q", got, want)
	}
}

func TestDirsPaths(t *testing.T) {
	t.Helper()
	dirs := Dirs{Cache: "/tmp/agr-cache"}
	tests := map[string]string{
		"socket":    dirs.Sock(),
		"lock":      dirs.Lock(),
		"pid":       dirs.Pid(),
		"log":       dirs.Log(),
		"bindings":  dirs.Bindings(),
		"recv":      dirs.Recv("user_h-abc123"),
		"bridge":    dirs.BridgeLog("user_h-abc123"),
		"host info": dirs.HostInfo("user_h-abc123"),
	}
	wants := map[string]string{
		"socket":    "/tmp/agr-cache/agr.sock",
		"lock":      "/tmp/agr-cache/agr.lock",
		"pid":       "/tmp/agr-cache/agr.pid",
		"log":       "/tmp/agr-cache/agr.log",
		"bindings":  "/tmp/agr-cache/bindings.json",
		"recv":      "/tmp/agr-cache/recv-user_h-abc123.sock",
		"bridge":    "/tmp/agr-cache/bridge-user_h-abc123.log",
		"host info": "/tmp/agr-cache/hosts/user_h-abc123.json",
	}
	for name, got := range tests {
		if want := wants[name]; got != want {
			t.Errorf("%s path = %q, want %q", name, got, want)
		}
	}
}

func TestTestDirsUsesShortCleanedRoot(t *testing.T) {
	t.Helper()
	dirs := TestDirs(t)
	if dirs.Cache == "" || filepath.Dir(dirs.Cache) != "/tmp" {
		t.Fatalf("TestDirs().Cache = %q, want a direct child of /tmp", dirs.Cache)
	}
}
