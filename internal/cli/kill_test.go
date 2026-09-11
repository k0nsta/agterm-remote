package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/remote"
)

func TestKillerPartialFailureAndInvalidName(t *testing.T) {
	t.Helper()
	source := &fakeKillSessions{fail: map[string]error{"infra": errors.New("not agr-managed")}}
	var output bytes.Buffer
	killer := &Killer{Sessions: source, Errors: &output}
	if code := killer.Run(context.Background(), "home", []string{"api", "bad name", "infra"}); code == 0 {
		t.Fatal("Killer.Run() exit = 0, want non-zero")
	}
	if strings.Join(source.calls, ",") != "api,infra" {
		t.Fatalf("reap calls = %v, want valid names only", source.calls)
	}
	if !strings.Contains(output.String(), "invalid name \"bad name\"") || !strings.Contains(output.String(), "not agr-managed") {
		t.Fatalf("errors = %q, want validation and remote failure", output.String())
	}
}

func TestKillerSuccess(t *testing.T) {
	t.Helper()
	source := &fakeKillSessions{}
	if code := (&Killer{Sessions: source}).Run(context.Background(), "home", []string{"api"}); code != 0 {
		t.Fatalf("Killer.Run() exit = %d, want zero", code)
	}
}

func TestKillerRejectsEmptyNamesAndInvalidHost(t *testing.T) {
	t.Helper()
	source := &fakeKillSessions{}
	var output bytes.Buffer
	killer := &Killer{Sessions: source, Errors: &output}
	if code := killer.Run(context.Background(), "home", nil); code == 0 {
		t.Fatal("empty kill names exit = 0, want non-zero")
	}
	if code := killer.Run(context.Background(), "-host", []string{"api"}); code == 0 {
		t.Fatal("invalid host exit = 0, want non-zero")
	}
	if len(source.calls) != 0 {
		t.Fatalf("calls = %v, want none", source.calls)
	}
	if !strings.Contains(output.String(), "requires at least one") || !strings.Contains(output.String(), "invalid host") {
		t.Fatalf("errors = %q, want input errors", output.String())
	}
}

type fakeKillSessions struct {
	calls []string
	fail  map[string]error
}

func (f *fakeKillSessions) Sessions(context.Context, string) ([]remote.Session, error) {
	return nil, nil
}

func (f *fakeKillSessions) Reap(_ context.Context, _, name string) error {
	f.calls = append(f.calls, name)
	return f.fail[name]
}
