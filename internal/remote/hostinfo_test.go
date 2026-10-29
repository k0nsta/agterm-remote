package remote

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
	"github.com/k0nsta/agterm-remote/internal/token"
)

func TestHostInfoRoundTrip(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	want := HostInfo{
		Home:        "/Users/remote",
		Mux:         "zmx",
		Relay:       "nc",
		Mosh:        true,
		AgrVersion:  "1.0.0",
		ZmxVersion:  "0.7.1",
		TmuxVersion: "3.4",
		ZmxLabels:   true,
		ProbedAt:    time.Date(2026, 9, 4, 12, 30, 0, 0, time.UTC),
	}

	if err := SaveHostInfo(dirs, "user@example.com", want); err != nil {
		t.Fatalf("SaveHostInfo() error = %v", err)
	}
	got, err := LoadHostInfo(dirs, "user@example.com")
	if err != nil {
		t.Fatalf("LoadHostInfo() error = %v", err)
	}
	want.Host = "user@example.com"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadHostInfo() = %#v, want %#v", got, want)
	}
}

func TestHostInfoUsesFileKeyAndMissingIsDistinct(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	if _, err := LoadHostInfo(dirs, "user@example.com"); err == nil {
		t.Fatal("LoadHostInfo() error = nil, want missing-file error")
	}

	if err := SaveHostInfo(dirs, "user@example.com", HostInfo{Home: "/home/user"}); err != nil {
		t.Fatalf("SaveHostInfo() error = %v", err)
	}
	if _, err := LoadHostInfo(dirs, "user@example.com"); err != nil {
		t.Fatalf("LoadHostInfo() after save error = %v", err)
	}
}

func TestCachedHosts(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	if hosts, err := CachedHosts(dirs); err != nil || len(hosts) != 0 {
		t.Fatalf("CachedHosts() with no cache = %v, %v; want none", hosts, err)
	}
	for _, host := range []string{"user@example.com", "myhost"} {
		if err := SaveHostInfo(dirs, host, HostInfo{}); err != nil {
			t.Fatalf("SaveHostInfo(%q) error = %v", host, err)
		}
	}
	// Files written before HostInfo.Host: a token-alphabet host is read back
	// from the file name, a lossy one is not, and a stray file is ignored.
	for host, body := range map[string]string{"old-box": `{"home":"/h"}`, "admin@old": `{}`} {
		if err := os.WriteFile(dirs.HostInfo(token.FileKey(host)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dirs.HostInfo("")), "notes-abc.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hosts, err := CachedHosts(dirs)
	if err != nil {
		t.Fatalf("CachedHosts() error = %v", err)
	}
	if want := []string{"myhost", "old-box", "user@example.com"}; !reflect.DeepEqual(hosts, want) {
		t.Fatalf("CachedHosts() = %v, want %v", hosts, want)
	}
}
