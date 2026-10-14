// Package pasteboard reads the Mac pasteboard and finds recent screenshots.
//
// The command runner is injectable so the source-selection logic can be tested
// without requiring macOS or access to the user's pasteboard.
package pasteboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Runner executes one local command and returns its stdout. It is deliberately
// small: osascript and defaults both communicate their useful result on
// stdout, while their exit status is enough for the screenshot defaults
// fallbacks.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner is the production command runner.
type ExecRunner struct{}

// Run executes a command without a shell and includes stderr in failures.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("nil command context")
	}
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return stdout.Bytes(), ctxErr
		}
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return stdout.Bytes(), fmt.Errorf("%s: %w: %s", name, err, message)
		}
		return stdout.Bytes(), fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), nil
}

// Content describes the first usable pasteboard source. Files take
// precedence over an image because Finder puts both a file URL and an icon
// image on the pasteboard when files are copied.
type Content struct {
	Files []string
	Image bool
}

// Clipboard reads the Mac pasteboard through osascript. A zero Clipboard uses
// ExecRunner; Runner is exposed so callers and tests can provide a seam.
type Clipboard struct {
	Runner Runner
}

// NewClipboard constructs a Clipboard using runner, or the real command
// runner when runner is nil.
func NewClipboard(runner Runner) *Clipboard {
	return &Clipboard{Runner: runner}
}

// Read reads file URLs before image data from the pasteboard. When image data
// exists it is written as PNG to pngPath by the JXA script and Image is true.
func (c Clipboard) Read(ctx context.Context, pngPath string) (Content, error) {
	if ctx == nil {
		return Content{}, errors.New("nil clipboard context")
	}
	if pngPath == "" {
		return Content{}, errors.New("empty clipboard image path")
	}

	runner := c.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	body, err := runner.Run(ctx, "osascript", "-l", "JavaScript", "-e", clipboardScript, pngPath)
	if err != nil {
		return Content{}, fmt.Errorf("read clipboard: %w", err)
	}

	var result struct {
		Files []string `json:"files"`
		Image bool     `json:"image"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &result); err != nil {
		return Content{}, fmt.Errorf("read clipboard: invalid response: %w", err)
	}
	for _, file := range result.Files {
		if err := readableRegularFile(file); err != nil {
			return Content{}, err
		}
	}
	if len(result.Files) > 0 {
		return Content{Files: append([]string(nil), result.Files...)}, nil
	}
	return Content{Image: result.Image}, nil
}

func readableRegularFile(name string) error {
	info, err := os.Stat(name)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", name)
	}
	file, err := os.Open(name)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Screenshots locates recent screenshot files. A zero Screenshots uses
// ExecRunner and the current user's home directory; Home is injectable for
// tests and for callers that already know the home directory.
type Screenshots struct {
	Runner Runner
	Home   string
}

// NewScreenshots constructs a screenshot finder using runner and home. Empty
// values use the real command runner and os.UserHomeDir respectively.
func NewScreenshots(runner Runner, home string) *Screenshots {
	return &Screenshots{Runner: runner, Home: home}
}

// Latest returns the newest regular screenshot file whose extension matches
// the macOS screenshot type and whose modification time is less than maxAge
// before now. A false result means there was no eligible screenshot.
func (s Screenshots) Latest(ctx context.Context, now time.Time, maxAge time.Duration) (string, bool, error) {
	if ctx == nil {
		return "", false, errors.New("nil screenshot context")
	}
	if maxAge <= 0 {
		return "", false, nil
	}

	runner := s.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	home := s.Home
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", false, fmt.Errorf("find screenshot home: %w", err)
		}
	}

	locationOutput, err := runner.Run(ctx, "defaults", "read", "com.apple.screencapture", "location")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", false, ctxErr
		}
		locationOutput = nil
	}
	location := strings.TrimSpace(string(locationOutput))
	if location == "" {
		location = filepath.Join(home, "Desktop")
	} else {
		location = expandHome(location, home)
	}

	typeOutput, err := runner.Run(ctx, "defaults", "read", "com.apple.screencapture", "type")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", false, ctxErr
		}
		typeOutput = nil
	}
	extension := strings.TrimSpace(string(typeOutput))
	if extension == "" {
		extension = "png"
	}
	extension = strings.TrimPrefix(extension, ".")
	if extension == "" || strings.ContainsAny(extension, "/\t\r\n ") {
		return "", false, fmt.Errorf("invalid screenshot type %q", extension)
	}

	entries, err := os.ReadDir(location)
	if err != nil {
		return "", false, fmt.Errorf("read screenshot folder %q: %w", location, err)
	}

	var newest string
	var newestTime time.Time
	for _, entry := range entries {
		if !strings.EqualFold(filepath.Ext(entry.Name()), "."+extension) {
			continue
		}
		path := filepath.Join(location, entry.Name())
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if newest == "" || info.ModTime().After(newestTime) {
			newest = path
			newestTime = info.ModTime()
		}
	}
	if newest == "" {
		return "", false, nil
	}
	age := now.Sub(newestTime)
	if age < 0 || age >= maxAge {
		return "", false, nil
	}
	return newest, true, nil
}

func expandHome(name, home string) string {
	if name == "~" {
		return home
	}
	if strings.HasPrefix(name, "~/") {
		return filepath.Join(home, name[2:])
	}
	return name
}

// SafeName prefixes a paste name with its per-paste index, replaces every
// byte outside the remote token alphabet with '-', and retains the extension
// while limiting the result to 80 bytes. The index prefix also makes repeated
// basenames distinct and prevents a sanitized name from beginning with '.' or
// '-'.
func SafeName(index int, base string) string {
	if index < 0 {
		index = 0
	}
	prefix := strconv.Itoa(index) + "-"
	if base == "" {
		base = "file"
	}
	extension := filepath.Ext(base)
	stem := strings.TrimSuffix(base, extension)
	cleanStem := safeBytes(stem)
	cleanExtension := safeBytes(extension)

	const maxBytes = 80
	available := maxBytes - len(prefix) - len(cleanExtension)
	if available < 0 {
		cleanExtension = cleanExtension[:maxBytes-len(prefix)]
		available = 0
	}
	if len(cleanStem) > available {
		cleanStem = cleanStem[:available]
	}
	result := prefix + cleanStem + cleanExtension
	if result == prefix {
		result += "file"
		if len(result) > maxBytes {
			result = result[:maxBytes]
		}
	}
	return result
}

func safeBytes(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for i := 0; i < len(value); i++ {
		char := value[i]
		if (char >= 'A' && char <= 'Z') ||
			(char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') ||
			char == '.' || char == '_' || char == '-' {
			builder.WriteByte(char)
		} else {
			builder.WriteByte('-')
		}
	}
	return builder.String()
}

const clipboardScript = `
ObjC.import('AppKit')
ObjC.import('Foundation')

function run(argv) {
    const pasteboard = $.NSPasteboard.generalPasteboard
    const files = []
    const items = pasteboard.pasteboardItems
    if (items) {
        for (let i = 0; i < items.count; i++) {
            const item = items.objectAtIndex(i)
            const value = item.stringForType('public.file-url')
            if (!value) {
                continue
            }
            const url = $.NSURL.URLWithString(ObjC.unwrap(value))
            if (url && url.path) {
                files.push(ObjC.unwrap(url.path))
            }
        }
    }
    if (files.length > 0) {
        return JSON.stringify({files: files})
    }

    const path = argv[0]
    let data = pasteboard.dataForType('public.png')
    if (data) {
        data.writeToFileAtomically(path, true)
        return JSON.stringify({image: true})
    }

    data = pasteboard.dataForType('public.tiff')
    if (data) {
        const bitmap = $.NSBitmapImageRep.alloc.initWithData(data)
        const png = bitmap.representationUsingTypeProperties(4, $())
        png.writeToFileAtomically(path, true)
        return JSON.stringify({image: true})
    }
    return '{}'
}
`
