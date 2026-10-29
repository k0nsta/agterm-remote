// Package agrpath finds the path other programs should use to run this agr.
package agrpath

import (
	"os"
	"path/filepath"
	"strings"
)

// Stable returns the absolute path to run this agr by from hooks, keymap
// commands and the proxy. Homebrew puts the binary under a versioned
// Caskroom or Cellar directory that `brew upgrade` removes, so when the
// running binary lives there the <prefix>/bin/agr symlink is returned, if it
// resolves to the same binary. Anything else is os.Executable() as it is, and
// "agr" (looked up on PATH) when even that is unknown.
func Stable() string {
	self, err := os.Executable()
	if err != nil || self == "" {
		return "agr"
	}
	return stable(self)
}

func stable(self string) string {
	prefix, ok := brewPrefix(self)
	if !ok {
		return self
	}
	link := filepath.Join(prefix, "bin", "agr")
	want, err := filepath.EvalSymlinks(self)
	if err != nil {
		return self
	}
	got, err := filepath.EvalSymlinks(link)
	if err != nil || got != want {
		return self
	}
	return link
}

// brewPrefix is the Homebrew prefix of a path under <prefix>/Caskroom/ or
// <prefix>/Cellar/.
func brewPrefix(path string) (string, bool) {
	for _, dir := range []string{"/Caskroom/", "/Cellar/"} {
		if before, _, found := strings.Cut(path, dir); found && before != "" {
			return before, true
		}
	}
	return "", false
}
