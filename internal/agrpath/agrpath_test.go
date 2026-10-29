package agrpath

import (
	"os"
	"path/filepath"
	"testing"
)

// brewTree lays out <prefix>/<kind>/agr/1.3.0/<rel> as a file and returns the
// prefix and that file.
func brewTree(t *testing.T, kind, rel string) (prefix, binary string) {
	t.Helper()
	prefix = t.TempDir()
	binary = filepath.Join(prefix, kind, "agr", "1.3.0", rel)
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	return prefix, binary
}

func TestStableCaskroomPathWithMatchingSymlink(t *testing.T) {
	t.Helper()
	prefix, binary := brewTree(t, "Caskroom", "agr")
	link := filepath.Join(prefix, "bin", "agr")
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	if got := stable(binary); got != link {
		t.Fatalf("stable(%q) = %q, want %q", binary, got, link)
	}
}

func TestStableCellarPathWithMatchingSymlink(t *testing.T) {
	t.Helper()
	prefix, binary := brewTree(t, "Cellar", "bin/agr")
	link := filepath.Join(prefix, "bin", "agr")
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	if got := stable(binary); got != link {
		t.Fatalf("stable(%q) = %q, want %q", binary, got, link)
	}
}

func TestStableCellarPathWithSymlinkToAnotherBinary(t *testing.T) {
	t.Helper()
	prefix, binary := brewTree(t, "Cellar", "bin/agr")
	other := filepath.Join(prefix, "other-agr")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(prefix, "bin", "agr")); err != nil {
		t.Fatal(err)
	}
	if got := stable(binary); got != binary {
		t.Fatalf("stable(%q) = %q, want the binary itself", binary, got)
	}
}

func TestStableCellarPathWithoutSymlink(t *testing.T) {
	t.Helper()
	_, binary := brewTree(t, "Cellar", "bin/agr")
	if got := stable(binary); got != binary {
		t.Fatalf("stable(%q) = %q, want the binary itself", binary, got)
	}
}

func TestStableNonBrewPathIsUnchanged(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "agr")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A bin/agr link beside it must not be picked up for a non-brew path.
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, filepath.Join(dir, "bin", "agr")); err != nil {
		t.Fatal(err)
	}
	if got := stable(binary); got != binary {
		t.Fatalf("stable(%q) = %q, want unchanged", binary, got)
	}
}

func TestStableReturnsAnAbsolutePathForTheRunningBinary(t *testing.T) {
	t.Helper()
	if got := Stable(); got == "" || (got != "agr" && !filepath.IsAbs(got)) {
		t.Fatalf("Stable() = %q", got)
	}
}
