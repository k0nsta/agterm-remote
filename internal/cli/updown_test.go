package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

type stubBridge struct {
	up, down error
}

func (b *stubBridge) Up(context.Context, string) error   { return b.up }
func (b *stubBridge) Down(context.Context, string) error { return b.down }

// TestRunDownWithoutDaemonIsSuccess pins `agr down` as idempotent: Down never
// starts a daemon, so no daemon means no bridge — exit 0, not a reported failure.
func TestRunDownWithoutDaemonIsSuccess(t *testing.T) {
	t.Helper()
	var errw bytes.Buffer
	if code := RunDown(context.Background(), "host-a", &stubBridge{down: ErrDaemonNotRunning}, &errw); code != 0 {
		t.Fatalf("RunDown() with no daemon = %d, want 0; stderr %q", code, errw.String())
	}
	if !strings.Contains(errw.String(), "nothing to stop") {
		t.Fatalf("RunDown() stderr = %q, want the not-running note", errw.String())
	}
}

// TestRunBridgeOtherErrorsStillFail keeps the shortcut narrow: a real Down
// failure and an unreachable daemon on `up` are still exit 1.
func TestRunBridgeOtherErrorsStillFail(t *testing.T) {
	t.Helper()
	boom := errors.New("boom")
	if code := RunDown(context.Background(), "host-a", &stubBridge{down: boom}, nil); code != 1 {
		t.Fatalf("RunDown() with a real error = %d, want 1", code)
	}
	if code := RunUp(context.Background(), "host-a", &stubBridge{up: ErrDaemonNotRunning}, nil); code != 1 {
		t.Fatalf("RunUp() with no daemon = %d, want 1", code)
	}
}
