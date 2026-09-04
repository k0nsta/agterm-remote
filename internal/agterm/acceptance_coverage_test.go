package agterm_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/agterm/mocks"
	"go.uber.org/mock/gomock"
)

func TestCtlRestoreModeErrorsAndDecodesSupportedShapes(t *testing.T) {
	t.Helper()
	tests := []struct {
		name    string
		output  string
		exit    int
		runErr  error
		want    string
		wantErr string
	}{
		{name: "direct string", output: `"live"`, want: "live"},
		{name: "flat object", output: `{"restoreMode":"safe"}`, want: "safe"},
		{name: "missing mode", output: `{}`, wantErr: "missing mode"},
		{name: "malformed json", output: `{`, wantErr: "decode agterm restore mode"},
		{name: "nonzero without error", exit: 3, wantErr: "exit 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			ctrl := gomock.NewController(t)
			outputter := mocks.NewMockOutputter(ctrl)
			ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)
			outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "restore", "mode", "--json", "--socket", testCtlSock).
				Return([]byte(tt.output), tt.exit, tt.runErr)
			got, err := ctl.RestoreMode(context.Background())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("RestoreMode() error = %v, want text %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("RestoreMode() = %q, %v, want %q, nil", got, err, tt.want)
			}
		})
	}
}

func TestCtlRestoreModeNilClientAndContextProbeError(t *testing.T) {
	t.Helper()
	var ctl *agterm.Ctl
	if _, err := ctl.RestoreMode(context.Background()); err == nil {
		t.Fatal("nil RestoreMode() error = nil")
	}

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockRunner(ctrl)
	client := agterm.NewCtl(testCtlPath, testCtlSock, runner, nil, nil)
	runner.EXPECT().Run(gomock.Any(), testCtlPath, "session", "context", "--help", "--socket", testCtlSock).
		Return(errors.New("permission denied"))
	if supported, err := client.SupportsContext(context.Background()); supported || err == nil || strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("SupportsContext() = %v, %v, want non-unsupported error", supported, err)
	}
}

func TestCtlTreeAndPickCommandFailures(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	outputter := mocks.NewMockOutputter(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, outputter, nil)

	outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "tree", "--json", "--socket", testCtlSock).
		Return([]byte("tree failed"), 1, errors.New("exit status 1"))
	if _, err := ctl.Tree(context.Background()); err == nil || !strings.Contains(err.Error(), "tree failed") {
		t.Fatalf("Tree() error = %v, want command output", err)
	}

	outputter.EXPECT().Output(gomock.Any(), []byte(nil), testCtlPath, "tree", "--json", "--socket", testCtlSock).
		Return([]byte(`{"result":{}}`), 0, nil)
	if _, err := ctl.Tree(context.Background()); err == nil || !strings.Contains(err.Error(), "missing result.tree") {
		t.Fatalf("Tree() error = %v, want missing tree error", err)
	}

	outputter.EXPECT().Output(gomock.Any(), []byte("[]"), testCtlPath, "pick", "open", "--prompt", "choose", "--allow-custom", "--socket", testCtlSock).
		Return([]byte("pick failed"), 1, errors.New("exit status 1"))
	if _, err := ctl.Pick(context.Background(), []agterm.PickItem{}, "choose"); err == nil || !strings.Contains(err.Error(), "pick failed") {
		t.Fatalf("Pick() error = %v, want command output", err)
	}
}

func TestCtlClosedRowsSkipsMalformedLinesAndReportsStopFailure(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	streamer := mocks.NewMockStreamer(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, nil, streamer)
	streamer.EXPECT().Stream(gomock.Any(), testCtlPath, "events", "--json", "--kind", "session.closed", "--socket", testCtlSock).
		Return(io.NopCloser(strings.NewReader("not json\n{\"event\":{\"kind\":\"session.closed\",\"session_id\":\"row-2\"}}\n")), func() error {
			return errors.New("stop failed")
		}, nil)
	rows, err := ctl.ClosedRows(context.Background())
	if err != nil {
		t.Fatalf("ClosedRows() error = %v", err)
	}
	var got []string
	for row := range rows {
		got = append(got, row)
	}
	if len(got) != 1 || got[0] != "row-2" {
		t.Fatalf("ClosedRows() rows = %#v, want [row-2]", got)
	}
}

func TestCtlClosedRowsRejectsInvalidStream(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	streamer := mocks.NewMockStreamer(ctrl)
	ctl := agterm.NewCtl(testCtlPath, testCtlSock, nil, nil, streamer)

	streamer.EXPECT().Stream(gomock.Any(), testCtlPath, "events", "--json", "--kind", "session.closed", "--socket", testCtlSock).
		Return(nil, nil, nil)
	if _, err := ctl.ClosedRows(context.Background()); err == nil || !strings.Contains(err.Error(), "no reader") {
		t.Fatalf("ClosedRows() error = %v, want missing-reader error", err)
	}

	streamer.EXPECT().Stream(gomock.Any(), testCtlPath, "events", "--json", "--kind", "session.closed", "--socket", testCtlSock).
		Return(io.NopCloser(strings.NewReader("")), nil, nil)
	if _, err := ctl.ClosedRows(context.Background()); err == nil || !strings.Contains(err.Error(), "no reader") {
		t.Fatalf("ClosedRows() error = %v, want missing-stop error", err)
	}
}

func TestCtlNewUsesDefaultsAndCtlPathIsResolvable(t *testing.T) {
	t.Helper()
	ctl := agterm.NewCtl("", "", nil, nil, nil)
	if ctl == nil {
		t.Fatal("NewCtl() returned nil")
	}
	_ = agterm.CtlPath()
}
