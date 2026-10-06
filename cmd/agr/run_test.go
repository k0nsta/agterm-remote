package main

import (
	"bytes"
	"strings"
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

func TestParseOpenArgs(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		args       []string
		positional []string
		cwd        string
		parent     string
		ok         bool
	}{
		{args: []string{"host"}, positional: []string{"host"}, ok: true},
		{args: []string{"host", "a1-2", "--cwd", "/srv/x y", "--parent", "a1"}, positional: []string{"host", "a1-2"}, cwd: "/srv/x y", parent: "a1", ok: true},
		{args: []string{"--parent", "a1", "host", "a1-2"}, positional: []string{"host", "a1-2"}, parent: "a1", ok: true},
		{args: []string{"host", "--cwd"}},
		{args: []string{"host", "--cwd", "/a", "--cwd", "/b"}},
	} {
		positional, opts, ok := parseOpenArgs(tc.args)
		if ok != tc.ok || (ok && (len(positional) != len(tc.positional) || opts.Cwd != tc.cwd || opts.Parent != tc.parent)) {
			t.Errorf("parseOpenArgs(%q) = (%q, %+v, %v)", tc.args, positional, opts, ok)
			continue
		}
		for i := range positional {
			if positional[i] != tc.positional[i] {
				t.Errorf("parseOpenArgs(%q) positional = %q, want %q", tc.args, positional, tc.positional)
			}
		}
	}
}

func TestOnSplitRejectsArguments(t *testing.T) {
	t.Helper()
	var out, errw bytes.Buffer
	if got := runWithApplication([]string{"on-split", "x"}, &out, &errw, &application{}); got != 2 {
		t.Fatalf("run(on-split x) exit = %d, want 2", got)
	}
}

func TestRunSetupRejectsBadArguments(t *testing.T) {
	t.Helper()
	for _, argv := range [][]string{{"setup", "extra"}, {"setup", "--end-key"}, {"setup", "--end-key", ""}, {"setup", "--key", "x"}} {
		var out, errout strings.Builder
		if got := runWithApplication(argv, &out, &errout, &application{}); got != 2 {
			t.Fatalf("run(%q) exit = %d, want 2", argv, got)
		}
	}
}
