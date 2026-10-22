// Package skill holds the Claude Code skill agr ships and installs.
package skill

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed SKILL.md
var source []byte

// Marker starts the line that marks a SKILL.md as agr's own. Only a file that
// carries it may be replaced.
const Marker = "<!-- agr-skill "

// FileName is the skill's file inside its directory.
const FileName = "SKILL.md"

// Install outcomes.
const (
	Installed = "installed"
	Updated   = "updated"
	Unchanged = "unchanged"
	// Kept means the file has no marker: the user's own, left alone.
	Kept = "kept"
)

// Bytes is the skill with version stamped into the marker.
func Bytes(version string) []byte {
	return bytes.ReplaceAll(source, []byte("@VERSION@"), []byte(version))
}

// Install writes the skill to dir/SKILL.md when the file is missing or
// carries the marker, and says which of the outcomes happened. A symlinked
// file is written through to its target.
func Install(dir, version string) (string, error) {
	name := filepath.Join(dir, FileName)
	if target, err := filepath.EvalSymlinks(name); err == nil {
		name = target
	}
	want := Bytes(version)
	state := Installed
	switch existing, err := os.ReadFile(name); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", err
	case !bytes.Contains(existing, []byte("\n"+Marker)):
		return Kept, nil
	case bytes.Equal(existing, want):
		return Unchanged, nil
	default:
		state = Updated
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), "."+FileName+".*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(want); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), name); err != nil {
		return "", fmt.Errorf("write %s: %w", name, err)
	}
	return state, nil
}
