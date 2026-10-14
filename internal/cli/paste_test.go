package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/pasteboard"
	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
)

type pasteClipboardFake struct {
	content   pasteboard.Content
	imageBody []byte
	calls     []string
	err       error
}

func (f *pasteClipboardFake) Read(_ context.Context, pngPath string) (pasteboard.Content, error) {
	f.calls = append(f.calls, pngPath)
	if f.err != nil {
		return pasteboard.Content{}, f.err
	}
	if f.content.Image {
		if err := os.WriteFile(pngPath, f.imageBody, 0o600); err != nil {
			return pasteboard.Content{}, err
		}
	}
	return f.content, nil
}

type pasteScreenshotsFake struct {
	path  string
	found bool
	calls int
	err   error
}

func (f *pasteScreenshotsFake) Latest(context.Context, time.Time, time.Duration) (string, bool, error) {
	f.calls++
	return f.path, f.found, f.err
}

type pasteUploadCall struct {
	host string
	name string
	body []byte
}

type pasteUploaderFake struct {
	calls  []pasteUploadCall
	failAt int
	err    error
}

func (f *pasteUploaderFake) PastePut(_ context.Context, host, name string, body []byte) (string, error) {
	f.calls = append(f.calls, pasteUploadCall{host: host, name: name, body: append([]byte(nil), body...)})
	if f.failAt >= 0 && len(f.calls)-1 == f.failAt {
		return "", f.err
	}
	return "/home/remote/.cache/agr/paste/20261014-" + name, nil
}

type pastePanesFake struct {
	rows  []string
	panes []string
	texts []string
}

func (f *pastePanesFake) Type(_ context.Context, row, paneID, text string) error {
	f.rows = append(f.rows, row)
	f.panes = append(f.panes, paneID)
	f.texts = append(f.texts, text)
	return nil
}

func newPasteStore(t *testing.T, bound ...bindings.Binding) *bindings.Store {
	t.Helper()
	store := bindings.New(pathstest.Dirs(t))
	for _, binding := range bound {
		if err := store.Bind(binding); err != nil {
			t.Fatalf("Bind() error = %v", err)
		}
	}
	return store
}

func TestPasteUsesPaneBindingAndKeepsMultipleBasenamesDistinct(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	first := filepath.Join(dir, "one", "image.png")
	second := filepath.Join(dir, "two", "image.png")
	if err := os.MkdirAll(filepath.Dir(first), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(second), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	clipboard := &pasteClipboardFake{content: pasteboard.Content{Files: []string{first, second}, Image: true}}
	screenshots := &pasteScreenshotsFake{}
	uploader := &pasteUploaderFake{failAt: -1}
	panes := &pastePanesFake{}
	store := newPasteStore(t,
		bindings.Binding{Row: "row-1", PaneID: "L", Pane: "left", Host: "primary-host", Name: "main"},
		bindings.Binding{Row: "row-1", PaneID: "R", Pane: "right", Host: "split-host", Name: "side"},
	)

	err := Paste(context.Background(), "row-1", "R", PasteDependencies{
		Store: store, Clipboard: clipboard, Screenshots: screenshots, Remote: uploader, Panes: panes,
	})
	if err != nil {
		t.Fatalf("Paste() error = %v", err)
	}
	wantCalls := []pasteUploadCall{
		{host: "split-host", name: "0-image.png", body: []byte("one")},
		{host: "split-host", name: "1-image.png", body: []byte("two")},
	}
	if !reflect.DeepEqual(uploader.calls, wantCalls) {
		t.Fatalf("uploads = %#v, want %#v", uploader.calls, wantCalls)
	}
	wantText := "/home/remote/.cache/agr/paste/20261014-0-image.png /home/remote/.cache/agr/paste/20261014-1-image.png "
	if !reflect.DeepEqual(panes.panes, []string{"R"}) || !reflect.DeepEqual(panes.texts, []string{wantText}) {
		t.Fatalf("typed panes/text = %#v/%#v, want R/%q", panes.panes, panes.texts, wantText)
	}
	if screenshots.calls != 0 {
		t.Fatalf("screenshot calls = %d, want files to win over image and screenshot", screenshots.calls)
	}
}

func TestPasteFallsBackToPrimaryForAnUnboundOrEmptyPane(t *testing.T) {
	t.Helper()
	for _, test := range []struct {
		name     string
		paneID   string
		target   string
		wantHost string
	}{
		{name: "unbound scratch", paneID: "S", target: "S", wantHost: "primary-host"},
		{name: "empty pane id", paneID: "", target: "L", wantHost: "primary-host"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Helper()
			dir := t.TempDir()
			file := filepath.Join(dir, "photo.jpg")
			if err := os.WriteFile(file, []byte("photo"), 0o600); err != nil {
				t.Fatal(err)
			}
			clipboard := &pasteClipboardFake{content: pasteboard.Content{Files: []string{file}}}
			uploader := &pasteUploaderFake{failAt: -1}
			panes := &pastePanesFake{}
			store := newPasteStore(t,
				bindings.Binding{Row: "row-1", PaneID: "L", Pane: "left", Host: "primary-host", Name: "main"},
				bindings.Binding{Row: "row-1", PaneID: "R", Pane: "right", Host: "split-host", Name: "side"},
			)
			if err := Paste(context.Background(), "row-1", test.paneID, PasteDependencies{
				Store: store, Clipboard: clipboard, Screenshots: &pasteScreenshotsFake{}, Remote: uploader, Panes: panes,
			}); err != nil {
				t.Fatalf("Paste() error = %v", err)
			}
			if len(uploader.calls) != 1 || uploader.calls[0].host != test.wantHost {
				t.Fatalf("uploads = %#v, want one upload to %s", uploader.calls, test.wantHost)
			}
			if !reflect.DeepEqual(panes.panes, []string{test.target}) {
				t.Fatalf("typed panes = %#v, want %q", panes.panes, test.target)
			}
		})
	}
}

func TestPasteSelectsImageAndScreenshotBranches(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	screenshot := filepath.Join(dir, "Screenshot 2026-10-14.png")
	if err := os.WriteFile(screenshot, []byte("screenshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		content    pasteboard.Content
		screenshot *pasteScreenshotsFake
		wantName   string
		wantBody   string
	}{
		{name: "clipboard image", content: pasteboard.Content{Image: true}, screenshot: &pasteScreenshotsFake{}, wantName: "0-clipboard.png", wantBody: "image bytes"},
		{name: "recent screenshot", content: pasteboard.Content{}, screenshot: &pasteScreenshotsFake{path: screenshot, found: true}, wantName: "0-Screenshot-2026-10-14.png", wantBody: "screenshot"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Helper()
			uploader := &pasteUploaderFake{failAt: -1}
			panes := &pastePanesFake{}
			store := newPasteStore(t, bindings.Binding{Row: "row-1", PaneID: "L", Pane: "left", Host: "host", Name: "main"})
			if err := Paste(context.Background(), "row-1", "L", PasteDependencies{
				Store: store, Clipboard: &pasteClipboardFake{content: test.content, imageBody: []byte("image bytes")},
				Screenshots: test.screenshot, Remote: uploader, Panes: panes,
			}); err != nil {
				t.Fatalf("Paste() error = %v", err)
			}
			if len(uploader.calls) != 1 || uploader.calls[0].name != test.wantName || string(uploader.calls[0].body) != test.wantBody {
				t.Fatalf("upload = %#v, want %s/%q", uploader.calls, test.wantName, test.wantBody)
			}
			if test.name == "clipboard image" && test.screenshot.calls != 0 {
				t.Fatal("image branch consulted the screenshot finder")
			}
		})
	}
}

func TestPasteRejectsEmptyClipboardAndLocalRows(t *testing.T) {
	t.Helper()
	base := PasteDependencies{
		Store: newPasteStore(t), Clipboard: &pasteClipboardFake{}, Screenshots: &pasteScreenshotsFake{},
		Remote: &pasteUploaderFake{failAt: -1}, Panes: &pastePanesFake{},
	}
	err := Paste(context.Background(), "row-1", "", base)
	if err == nil || err.Error() != "not an agr row" {
		t.Fatalf("local row error = %v, want not an agr row", err)
	}
	base.Store = newPasteStore(t, bindings.Binding{Row: "row-1", PaneID: "L", Pane: "left", Host: "host", Name: "main"})
	err = Paste(context.Background(), "row-1", "L", base)
	if err == nil || err.Error() != "nothing to paste: copy an image or files, or take a screenshot" {
		t.Fatalf("empty clipboard error = %v", err)
	}
}

func TestPasteDoesNotTypeWhenAnUploadFailsOrPathIsNotPrintable(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	first := filepath.Join(dir, "first.txt")
	second := filepath.Join(dir, "second.txt")
	if err := os.WriteFile(first, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newPasteStore(t, bindings.Binding{Row: "row-1", PaneID: "L", Pane: "left", Host: "host", Name: "main"})
	for _, test := range []struct {
		name   string
		upload PasteUploader
		want   string
	}{
		{name: "second upload fails", upload: &pasteUploaderFake{failAt: 1, err: errors.New("network down")}, want: "upload 1-second.txt: network down"},
		{name: "remote path is not printable", upload: nonPrintablePasteUploader{}, want: "refusing to type a non-printable remote path"},
		{name: "remote home has a space", upload: spacedPasteUploader{}, want: "contains whitespace"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Helper()
			clipboard := &pasteClipboardFake{content: pasteboard.Content{Files: []string{first, second}}}
			panes := &pastePanesFake{}
			err := Paste(context.Background(), "row-1", "L", PasteDependencies{
				Store: store, Clipboard: clipboard, Screenshots: &pasteScreenshotsFake{}, Remote: test.upload, Panes: panes,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Paste() error = %v, want %q", err, test.want)
			}
			if len(panes.texts) != 0 {
				t.Fatalf("typed = %#v, want no delivery", panes.texts)
			}
		})
	}
}

type nonPrintablePasteUploader struct{}

func (nonPrintablePasteUploader) PastePut(context.Context, string, string, []byte) (string, error) {
	return "/home/remote/.cache/agr/paste/bad\x1bpath", nil
}

type spacedPasteUploader struct{}

func (spacedPasteUploader) PastePut(_ context.Context, _, name string, _ []byte) (string, error) {
	return "/srv/user data/.cache/agr/paste/20261014-" + name, nil
}

func TestPasteRemovesClipboardTempDirectoryAndRejectsOversize(t *testing.T) {
	t.Helper()
	uploader := &pasteUploaderFake{failAt: -1}
	panes := &pastePanesFake{}
	clipboard := &pasteClipboardFake{content: pasteboard.Content{Image: true}, imageBody: []byte("image")}
	store := newPasteStore(t, bindings.Binding{Row: "row-1", PaneID: "L", Pane: "left", Host: "host", Name: "main"})
	if err := Paste(context.Background(), "row-1", "L", PasteDependencies{
		Store: store, Clipboard: clipboard, Screenshots: &pasteScreenshotsFake{}, Remote: uploader, Panes: panes,
	}); err != nil {
		t.Fatalf("image Paste() error = %v", err)
	}
	if len(clipboard.calls) != 1 {
		t.Fatal("clipboard was not read")
	}
	if _, err := os.Stat(filepath.Dir(clipboard.calls[0])); !os.IsNotExist(err) {
		t.Fatalf("clipboard temp directory still exists: stat error = %v", err)
	}

	large := filepath.Join(t.TempDir(), "large.bin")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxPasteBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	uploader.calls = nil
	err = Paste(context.Background(), "row-1", "L", PasteDependencies{
		Store: store, Clipboard: &pasteClipboardFake{content: pasteboard.Content{Files: []string{large}}},
		Screenshots: &pasteScreenshotsFake{}, Remote: uploader, Panes: panes,
	})
	if err == nil || err.Error() != "paste is larger than 50 MB" {
		t.Fatalf("oversize error = %v", err)
	}
	if len(uploader.calls) != 0 {
		t.Fatalf("oversize paste uploaded %#v", uploader.calls)
	}
}
