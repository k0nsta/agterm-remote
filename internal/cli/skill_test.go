package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/skill"
)

func TestRunSkillPrintsTheEmbeddedSkill(t *testing.T) {
	t.Helper()
	var out, errw bytes.Buffer
	if got := RunSkill("3.1.4", &out, &errw); got != 0 {
		t.Fatalf("RunSkill() = %d, stderr = %q", got, errw.String())
	}
	if !bytes.Equal(out.Bytes(), skill.Bytes("3.1.4")) {
		t.Fatalf("output differs from the embedded skill:\n%s", out.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestRunSkillReportsAWriteFailure(t *testing.T) {
	t.Helper()
	var errw bytes.Buffer
	if got := RunSkill("1", failingWriter{}, &errw); got != 1 || errw.Len() == 0 {
		t.Fatalf("RunSkill() = %d, stderr = %q, want 1 and a message", got, errw.String())
	}
}
