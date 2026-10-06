package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testAgr = "'/opt/homebrew/bin/agr'"

func TestWireHooks(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty file",
			want: hooksNote + "\n" +
				"on pane.split     " + testAgr + " on-split\n" +
				"on pane.scratch   " + testAgr + " on-scratch\n",
		},
		{
			name: "README lines are repointed in place",
			in: "# my hooks\n" +
				"on status ~/bin/status.sh\n" +
				"on pane.split    $HOME/go/bin/agr on-split\n" +
				"\n" +
				"on pane.scratch  $HOME/go/bin/agr on-scratch   # remote shell\n",
			want: "# my hooks\n" +
				"on status ~/bin/status.sh\n" +
				"on pane.split    " + testAgr + " on-split\n" +
				"\n" +
				"on pane.scratch  " + testAgr + " on-scratch   # remote shell\n",
		},
		{
			name: "a missing line is appended after a blank",
			in:   "on pane.split '/old path/agr' on-split",
			want: "on pane.split " + testAgr + " on-split\n" +
				"\n" + hooksNote + "\n" +
				"on pane.scratch   " + testAgr + " on-scratch\n",
		},
		{
			name: "a repeat is dropped",
			in: "on pane.split agr on-split\n" +
				"on pane.split /usr/local/bin/agr on-split\n" +
				"on pane.scratch agr on-scratch\n",
			want: "on pane.split " + testAgr + " on-split\n" +
				"on pane.scratch " + testAgr + " on-scratch\n",
		},
		{
			name: "other commands are left alone",
			in: "on pane.split ~/bin/notify on-split\n" +
				"on pane.split agr on-scratch\n" +
				"on pane.split agr on-split --verbose\n",
			want: "on pane.split ~/bin/notify on-split\n" +
				"on pane.split agr on-scratch\n" +
				"on pane.split agr on-split --verbose\n" +
				"\n" + hooksNote + "\n" +
				"on pane.split     " + testAgr + " on-split\n" +
				"on pane.scratch   " + testAgr + " on-scratch\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := wireHooks(tc.in, testAgr)
			if got != tc.want {
				t.Fatalf("wireHooks() =\n%s\nwant\n%s", got, tc.want)
			}
			if again := wireHooks(got, testAgr); again != got {
				t.Fatalf("second wireHooks() changed the file:\n%s", again)
			}
		})
	}
}

func TestWireKeymap(t *testing.T) {
	t.Helper()
	end := testAgr + ` end "{AGT_SESSION_ID}"`
	for _, tc := range []struct {
		name string
		in   string
		key  string
		want string
	}{
		{
			name: "no command, no key: added without a chord",
			want: keymapNote + "\n" + `command "End remote session" --error-hud ` + end + "\n",
		},
		{
			name: "no command, a key",
			in:   "map cmd+k clear\n",
			key:  "ctrl+a>x",
			want: "map cmd+k clear\n\n" + keymapNote + "\n" + `command "End remote session" ctrl+a>x --error-hud ` + end + "\n",
		},
		{
			name: "an existing command keeps its name, chord and options",
			in:   `command "Finish" cmd+shift+e --error-hud $HOME/go/bin/agr end "{AGT_SESSION_ID}"` + "\n",
			want: `command "Finish" cmd+shift+e --error-hud ` + end + "\n",
		},
		{
			name: "a key rebinds an existing chord",
			in:   `command "Finish" cmd+shift+e agr end "{AGT_SESSION_ID}"` + "\n",
			key:  "ctrl+a>x",
			want: `command "Finish" ctrl+a>x ` + end + "\n",
		},
		{
			name: "a key is added to a command without one",
			in:   `command "End remote session" --error-hud agr end "{AGT_SESSION_ID}"` + "\n",
			key:  "ctrl+a>x",
			want: `command "End remote session" ctrl+a>x --error-hud ` + end + "\n",
		},
		{
			name: "other commands do not count",
			in:   `command "Agenda" ctrl+a>e calendar end` + "\n",
			want: `command "Agenda" ctrl+a>e calendar end` + "\n\n" + keymapNote + "\n" +
				`command "End remote session" --error-hud ` + end + "\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := wireKeymap(tc.in, testAgr, tc.key)
			if got != tc.want {
				t.Fatalf("wireKeymap() =\n%s\nwant\n%s", got, tc.want)
			}
			if again := wireKeymap(got, testAgr, tc.key); again != got {
				t.Fatalf("second wireKeymap() changed the file:\n%s", again)
			}
		})
	}
}

type agtermConfigFake struct {
	hooks, keymap string
	pathsErr      error
	counts        map[string]int
	reloaded      []string
}

func (f *agtermConfigFake) ConfigPaths(context.Context) (string, string, error) {
	return f.hooks, f.keymap, f.pathsErr
}

func (f *agtermConfigFake) Reload(_ context.Context, file string) (int, error) {
	f.reloaded = append(f.reloaded, file)
	return f.counts[file], nil
}

func TestSetupWritesAgtermFilesAndReloads(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	config := &agtermConfigFake{hooks: filepath.Join(dir, "cfg", "hooks.conf"), keymap: filepath.Join(dir, "cfg", "keymap.conf")}
	var out bytes.Buffer
	asked := 0
	deps := SetupDependencies{Config: config, Agr: "/opt/homebrew/bin/agr", Home: dir, Out: &out,
		AskKey: func() (string, error) { asked++; return " ctrl+a>x\n", nil }}
	if err := Setup(context.Background(), deps); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	hooks := readFile(t, config.hooks)
	if !strings.Contains(hooks, "on pane.split     "+testAgr+" on-split\n") {
		t.Fatalf("hooks.conf = %q", hooks)
	}
	if keymap := readFile(t, config.keymap); !strings.Contains(keymap, `ctrl+a>x --error-hud `+testAgr+` end "{AGT_SESSION_ID}"`) {
		t.Fatalf("keymap.conf = %q", keymap)
	}
	if !reflect.DeepEqual(config.reloaded, []string{"hooks", "keymap"}) {
		t.Fatalf("reloaded = %v", config.reloaded)
	}
	if !strings.Contains(out.String(), "updated   "+config.hooks) {
		t.Fatalf("output = %q", out.String())
	}

	out.Reset()
	if err := Setup(context.Background(), deps); err != nil {
		t.Fatalf("second Setup() error = %v", err)
	}
	if !strings.Contains(out.String(), "unchanged "+config.hooks) || !strings.Contains(out.String(), "unchanged "+config.keymap) {
		t.Fatalf("second run output = %q", out.String())
	}
	if asked != 1 {
		t.Fatalf("asked for a key %d times, want once: the second run finds the command", asked)
	}
}

func TestSetupWithoutAgtermUsesDefaultDirectory(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	config := &agtermConfigFake{pathsErr: errors.New("no socket")}
	var out bytes.Buffer
	err := Setup(context.Background(), SetupDependencies{Config: config, Agr: "/usr/local/bin/agr", Home: home, Out: &out})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	readFile(t, filepath.Join(home, ".config", "agterm", "hooks.conf"))
	readFile(t, filepath.Join(home, ".config", "agterm", "keymap.conf"))
	if len(config.reloaded) != 0 {
		t.Fatalf("reloaded = %v, want none without agterm", config.reloaded)
	}
	if !strings.Contains(out.String(), "agtermctl hooks reload") {
		t.Fatalf("output = %q, want the manual reload hint", out.String())
	}
}

func TestSetupReportsLinesAgtermRejects(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	config := &agtermConfigFake{
		hooks: filepath.Join(dir, "hooks.conf"), keymap: filepath.Join(dir, "keymap.conf"),
		counts: map[string]int{"keymap": 1},
	}
	err := Setup(context.Background(), SetupDependencies{Config: config, Agr: "/opt/homebrew/bin/agr"})
	if err == nil || !strings.Contains(err.Error(), "1 in keymap.conf") {
		t.Fatalf("Setup() error = %v, want the keymap problem", err)
	}
}

func TestSetupWritesThroughASymlink(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "hooks.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("on status true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "hooks.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	config := &agtermConfigFake{hooks: link, keymap: filepath.Join(dir, "keymap.conf")}
	if err := Setup(context.Background(), SetupDependencies{Config: config, Agr: "/opt/homebrew/bin/agr"}); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("hooks.conf is no longer a symlink: %v", err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("target mode changed: %v %v", info.Mode(), err)
	}
	if got := readFile(t, target); !strings.HasPrefix(got, "on status true\n\n"+hooksNote) {
		t.Fatalf("target = %q", got)
	}
}

func TestSetupWithoutKeySuggestsBindingLater(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	config := &agtermConfigFake{hooks: filepath.Join(dir, "hooks.conf"), keymap: filepath.Join(dir, "keymap.conf")}
	var out bytes.Buffer
	deps := SetupDependencies{Config: config, Agr: "/opt/homebrew/bin/agr", Out: &out,
		AskKey: func() (string, error) { return "\n", nil }}
	if err := Setup(context.Background(), deps); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if !strings.Contains(readFile(t, config.keymap), `command "End remote session" --error-hud `) {
		t.Fatalf("keymap.conf = %q", readFile(t, config.keymap))
	}
	if !strings.Contains(out.String(), "agr setup --end-key") {
		t.Fatalf("output = %q, want the hint to bind a key later", out.String())
	}

	out.Reset()
	deps.AskKey, deps.EndKey = nil, "ctrl+a>x"
	if err := Setup(context.Background(), deps); err != nil {
		t.Fatalf("Setup(--end-key) error = %v", err)
	}
	if !strings.Contains(readFile(t, config.keymap), `"End remote session" ctrl+a>x --error-hud`) || strings.Contains(out.String(), "--end-key") {
		t.Fatalf("keymap.conf = %q, output = %q", readFile(t, config.keymap), out.String())
	}
}

func TestSetupRejectsInvalidKey(t *testing.T) {
	t.Helper()
	for _, key := range []string{"--error-hud", "ctrl+a x", `ctrl+"`, "ctrl+#"} {
		dir := t.TempDir()
		err := Setup(context.Background(), SetupDependencies{Agr: "/opt/homebrew/bin/agr", Home: dir, EndKey: key})
		if err == nil {
			t.Fatalf("Setup(--end-key %q) error = nil", key)
		}
	}
}

func TestSetupRejectsUnusableAgrPath(t *testing.T) {
	t.Helper()
	for _, agr := range []string{"", "agr", "/Users/o'brien/bin/agr", "/tmp/a\nb/agr"} {
		if err := Setup(context.Background(), SetupDependencies{Agr: agr, Home: t.TempDir()}); err == nil {
			t.Fatalf("Setup(%q) error = nil", agr)
		}
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}
