package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingIsZero(t *testing.T) {
	t.Helper()
	got, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || got != (Settings{}) {
		t.Fatalf("Load() = %+v, %v, want zero settings", got, err)
	}
}

func TestSaveThenLoad(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agr", "config.json")
	if err := Save(path, Settings{PageSize: 70}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(path)
	if err != nil || got.PageSize != 70 {
		t.Fatalf("Load() = %+v, %v, want PageSize 70", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v, want 0600", info.Mode().Perm(), err)
	}
}

func TestLoadRejectsBrokenJSON(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want a parse error")
	}
}

func TestPathFollowsXDG(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
	if got := Path(); got != "/x/cfg/agr/config.json" {
		t.Fatalf("Path() = %q", got)
	}
}
