package remote_ssh_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/remote"
)

func TestExecSSHUsesPathShimAndExactArgv(t *testing.T) {
	t.Helper()
	shimDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatalf("resolve shim directory: %v", err)
	}
	record := filepath.Join(t.TempDir(), "argv")
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REMOTE_SSH_SHIM_MODE", "success")
	t.Setenv("REMOTE_SSH_SHIM_RECORD", record)

	client := remote.NewExecSSH()
	body, err := client.Run(context.Background(), "user@example.com", []byte("ignored"), "printf", "%s", "$HOME")
	if err != nil {
		t.Fatalf("ExecSSH.Run() error = %v", err)
	}
	if string(body) != "agr\t1.0.0\npayload\n" {
		t.Fatalf("ExecSSH.Run() body = %q, want stdout without stderr", body)
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read argv record: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	wantArgs := []string{"-o", "BatchMode=yes", "user@example.com", "--", "printf", "%s", "$HOME"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("ssh argv = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestExecSSHMaps255ToUnreachableAndRetainsStderr(t *testing.T) {
	t.Helper()
	client := newShimClient(t, "unreachable")
	body, err := client.Run(context.Background(), "host", nil, "agr", "sessions")
	if !errors.Is(err, remote.ErrUnreachable) {
		t.Fatalf("Run() error = %v, want ErrUnreachable", err)
	}
	if !strings.Contains(err.Error(), "network is down") {
		t.Fatalf("Run() error = %v, want stderr text", err)
	}
	if string(body) != "partial stdout\n" {
		t.Fatalf("Run() body = %q, want stdout preserved", body)
	}
}

func TestExecSSHReturnsTypedNon255Exit(t *testing.T) {
	t.Helper()
	client := newShimClient(t, "exit")
	_, err := client.Run(context.Background(), "host", nil, "agr", "reap", "api")
	var exitErr *remote.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Run() error = %v, want ExitError", err)
	}
	if exitErr.Code != 42 {
		t.Fatalf("ExitError.Code = %d, want 42", exitErr.Code)
	}
	if !strings.Contains(err.Error(), "remote refusal") {
		t.Fatalf("Run() error = %v, want stderr text", err)
	}
}

func TestExecSSHRejectsUnsafeHostBeforeStartingShim(t *testing.T) {
	t.Helper()
	record := filepath.Join(t.TempDir(), "argv")
	t.Setenv("REMOTE_SSH_SHIM_RECORD", record)
	client := newShimClient(t, "success")
	_, err := client.Run(context.Background(), "-oProxyCommand=bad", nil, "agr", "sessions")
	if err == nil || !strings.Contains(err.Error(), "invalid host") {
		t.Fatalf("Run() error = %v, want invalid-host error", err)
	}
	if _, statErr := os.Stat(record); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("shim record stat error = %v, want shim not started", statErr)
	}
}

func newShimClient(t *testing.T, mode string) *remote.ExecSSH {
	t.Helper()
	shimDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatalf("resolve shim directory: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REMOTE_SSH_SHIM_MODE", mode)
	t.Setenv("REMOTE_SSH_SHIM_RECORD", "")
	return remote.NewExecSSH()
}

func TestExecTTYValidatesArgv(t *testing.T) {
	t.Helper()
	if err := (&remote.ExecTTY{}).Interactive(context.Background()); err == nil {
		t.Fatal("ExecTTY.Interactive() error = nil, want empty-argv error")
	}
}
