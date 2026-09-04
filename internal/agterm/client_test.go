package agterm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/agterm/agtermtest"
)

type recordingRunner struct {
	mu    sync.Mutex
	calls []runnerCall
}

type runnerCall struct {
	name string
	args []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, runnerCall{name: name, args: append([]string(nil), args...)})
	return nil
}

func (r *recordingRunner) Calls(t *testing.T) []runnerCall {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := make([]runnerCall, len(r.calls))
	copy(calls, r.calls)
	return calls
}

func boolPtr(t *testing.T, value bool) *bool {
	t.Helper()
	return &value
}

func TestProtocolJSONTags(t *testing.T) {
	t.Helper()
	request := agterm.Request{
		Cmd:    "session.status",
		Target: "row-1",
		Args: agterm.StatusArgs{
			Status:    "active",
			Blink:     boolPtr(t, true),
			AutoReset: boolPtr(t, false),
			Pane:      "left",
			PaneID:    "pane-1",
		},
	}
	got, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	want := `{"cmd":"session.status","target":"row-1","args":{"status":"active","blink":true,"autoReset":false,"pane":"left","paneID":"pane-1"}}`
	if string(got) != want {
		t.Fatalf("marshal request = %s, want %s", got, want)
	}

	response, err := json.Marshal(agterm.Response{
		OK:     true,
		Result: json.RawMessage(`{"app":{"version":"0.26.0"}}`),
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if want := `{"ok":true,"result":{"app":{"version":"0.26.0"}}}`; string(response) != want {
		t.Fatalf("marshal response = %s, want %s", response, want)
	}
}

func TestClientStatusOK(t *testing.T) {
	t.Helper()
	sock := agtermtest.NewFakeAgterm(t, func(request agterm.Request) agterm.Response {
		if request.Cmd != "session.status" || request.Target != "row-1" {
			t.Errorf("request = %#v, want session.status for row-1", request)
		}
		return agterm.Response{OK: true}
	})

	client := agterm.NewClient(sock, "/fake/agtermctl", time.Second, nil)
	if err := client.Status(context.Background(), "row-1", agterm.StatusArgs{Status: "active"}); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
}

func TestClientStatusRefused(t *testing.T) {
	t.Helper()
	sock := agtermtest.NewFakeAgterm(t, func(agterm.Request) agterm.Response {
		return agterm.Response{Error: "blocked status owned by pane right"}
	})

	err := agterm.NewClient(sock, "/fake/agtermctl", time.Second, nil).Status(
		context.Background(), "row-1", agterm.StatusArgs{Status: "blocked"},
	)
	if !errors.Is(err, agterm.ErrRefused) {
		t.Fatalf("Status() error = %v, want ErrRefused", err)
	}
	if !strings.HasPrefix(err.Error(), "blocked status owned by pane") {
		t.Fatalf("Status() error = %q, want refusal prefix", err)
	}
}

func TestClientStatusUnknownTarget(t *testing.T) {
	t.Helper()
	sock := agtermtest.NewFakeAgterm(t, func(agterm.Request) agterm.Response {
		return agterm.Response{Error: "no such session: row-1"}
	})

	err := agterm.NewClient(sock, "/fake/agtermctl", time.Second, nil).Status(
		context.Background(), "row-1", agterm.StatusArgs{Status: "completed"},
	)
	if !errors.Is(err, agterm.ErrUnknownTarget) {
		t.Fatalf("Status() error = %v, want ErrUnknownTarget", err)
	}
	if got, want := err.Error(), "no such session: row-1"; got != want {
		t.Fatalf("Status() error = %q, want %q", got, want)
	}
}

func TestClientStatusDecodeFailureFallsBackEveryTimeAndWarnsOnce(t *testing.T) {
	t.Helper()
	var logs bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousWriter) })

	sock := agtermtest.NewFakeAgterm(t, func(agterm.Request) agterm.Response {
		return agterm.Response{Error: "invalid request: unknown field"}
	})
	runner := &recordingRunner{}
	client := agterm.NewClient(sock, "/fake/agtermctl", time.Second, runner)
	args := agterm.StatusArgs{
		Status:    "active",
		Pane:      "right",
		PaneID:    "pane-2",
		Blink:     boolPtr(t, true),
		AutoReset: boolPtr(t, true),
	}
	for i := 0; i < 2; i++ {
		if err := client.Status(context.Background(), "row-1", args); err != nil {
			t.Fatalf("Status() call %d error = %v", i+1, err)
		}
	}

	calls := runner.Calls(t)
	if len(calls) != 2 {
		t.Fatalf("fallback calls = %d, want 2", len(calls))
	}
	wantArgs := []string{"session", "status", "active", "--target", "row-1", "--socket", sock, "--pane", "right", "--pane-id", "pane-2", "--blink", "--auto-reset"}
	for i, call := range calls {
		if call.name != "/fake/agtermctl" {
			t.Errorf("fallback call %d name = %q, want absolute path", i+1, call.name)
		}
		if strings.Join(call.args, "\x00") != strings.Join(wantArgs, "\x00") {
			t.Errorf("fallback call %d args = %#v, want %#v", i+1, call.args, wantArgs)
		}
	}
	if count := strings.Count(logs.String(), "falling back to agtermctl"); count != 1 {
		t.Fatalf("fallback warning count = %d, want 1; logs: %s", count, logs.String())
	}
}

func TestClientStatusDecodeFailureMatchesDecodingError(t *testing.T) {
	t.Helper()
	sock := agtermtest.NewFakeAgterm(t, func(agterm.Request) agterm.Response {
		return agterm.Response{Error: "DecodingError: status"}
	})
	runner := &recordingRunner{}
	if err := agterm.NewClient(sock, "/fake/agtermctl", time.Second, runner).Status(
		context.Background(), "row-1", agterm.StatusArgs{Status: "completed"},
	); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got := len(runner.Calls(t)); got != 1 {
		t.Fatalf("fallback calls = %d, want 1", got)
	}
}

func TestClientStatusConnectionRefusedDoesNotFallBack(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "agr-refuse-")
	if err != nil {
		t.Fatalf("create temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agterm.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	runner := &recordingRunner{}
	err = agterm.NewClient(sock, "/fake/agtermctl", time.Second, runner).Status(
		context.Background(), "row-1", agterm.StatusArgs{Status: "active"},
	)
	if err == nil {
		t.Fatal("Status() error = nil, want connection refusal")
	}
	if len(runner.Calls(t)) != 0 {
		t.Fatal("connection refusal unexpectedly used fallback")
	}
}

func TestClientVersionParsesAndWarnsOnceBelowMinimum(t *testing.T) {
	t.Helper()
	var logs bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousWriter) })

	sock := agtermtest.NewFakeAgterm(t, func(request agterm.Request) agterm.Response {
		if request.Cmd != "version" {
			t.Errorf("request command = %q, want version", request.Cmd)
		}
		return agterm.Response{OK: true, Result: json.RawMessage(`{"app":{"version":"0.24.9"}}`)}
	})
	client := agterm.NewClient(sock, "/fake/agtermctl", time.Second, nil)
	for i := 0; i < 2; i++ {
		got, err := client.Version(context.Background())
		if err != nil {
			t.Fatalf("Version() call %d error = %v", i+1, err)
		}
		if got != "0.24.9" {
			t.Fatalf("Version() = %q, want 0.24.9", got)
		}
	}
	if count := strings.Count(logs.String(), "older than the tested minimum"); count != 1 {
		t.Fatalf("version warning count = %d, want 1; logs: %s", count, logs.String())
	}
}

func TestClientTimeoutHonored(t *testing.T) {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	sock := agtermtest.NewFakeAgterm(t, func(agterm.Request) agterm.Response {
		<-release
		return agterm.Response{OK: true}
	})
	started := time.Now()
	_, err := agterm.NewClient(sock, "/fake/agtermctl", 30*time.Millisecond, nil).Version(context.Background())
	if err == nil {
		t.Fatal("Version() error = nil, want timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Version() error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout took %v, want less than one second", elapsed)
	}
}

func TestIsDecodeFailure(t *testing.T) {
	t.Helper()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "invalid request", err: errors.New("invalid request: field"), want: true},
		{name: "decoding error", err: errors.New("DecodingError"), want: true},
		{name: "other", err: errors.New("no such session: row"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			if got := agterm.IsDecodeFailure(tt.err); got != tt.want {
				t.Fatalf("IsDecodeFailure(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}
