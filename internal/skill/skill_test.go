package skill

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBytesCarryMarkerAndVersion(t *testing.T) {
	t.Helper()
	got := string(Bytes("1.2.3"))
	if !strings.Contains(got, "\n<!-- agr-skill 1.2.3 -->\n") {
		t.Fatalf("marker line with version missing:\n%s", got)
	}
	if strings.Contains(got, "@VERSION@") {
		t.Fatal("version placeholder left in the skill")
	}
}

func TestFrontmatterParses(t *testing.T) {
	t.Helper()
	text := string(Bytes("test"))
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		t.Fatal("skill does not start with frontmatter")
	}
	front, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		t.Fatal("frontmatter is not closed")
	}
	fields := map[string]string{}
	for _, line := range strings.Split(front, "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			t.Fatalf("frontmatter line %q is not key: value", line)
		}
		fields[key] = value
	}
	if fields["name"] != "agr" {
		t.Fatalf("name = %q, want agr", fields["name"])
	}
	for _, trigger := range []string{"on <host>", "from <host>", "remote session", "hand off to <row>"} {
		if !strings.Contains(fields["description"], trigger) {
			t.Errorf("description lacks trigger %q", trigger)
		}
	}
	if !strings.HasPrefix(body, Marker+"test -->\n") {
		t.Fatalf("marker is not the first body line: %q", body[:40])
	}
	for _, section := range []string{"## Spawn on a host", "## Teardown", "## On a remote row", "## Hand off"} {
		if !strings.Contains(body, section) {
			t.Errorf("missing section %q", section)
		}
	}
}

func TestNoHostLiterals(t *testing.T) {
	t.Helper()
	lower := strings.ToLower(string(source))
	for _, literal := range []string{"homelab", "user@", ".local\n", "192.168."} {
		if strings.Contains(lower, literal) {
			t.Errorf("skill contains the host literal %q", literal)
		}
	}
}

func TestInstall(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name      string
		existing  string
		present   bool
		wantState string
		wantFile  func(existing string) string
	}{
		{name: "missing", wantState: Installed, wantFile: func(string) string { return string(Bytes("2.0.0")) }},
		{name: "marked is replaced", existing: "---\n---\n<!-- agr-skill 1.0.0 -->\nold\n", present: true, wantState: Updated, wantFile: func(string) string { return string(Bytes("2.0.0")) }},
		{name: "same version is unchanged", existing: string(Bytes("2.0.0")), present: true, wantState: Unchanged, wantFile: func(string) string { return string(Bytes("2.0.0")) }},
		{name: "unmarked is kept", existing: "my own skill\n", present: true, wantState: Kept, wantFile: func(existing string) string { return existing }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "skills", "agr")
			existing := tc.existing
			if tc.present {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, FileName), []byte(existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			state, err := Install(dir, "2.0.0")
			if err != nil {
				t.Fatalf("Install() error = %v", err)
			}
			if state != tc.wantState {
				t.Fatalf("state = %q, want %q", state, tc.wantState)
			}
			got, err := os.ReadFile(filepath.Join(dir, FileName))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, []byte(tc.wantFile(existing))) {
				t.Fatalf("file = %q", got)
			}
		})
	}
}

func TestInstallWritesThroughSymlink(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	target := filepath.Join(root, "dotfiles", "agr.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("\n"+Marker+"1.0.0 -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "skills", "agr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, FileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, "2.0.0"); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if info, err := os.Lstat(filepath.Join(dir, FileName)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was replaced: %v %v", info, err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, Bytes("2.0.0")) {
		t.Fatal("target not rewritten")
	}
}
