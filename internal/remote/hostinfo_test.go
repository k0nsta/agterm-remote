package remote

import (
	"reflect"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
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
