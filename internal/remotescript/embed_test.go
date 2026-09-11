package remotescript

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestScriptSubstitutesVersion(t *testing.T) {
	t.Helper()
	wantVersion := "1.0.7"
	got := Script(wantVersion)

	if bytes.Contains(got, []byte("@VERSION@")) {
		t.Fatal("Script() left the version placeholder in the script")
	}
	if !bytes.Contains(got, []byte("AGR_VERSION=\"1.0.7\"")) {
		t.Fatalf("Script() does not contain AGR_VERSION=%q", wantVersion)
	}
}

func TestScriptMatchesEmbeddedFileApartFromVersion(t *testing.T) {
	t.Helper()
	source, err := os.ReadFile("agr.sh")
	if err != nil {
		t.Fatalf("read agr.sh: %v", err)
	}
	want := bytes.ReplaceAll(source, []byte("@VERSION@"), []byte("test"))
	got := Script("test")
	if !bytes.Equal(got, want) {
		t.Fatal("Script() differs from agr.sh beyond the version substitution")
	}
	if !strings.HasPrefix(string(got), "#!/bin/sh\nset -eu\n") {
		t.Fatal("embedded script does not preserve the POSIX shebang and set -eu")
	}
}
