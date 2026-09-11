package agterm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/agterm/mocks"
	"go.uber.org/mock/gomock"
)

const (
	testCtlPath = "/absolute/agtermctl"
	testCtlSock = "/tmp/agterm-test.sock"
)

func TestCtlRunnerMethodsUseExactArgv(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockRunner(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, runner, nil, nil)
	ctx := context.Background()

	runner.EXPECT().Run(gomock.Any(), testCtlPath, "session", "hud", "open", "host unreachable", "--target", "row-1", "--socket", testCtlSock)
	if err := ctl.HudOpen(ctx, "row-1", "host unreachable"); err != nil {
		t.Fatalf("HudOpen() error = %v", err)
	}
	runner.EXPECT().Run(gomock.Any(), testCtlPath, "session", "hud", "close", "--target", "row-1", "--socket", testCtlSock)
	if err := ctl.HudClose(ctx, "row-1"); err != nil {
		t.Fatalf("HudClose() error = %v", err)
	}
	runner.EXPECT().Run(gomock.Any(), testCtlPath, "session", "rename", "new name", "--target", "row-1", "--socket", testCtlSock)
	if err := ctl.Rename(ctx, "row-1", "new name"); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	runner.EXPECT().Run(gomock.Any(), testCtlPath, "session", "context", "purpose", "--target", "row-1", "--socket", testCtlSock)
	if err := ctl.Context(ctx, "row-1", "purpose"); err != nil {
		t.Fatalf("Context() error = %v", err)
	}
}

func TestCtlContextUnknownSubcommandIsBestEffort(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockRunner(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, runner, nil, nil)
	runner.EXPECT().Run(gomock.Any(), testCtlPath, "session", "context", "purpose", "--target", "row-1", "--socket", testCtlSock).
		Return(errors.New("agtermctl: unknown subcommand context"))
	if err := ctl.Context(context.Background(), "row-1", "purpose"); err != nil {
		t.Fatalf("Context() error = %v, want nil for unknown subcommand", err)
	}
}

func TestCtlRestoreModeUsesJSONOutput(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	outputter := mocks.NewMockOutputter(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
	outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "restore", "mode", "--json", "--socket", testCtlSock).
		Return([]byte(`{"result":{"mode":"live"}}`), 0, nil)
	mode, err := ctl.RestoreMode(context.Background())
	if err != nil {
		t.Fatalf("RestoreMode() error = %v", err)
	}
	if mode != "live" {
		t.Fatalf("RestoreMode() = %q, want live", mode)
	}
}

func TestCtlRestoreModeReportsUnsupported(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	outputter := mocks.NewMockOutputter(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
	outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "restore", "mode", "--json", "--socket", testCtlSock).
		Return(nil, 1, errors.New("agtermctl: unknown subcommand restore"))
	_, err := ctl.RestoreMode(context.Background())
	if !errors.Is(err, agterm.ErrUnsupported) {
		t.Fatalf("RestoreMode() error = %v, want ErrUnsupported", err)
	}
}

func TestCtlSupportsContextUsesHelpAndReportsUnsupported(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockRunner(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, runner, nil, nil)
	runner.EXPECT().Run(gomock.Any(), testCtlPath, "session", "context", "--help", "--socket", testCtlSock).
		Return(nil)
	supported, err := ctl.SupportsContext(context.Background())
	if err != nil || !supported {
		t.Fatalf("SupportsContext() = %v, %v, want true, nil", supported, err)
	}

	runner.EXPECT().Run(gomock.Any(), testCtlPath, "session", "context", "--help", "--socket", testCtlSock).
		Return(errors.New("agtermctl: unknown subcommand context"))
	supported, err = ctl.SupportsContext(context.Background())
	if supported || !errors.Is(err, agterm.ErrUnsupported) {
		t.Fatalf("SupportsContext() = %v, %v, want false, ErrUnsupported", supported, err)
	}
}

func TestCtlTreeUsesOutputterAndDecodesRows(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	outputter := mocks.NewMockOutputter(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
	tree := `{"result":{"tree":{"workspaces":[{"sessions":[{"id":"row-1"},{"id":"row-2"}]}]}}}`
	outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "tree", "--json", "--socket", testCtlSock).
		Return([]byte(tree), 0, nil)
	rows, err := ctl.Tree(context.Background())
	if err != nil {
		t.Fatalf("Tree() error = %v", err)
	}
	if want := []string{"row-1", "row-2"}; !equalStrings(t, rows, want) {
		t.Fatalf("Tree() = %#v, want %#v", rows, want)
	}
}

func TestCtlTreeReportsMalformedOutput(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	outputter := mocks.NewMockOutputter(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
	outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "tree", "--json", "--socket", testCtlSock).
		Return([]byte("not json"), 0, nil)
	if _, err := ctl.Tree(context.Background()); err == nil || !strings.Contains(err.Error(), "decode agterm tree") {
		t.Fatalf("Tree() error = %v, want tree decode error", err)
	}
}

func TestCtlPickUsesOutputterAndAllowsEmptyItems(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	outputter := mocks.NewMockOutputter(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
	outputter.EXPECT().Output(gomock.Any(), []byte("[]"), testCtlPath, "pick", "open", "--prompt", "choose a session", "--allow-custom", "--socket", testCtlSock).
		Return([]byte(`{"result":"custom","query":"new-session"}`), 0, nil)
	result, err := ctl.Pick(context.Background(), []agterm.PickItem{}, "choose a session")
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if result != (agterm.PickResult{Kind: "custom", Query: "new-session"}) {
		t.Fatalf("Pick() = %#v, want custom result", result)
	}
}

func TestCtlPickDecodesCancellationEvenWhenCommandReturnsExitError(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	outputter := mocks.NewMockOutputter(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
	outputter.EXPECT().Output(gomock.Any(), []byte("[]"), testCtlPath, "pick", "open", "--prompt", "choose", "--allow-custom", "--socket", testCtlSock).
		Return([]byte(`{"result":"cancelled"}`), 2, errors.New("agtermctl: exit status 2"))
	_, err := ctl.Pick(context.Background(), []agterm.PickItem{}, "choose")
	if !errors.Is(err, agterm.ErrCancelled) {
		t.Fatalf("Pick() error = %v, want ErrCancelled", err)
	}
}

func TestDecodePick(t *testing.T) {
	t.Helper()
	tests := []struct {
		name string
		out  string
		exit int
		want agterm.PickResult
		err  error
	}{
		{name: "picked", out: `{"result":"picked","id":"row-1"}`, want: agterm.PickResult{Kind: "picked", ID: "row-1"}},
		{name: "custom preserves query", out: `{"result":"custom","query":"a query with spaces"}`, want: agterm.PickResult{Kind: "custom", Query: "a query with spaces"}},
		{name: "cancelled result", out: `{"result":"cancelled"}`, err: agterm.ErrCancelled},
		{name: "cancelled exit", exit: 2, err: agterm.ErrCancelled},
		{name: "exit one", exit: 1, err: errors.New("agtermctl pick exited with status 1")},
		{name: "malformed", out: "{", err: errors.New("decode agterm picker result")},
		{name: "empty picked id", out: `{"result":"picked"}`, err: errors.New("empty picked id")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			got, err := agterm.DecodePick([]byte(tt.out), tt.exit)
			if tt.err != nil {
				if err == nil || !strings.Contains(err.Error(), tt.err.Error()) {
					t.Fatalf("DecodePick() error = %v, want text %q", err, tt.err)
				}
				if errors.Is(tt.err, agterm.ErrCancelled) && !errors.Is(err, agterm.ErrCancelled) {
					t.Fatalf("DecodePick() error = %v, want ErrCancelled", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodePick() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("DecodePick() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCtlClosedRowsUsesStreamerAndCloses(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	streamer := mocks.NewMockStreamer(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, nil, streamer)
	stopCalled := false
	streamer.EXPECT().Stream(gomock.Any(), testCtlPath, "events", "--json", "--kind", "session.closed", "--socket", testCtlSock).
		Return(io.NopCloser(strings.NewReader("{\"kind\":\"session.closed\",\"id\":\"row-1\"}\n{\"kind\":\"session.closed\",\"session_id\":\"row-2\"}\n")), func() error {
			stopCalled = true
			return nil
		}, nil)
	rows, err := ctl.ClosedRows(context.Background())
	if err != nil {
		t.Fatalf("ClosedRows() error = %v", err)
	}
	var got []string
	for row := range rows {
		got = append(got, row)
	}
	if want := []string{"row-1", "row-2"}; !equalStrings(t, got, want) {
		t.Fatalf("ClosedRows() = %#v, want %#v", got, want)
	}
	if !stopCalled {
		t.Fatal("ClosedRows() did not stop stream")
	}
}

func TestCtlClosedRowsCancelClosesCleanly(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	streamer := mocks.NewMockStreamer(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, nil, streamer)
	reader, writer := io.Pipe()
	stopCalled := make(chan struct{})
	streamer.EXPECT().Stream(gomock.Any(), testCtlPath, "events", "--json", "--kind", "session.closed", "--socket", testCtlSock).
		Return(reader, func() error {
			close(stopCalled)
			return nil
		}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	rows, err := ctl.ClosedRows(ctx)
	if err != nil {
		t.Fatalf("ClosedRows() error = %v", err)
	}
	cancel()
	_ = writer.Close()
	for row := range rows {
		if row != "" {
			t.Fatalf("ClosedRows() yielded row %q after cancellation", row)
		}
	}
	select {
	case <-stopCalled:
	default:
		t.Fatal("ClosedRows() did not stop stream after cancellation")
	}
}

func equalStrings(t *testing.T, got, want []string) bool {
	t.Helper()
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestPickItemJSONTags(t *testing.T) {
	t.Helper()
	got, err := json.Marshal([]agterm.PickItem{{ID: "row", Label: "Row", Subtitle: "cmd · active · 1m"}})
	if err != nil {
		t.Fatalf("marshal PickItem: %v", err)
	}
	if want := `[{"id":"row","label":"Row","subtitle":"cmd · active · 1m"}]`; string(got) != want {
		t.Fatalf("marshal PickItem = %s, want %s", got, want)
	}
}
