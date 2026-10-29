package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/settings"
	"github.com/k0nsta/agterm-remote/internal/skill"
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
	for _, argv := range [][]string{
		{"setup", "extra"},
		{"setup", "--end-key"},
		{"setup", "--end-key", ""},
		{"setup", "--paste-key"},
		{"setup", "--paste-key", ""},
		{"setup", "--key", "x"},
		{"setup", "--end-key", "x", "--end-key", "y"},
		{"setup", "--paste-key", "x", "--paste-key", "y"},
		{"setup", "--end-key", "--paste-key", "x"},
		{"setup", "--page-size", "0"},
		{"setup", "--page-size", "101"},
		{"setup", "--page-size", "big"},
		{"setup", "--page-size", "50", "--page-size", "60"},
		{"setup", "--new-key"},
		{"setup", "--new-key", "x", "--new-key", "y"},
	} {
		var out, errout strings.Builder
		if got := runWithApplication(argv, &out, &errout, &application{}); got != 2 {
			t.Fatalf("run(%q) exit = %d, want 2", argv, got)
		}
	}
}

func TestRunSetupAcceptsTheKeyFlagsInAnyOrder(t *testing.T) {
	t.Helper()
	for _, args := range [][]string{
		{"setup", "--end-key", "ctrl+a>x", "--paste-key", "ctrl+a>v", "--new-key", "ctrl+a>n"},
		{"setup", "--new-key", "ctrl+a>n", "--paste-key", "ctrl+a>v", "--end-key", "ctrl+a>x"},
	} {
		t.Run(strings.Join(args[1:], "/"), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			var out, errout strings.Builder
			if got := runWithApplication(args, &out, &errout, &application{}); got != 0 {
				t.Fatalf("run(%q) exit = %d, stderr = %q", args, got, errout.String())
			}
			keymap, err := os.ReadFile(filepath.Join(home, ".config", "agterm", "keymap.conf"))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{`"End remote session" ctrl+a>x`, `"Paste to remote" ctrl+a>v`, `"New remote session" ctrl+a>n`} {
				if !strings.Contains(string(keymap), want) {
					t.Fatalf("keymap.conf = %q, want %s", keymap, want)
				}
			}
		})
	}
}

func TestRunSetupSavesThePageSize(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	for _, tt := range []struct {
		size, want string
		stored     int
	}{
		{size: "70", want: "host pages: 70%", stored: 70},
		{size: "100", want: "host pages: full", stored: 0},
	} {
		var out, errout strings.Builder
		if got := runWithApplication([]string{"setup", "--page-size", tt.size}, &out, &errout, &application{}); got != 0 {
			t.Fatalf("setup --page-size %s exit = %d, stderr = %q", tt.size, got, errout.String())
		}
		if !strings.Contains(out.String(), tt.want) {
			t.Fatalf("setup --page-size %s said %q, want %q", tt.size, out.String(), tt.want)
		}
		saved, err := settings.Load(settings.Path())
		if err != nil || saved.PageSize != tt.stored {
			t.Fatalf("saved = %+v, %v, want PageSize %d", saved, err, tt.stored)
		}
	}
}

func TestRunPasteUsesRowFromAgtermEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("AGT_SESSION_ID", "row-from-keymap")
	t.Setenv("AGTERM_SESSION_ID", "")
	var out, errout strings.Builder
	if got := runWithApplication([]string{"paste"}, &out, &errout, &application{}); got != 1 {
		t.Fatalf("run(paste) exit = %d, want 1 after using the environment row", got)
	}
	if strings.Contains(errout.String(), "no agterm row in the environment") {
		t.Fatalf("run(paste) ignored AGT_SESSION_ID: %q", errout.String())
	}
}

func TestRunPasteRequiresARowWhenNoArgumentOrEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("AGT_SESSION_ID", "")
	t.Setenv("AGTERM_SESSION_ID", "")
	var out, errout strings.Builder
	if got := runWithApplication([]string{"paste"}, &out, &errout, &application{}); got != 2 {
		t.Fatalf("run(paste) exit = %d, want usage error", got)
	}
	if !strings.Contains(errout.String(), "no agterm row in the environment") {
		t.Fatalf("run(paste) stderr = %q, want missing-row usage", errout.String())
	}
}

func TestRunSkillPrintsAndRejectsArguments(t *testing.T) {
	t.Helper()
	var out, errw strings.Builder
	if got := runWithApplication([]string{"skill"}, &out, &errw, &application{}); got != 0 {
		t.Fatalf("run(skill) exit = %d, stderr = %q", got, errw.String())
	}
	if out.String() != string(skill.Bytes(version)) {
		t.Fatal("agr skill output differs from the embedded skill")
	}
	out.Reset()
	if got := runWithApplication([]string{"skill", "x"}, &out, &errw, &application{}); got != 2 {
		t.Fatalf("run(skill x) exit = %d, want 2", got)
	}
}

func TestRunNewRejectsExtraArguments(t *testing.T) {
	t.Helper()
	var out, errout strings.Builder
	if got := runWithApplication([]string{"new", "row-1", "x"}, &out, &errout, &application{}); got != 2 {
		t.Fatalf("run(new row-1 x) exit = %d, want 2", got)
	}
	if !strings.Contains(errout.String(), "usage: agr new [row]") {
		t.Fatalf("stderr = %q, want new usage", errout.String())
	}
}
