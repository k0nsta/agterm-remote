package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/pasteboard"
	"github.com/k0nsta/agterm-remote/internal/remote"
	"github.com/k0nsta/agterm-remote/internal/token"
)

const (
	maxPasteBytes      int64 = 50 * 1024 * 1024
	pasteScreenshotAge       = 5 * time.Minute
	pasteUploadTimeout       = 60 * time.Second
)

// PasteDependencies are the collaborators of Paste.
type PasteDependencies struct {
	Store       PasteBindings
	Clipboard   PasteClipboard
	Screenshots PasteScreenshots
	Remote      PasteUploader
	Panes       PastePanes
}

type pasteFile struct {
	name string
	body []byte
}

// Paste selects local paste content, uploads it to the host represented by
// row, and types the resulting remote paths into the requested pane. The
// terminal line is deliberately left unsubmitted so Claude Code can handle
// the path as it would a local drag.
func Paste(ctx context.Context, row, paneID string, deps PasteDependencies) error {
	if ctx == nil {
		return errors.New("nil paste context")
	}
	if !token.Valid(row) {
		return fmt.Errorf("invalid session-id %q", row)
	}
	if deps.Store == nil {
		return errors.New("paste dependencies are unavailable")
	}
	bound, ok := pasteBinding(deps.Store.ForRow(row), paneID)
	if !ok {
		return errors.New("not an agr row")
	}
	if deps.Clipboard == nil || deps.Screenshots == nil || deps.Remote == nil || deps.Panes == nil {
		return errors.New("paste dependencies are unavailable")
	}
	files, err := pasteFiles(ctx, deps)
	if err != nil {
		return err
	}

	paths := make([]string, 0, len(files))
	for i, file := range files {
		name := pasteboard.SafeName(i, file.name)
		uploadCtx, cancel := context.WithTimeout(ctx, pasteUploadTimeout)
		path, err := deps.Remote.PastePut(uploadCtx, bound.Host, name, file.body)
		cancel()
		if err != nil {
			return fmt.Errorf("upload %s: %w", name, err)
		}
		if !remote.Printable(path) {
			return fmt.Errorf("refusing to type a non-printable remote path")
		}
		if strings.ContainsAny(path, " \t") {
			return fmt.Errorf("refusing to type remote path %q: it contains whitespace", path)
		}
		paths = append(paths, path)
	}

	targetPaneID := paneID
	if targetPaneID == "" {
		targetPaneID = bound.PaneID
	}
	line := strings.Join(paths, " ") + " "
	if !remote.Printable(line) {
		return errors.New("refusing to type a non-printable remote path")
	}
	if err := deps.Panes.Type(ctx, row, targetPaneID, line); err != nil {
		return err
	}
	return nil
}

// pasteBinding first honors the pane that fired the key. A scratch or local
// pane has no binding, so it falls back to the row's primary binding.
func pasteBinding(all []bindings.Binding, paneID string) (bindings.Binding, bool) {
	if paneID != "" {
		for _, binding := range all {
			if binding.PaneID == paneID {
				return binding, true
			}
		}
	}
	for _, binding := range all {
		if isPrimaryBinding(binding, "") {
			return binding, true
		}
	}
	return bindings.Binding{}, false
}

func pasteFiles(ctx context.Context, deps PasteDependencies) ([]pasteFile, error) {
	tempDir, err := os.MkdirTemp("", "agr-paste-")
	if err != nil {
		return nil, fmt.Errorf("create paste temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	pngPath := filepath.Join(tempDir, "clipboard.png")
	content, err := deps.Clipboard.Read(ctx, pngPath)
	if err != nil {
		return nil, err
	}
	var sources []struct {
		name string
		path string
	}
	switch {
	case len(content.Files) > 0:
		for _, name := range content.Files {
			sources = append(sources, struct {
				name string
				path string
			}{name: filepath.Base(name), path: name})
		}
	case content.Image:
		sources = append(sources, struct {
			name string
			path string
		}{name: "clipboard.png", path: pngPath})
	default:
		path, found, err := deps.Screenshots.Latest(ctx, time.Now(), pasteScreenshotAge)
		if err != nil {
			return nil, err
		}
		if found {
			sources = append(sources, struct {
				name string
				path string
			}{name: filepath.Base(path), path: path})
		}
	}
	if len(sources) == 0 {
		return nil, errors.New("nothing to paste: copy an image or files, or take a screenshot")
	}

	files := make([]pasteFile, 0, len(sources))
	var total int64
	for _, source := range sources {
		body, err := readPasteFile(source.path, &total)
		if err != nil {
			return nil, err
		}
		files = append(files, pasteFile{name: source.name, body: body})
	}
	return files, nil
}

func readPasteFile(name string, total *int64) ([]byte, error) {
	info, err := os.Stat(name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", name)
	}
	if info.Size() > maxPasteBytes || *total > maxPasteBytes-info.Size() {
		return nil, errors.New("paste is larger than 50 MB")
	}
	body, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if int64(len(body)) > maxPasteBytes-*total {
		return nil, errors.New("paste is larger than 50 MB")
	}
	*total += int64(len(body))
	return body, nil
}

// RunPaste adapts Paste to agr's integer-exit CLI convention.
func RunPaste(ctx context.Context, row, paneID string, deps PasteDependencies, errw io.Writer) int {
	if err := Paste(ctx, row, paneID, deps); err != nil {
		if errw != nil {
			_, _ = fmt.Fprintf(errw, "agr: paste: %v\n", err)
		}
		return 1
	}
	return 0
}
