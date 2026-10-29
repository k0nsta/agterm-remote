// Package settings holds the user's agr preferences on the Mac.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Settings are written by `agr setup` and read when they are used, so a
// change needs no daemon restart.
type Settings struct {
	// PageSize is the percent a host's page opens at on a row the host owns
	// whole; 0 keeps pages full.
	PageSize int `json:"page_size_percent,omitempty"`
}

// Path is the settings file: $XDG_CONFIG_HOME/agr/config.json, else
// ~/.config/agr/config.json.
func Path() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "agr", "config.json")
}

// Load reads path; a missing file is the zero Settings.
func Load(path string) (Settings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("read %s: %w", path, err)
	}
	return s, nil
}

// Save writes s to path, replacing it whole.
func Save(path string, s Settings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
