package main

import (
	"bytes"
	"testing"
)

func TestRunVersion(t *testing.T) {
	t.Helper()

	previous := version
	version = "test-version"
	t.Cleanup(func() { version = previous })

	var out, errw bytes.Buffer
	if got := run([]string{"--version"}, &out, &errw); got != 0 {
		t.Fatalf("run() exit code = %d, want 0", got)
	}
	if got, want := out.String(), "test-version\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := errw.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestRunHelp(t *testing.T) {
	t.Helper()

	for _, arg := range []string{"--help", "-h"} {
		var out, errw bytes.Buffer
		if got := run([]string{arg}, &out, &errw); got != 0 {
			t.Fatalf("run(%q) exit code = %d, want 0", arg, got)
		}
		if got, want := out.String(), help+"\n"; got != want {
			t.Errorf("run(%q) stdout = %q, want %q", arg, got, want)
		}
		if got := errw.String(); got != "" {
			t.Errorf("run(%q) stderr = %q, want empty", arg, got)
		}
	}
}

func TestRunUnknownCommand(t *testing.T) {
	t.Helper()

	var out, errw bytes.Buffer
	if got := run([]string{"unknown"}, &out, &errw); got != 2 {
		t.Fatalf("run() exit code = %d, want 2", got)
	}
	if got := out.String(); got != "" {
		t.Fatalf("stdout = %q, want empty", got)
	}
	if got, want := errw.String(), usage+"\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestRunWithoutArguments(t *testing.T) {
	t.Helper()

	var out, errw bytes.Buffer
	if got := run(nil, &out, &errw); got != 0 {
		t.Fatalf("run() exit code = %d, want 0", got)
	}
	if got, want := out.String(), usage+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := errw.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}
