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

// TestCtlHudCloseTreatsNoHudAsSuccess pins the real-host finding: every bridge
// start logged `close reconnecting HUD … error: no hud` for rows that never
// had one. Only that answer is swallowed; any other failure still surfaces.
func TestCtlHudCloseTreatsNoHudAsSuccess(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockRunner(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, runner, nil, nil)
	ctx := context.Background()

	runner.EXPECT().Run(gomock.Any(), testCtlPath, "session", "hud", "close", "--target", "row-1", "--socket", testCtlSock).
		Return(errors.New("agtermctl: exit status 1: error: no hud"))
	if err := ctl.HudClose(ctx, "row-1"); err != nil {
		t.Fatalf("HudClose() with no HUD open = %v, want nil", err)
	}
	runner.EXPECT().Run(gomock.Any(), testCtlPath, "session", "hud", "close", "--target", "row-1", "--socket", testCtlSock).
		Return(errors.New("agtermctl: exit status 1: error: no such session: row-1"))
	if err := ctl.HudClose(ctx, "row-1"); err == nil || !strings.Contains(err.Error(), "no such session") {
		t.Fatalf("HudClose() other error = %v, want it surfaced", err)
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

// TestCtlRestoreModeDecodesAgterm027Shape pins the 0.27 response, which nests
// the mode under result.restore and reports the one in force as "active".
// Captured from agterm 0.27.1: doctor showed "missing mode" against it.
func TestCtlRestoreModeDecodesAgterm027Shape(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	outputter := mocks.NewMockOutputter(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
	outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "restore", "mode", "--json", "--socket", testCtlSock).
		Return([]byte(`{"result":{"restore":{"restartRequired":false,"requestedAtLaunch":"rerun","configured":"rerun","active":"rerun"}},"ok":true}`), 0, nil)
	mode, err := ctl.RestoreMode(context.Background())
	if err != nil {
		t.Fatalf("RestoreMode() error = %v", err)
	}
	if mode != "rerun" {
		t.Fatalf("RestoreMode() = %q, want rerun", mode)
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
		Return(io.NopCloser(strings.NewReader("{\"kind\":\"session.closed\",\"id\":\"row-1\"}\n{\"kind\":\"session.closed\",\"session_id\":\"row-2\"}\n{\"kind\":\"session.closed\",\"session\":\"row-3\",\"workspace\":\"ws-1\",\"window\":\"win-1\",\"payload\":{\"name\":\"x\"}}\n")), func() error {
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
	if want := []string{"row-1", "row-2", "row-3"}; !equalStrings(t, got, want) {
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

func TestCtlSurfacesDecodesPanesPerRow(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	outputter := mocks.NewMockOutputter(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
	tree := `{"result":{"tree":{"workspaces":[{"sessions":[
		{"id":"row-1","surfaces":[{"kind":"left","paneID":"L","visible":true,"active":false},
			{"kind":"right","paneID":"R","visible":true},{"kind":"scratch","paneID":"S","visible":false}]},
		{"id":"row-2"}]}]}}}`
	outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "tree", "--json", "--socket", testCtlSock).
		Return([]byte(tree), 0, nil)
	got, err := ctl.Surfaces(context.Background())
	if err != nil {
		t.Fatalf("Surfaces() error = %v", err)
	}
	want := []agterm.Surface{{Kind: "left", PaneID: "L", Visible: true}, {Kind: "right", PaneID: "R", Visible: true}, {Kind: "scratch", PaneID: "S"}}
	if len(got["row-1"]) != 3 || got["row-1"][0] != want[0] || got["row-1"][1] != want[1] || got["row-1"][2] != want[2] {
		t.Fatalf("Surfaces()[row-1] = %#v, want %#v", got["row-1"], want)
	}
	if surfaces, ok := got["row-2"]; !ok || len(surfaces) != 0 {
		t.Fatalf("Surfaces()[row-2] = %#v, %v; want present and empty", surfaces, ok)
	}
}

func TestCtlPaneTextAndTypeLineTargetThePaneByID(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	outputter := mocks.NewMockOutputter(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
	ctx := context.Background()

	outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "session", "text", "--target", "row-1", "--pane-id", "P", "--socket", testCtlSock).
		Return([]byte("$ \n"), 0, nil)
	if text, err := ctl.PaneText(ctx, "row-1", "P"); err != nil || text != "$ \n" {
		t.Fatalf("PaneText() = (%q, %v)", text, err)
	}
	outputter.EXPECT().Output(gomock.Any(), []byte("--help\n"), testCtlPath, "session", "type", "--stdin", "--target", "row-1", "--pane-id", "P", "--socket", testCtlSock).
		Return(nil, 0, nil)
	if err := ctl.TypeLine(ctx, "row-1", "P", "--help"); err != nil {
		t.Fatalf("TypeLine() error = %v", err)
	}
	outputter.EXPECT().Output(gomock.Any(), gomock.Any(), testCtlPath, "session", "type", "--stdin", "--target", "row-1", "--pane-id", "gone", "--socket", testCtlSock).
		Return([]byte("error: unknown pane"), 1, nil)
	if err := ctl.TypeLine(ctx, "row-1", "gone", "x"); err == nil || !strings.Contains(err.Error(), "unknown pane") {
		t.Fatalf("TypeLine() error = %v, want the agtermctl refusal", err)
	}
}

func TestCtlConfirmFailsClosed(t *testing.T) {
	t.Helper()
	args := []any{"ask", "open", "End a1?", "--message", "kills it",
		"--button", "end=End", "--button", "cancel=Cancel", "--destructive", "end", "--default", "cancel",
		"--target", "row-1", "--socket", testCtlSock}
	for _, tc := range []struct {
		name string
		out  string
		exit int
		err  error
		want bool
		fail bool
	}{
		// The answered shape probed on agterm 0.27 with a real click.
		{name: "confirmed", out: `{"id":"end","index":0,"result":"answered","label":"End"}`, want: true},
		{name: "other button", out: `{"id":"cancel","index":1,"result":"answered","label":"Cancel"}`},
		// ExecRunner reports every non-zero exit with an error as well, so
		// the cancel and failure rows carry one, as the real runner does.
		{name: "cancelled", out: `{"result":"cancelled"}`, exit: 2, err: errors.New("exit status 2")},
		{name: "unknown shape", out: `{"result":"pending","id":"end"}`},
		{name: "not json", out: `End`, fail: true},
		{name: "dialog failed", out: `error: no session`, exit: 1, err: errors.New("exit status 1"), fail: true},
		{name: "exec failed", err: errors.New("no agtermctl"), fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			outputter := mocks.NewMockOutputter(ctrl)
			ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
			outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, args...).Return([]byte(tc.out), tc.exit, tc.err)
			got, err := ctl.Confirm(context.Background(), "row-1", "End a1?", "kills it", "end", "End")
			if got != tc.want || (err != nil) != tc.fail {
				t.Fatalf("Confirm() = (%v, %v), want (%v, failure %v)", got, err, tc.want, tc.fail)
			}
		})
	}
}

func TestCtlCloseRow(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockRunner(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, runner, nil, nil)
	runner.EXPECT().Run(gomock.Any(), testCtlPath, "session", "close", "--target", "row-1", "--socket", testCtlSock)
	if err := ctl.CloseRow(context.Background(), "row-1"); err != nil {
		t.Fatalf("CloseRow() error = %v", err)
	}
}

// TestCtlConfigPathsReadsListedPaths uses the `hooks list` / `keymap list`
// shapes probed on agterm 0.35.
func TestCtlConfigPathsReadsListedPaths(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	outputter := mocks.NewMockOutputter(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
	outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "hooks", "list", "--json", "--socket", testCtlSock).
		Return([]byte(`{"result":{"hooks":{"diagnostics":[],"hooks":[],"path":"\/cfg\/hooks.conf"}},"ok":true}`), 0, nil)
	outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "keymap", "list", "--json", "--socket", testCtlSock).
		Return([]byte(`{"result":{"keymap":{"commands":[],"diagnostics":[],"path":"/cfg/keymap.conf"}},"ok":true}`), 0, nil)
	hooks, keymap, err := ctl.ConfigPaths(context.Background())
	if err != nil || hooks != "/cfg/hooks.conf" || keymap != "/cfg/keymap.conf" {
		t.Fatalf("ConfigPaths() = (%q, %q, %v)", hooks, keymap, err)
	}
}

func TestCtlConfigPathsRejectsMissingPath(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name string
		out  string
		exit int
		err  error
	}{
		{name: "no path", out: `{"result":{"hooks":{"hooks":[]}},"ok":true}`},
		{name: "relative path", out: `{"result":{"hooks":{"path":"hooks.conf"}},"ok":true}`},
		{name: "not json", out: `ok`},
		{name: "old agterm", out: `Error: Unexpected argument 'hooks'`, exit: 64, err: errors.New("exit status 64")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			outputter := mocks.NewMockOutputter(ctrl)
			ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
			outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "hooks", "list", "--json", "--socket", testCtlSock).
				Return([]byte(tc.out), tc.exit, tc.err)
			if _, _, err := ctl.ConfigPaths(context.Background()); err == nil {
				t.Fatal("ConfigPaths() error = nil, want failure")
			}
		})
	}
}

func TestCtlReloadReturnsDiagnosticCount(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	outputter := mocks.NewMockOutputter(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
	outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "keymap", "reload", "--json", "--socket", testCtlSock).
		Return([]byte(`{"ok":true,"result":{"count":2}}`), 0, nil)
	if count, err := ctl.Reload(context.Background(), "keymap"); err != nil || count != 2 {
		t.Fatalf("Reload() = (%d, %v), want (2, nil)", count, err)
	}
	outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "hooks", "reload", "--json", "--socket", testCtlSock).
		Return([]byte(`no socket`), 1, errors.New("exit status 1"))
	if _, err := ctl.Reload(context.Background(), "hooks"); err == nil {
		t.Fatal("Reload() error = nil, want failure")
	}
}
