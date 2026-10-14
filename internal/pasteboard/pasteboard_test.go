package pasteboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type commandCall struct {
	name string
	args []string
}

type fakeRunner struct {
	calls   []commandCall
	respond func(string, []string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, commandCall{name: name, args: append([]string(nil), args...)})
	if f.respond == nil {
		return nil, nil
	}
	return f.respond(name, args)
}

func TestClipboardReadFilesWinOverImage(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	first := filepath.Join(dir, "one.png")
	second := filepath.Join(dir, "two.txt")
	if err := os.WriteFile(first, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{respond: func(name string, args []string) ([]byte, error) {
		if name != "osascript" || len(args) != 5 || args[0] != "-l" || args[1] != "JavaScript" || args[2] != "-e" {
			t.Fatalf("osascript call = %q %#v", name, args)
		}
		return []byte(fmt.Sprintf(`{"files":[%q,%q],"image":true}`, first, second)), nil
	}}

	got, err := (Clipboard{Runner: runner}).Read(context.Background(), filepath.Join(dir, "image.png"))
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	want := Content{Files: []string{first, second}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read() = %#v, want %#v", got, want)
	}
	if !strings.Contains(runner.calls[0].args[3], "public.file-url") ||
		!strings.Contains(runner.calls[0].args[3], "public.png") ||
		!strings.Contains(runner.calls[0].args[3], "public.tiff") {
		t.Fatalf("clipboard script does not probe files, PNG, and TIFF: %s", runner.calls[0].args[3])
	}
	if !strings.Contains(runner.calls[0].args[3], "representationUsingTypeProperties(4") {
		t.Fatalf("clipboard script does not convert TIFF to PNG: %s", runner.calls[0].args[3])
	}
}

func TestClipboardReadImageAndEmptyResponses(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name string
		body string
		want Content
	}{
		{name: "PNG data", body: `{"image":true}`, want: Content{Image: true}},
		{name: "TIFF converted to PNG", body: `{"image":true}`, want: Content{Image: true}},
		{name: "empty clipboard", body: `{}`, want: Content{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{respond: func(string, []string) ([]byte, error) {
				return []byte(tc.body), nil
			}}
			got, err := (Clipboard{Runner: runner}).Read(context.Background(), filepath.Join(t.TempDir(), "image.png"))
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Read() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestClipboardReadRejectsMalformedJSONAndFolders(t *testing.T) {
	t.Helper()
	t.Run("malformed JSON", func(t *testing.T) {
		runner := &fakeRunner{respond: func(string, []string) ([]byte, error) {
			return []byte("not json"), nil
		}}
		if _, err := (Clipboard{Runner: runner}).Read(context.Background(), filepath.Join(t.TempDir(), "image.png")); err == nil || !strings.Contains(err.Error(), "invalid response") {
			t.Fatalf("Read() error = %v, want invalid response", err)
		}
	})

	t.Run("folder", func(t *testing.T) {
		folder := t.TempDir()
		runner := &fakeRunner{respond: func(string, []string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"files":[%q]}`, folder)), nil
		}}
		if _, err := (Clipboard{Runner: runner}).Read(context.Background(), filepath.Join(t.TempDir(), "image.png")); err == nil || !strings.Contains(err.Error(), folder) || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("Read() error = %v, want named folder refusal", err)
		}
	})

	t.Run("unreadable or missing", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing.png")
		runner := &fakeRunner{respond: func(string, []string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"files":[%q]}`, missing)), nil
		}}
		if _, err := (Clipboard{Runner: runner}).Read(context.Background(), filepath.Join(t.TempDir(), "image.png")); err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("Read() error = %v, want named unreadable file", err)
		}
	})
}

func TestScreenshotsLatestUsesConfiguredLocationAndType(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	location := filepath.Join(home, "Pictures", "Screenshots")
	if err := os.MkdirAll(location, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 14, 18, 53, 1, 0, time.UTC)
	jpg := filepath.Join(location, "Screenshot.jpg")
	mov := filepath.Join(location, "Screenshot.mov")
	if err := os.WriteFile(jpg, []byte("jpg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mov, []byte("movie"), 0o600); err != nil {
		t.Fatal(err)
	}
	setModTime(t, jpg, now.Add(-time.Minute))
	setModTime(t, mov, now.Add(-30*time.Second))
	runner := &fakeRunner{respond: func(name string, args []string) ([]byte, error) {
		if name != "defaults" {
			t.Fatalf("command = %q %#v, want defaults", name, args)
		}
		switch args[len(args)-1] {
		case "location":
			return []byte("~/Pictures/Screenshots\n"), nil
		case "type":
			return []byte("jpg\n"), nil
		default:
			t.Fatalf("unexpected defaults key: %#v", args)
			return nil, nil
		}
	}}

	got, ok, err := (Screenshots{Runner: runner, Home: home}).Latest(context.Background(), now, 5*time.Minute)
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if !ok || got != jpg {
		t.Fatalf("Latest() = (%q, %v), want (%q, true); newer .mov must be ignored", got, ok, jpg)
	}
}

func TestScreenshotsLatestFallsBackToDesktopAndPNG(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	desktop := filepath.Join(home, "Desktop")
	if err := os.MkdirAll(desktop, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 14, 18, 53, 1, 0, time.UTC)
	old := filepath.Join(desktop, "old.png")
	if err := os.WriteFile(old, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	setModTime(t, old, now.Add(-6*time.Minute))
	runner := &fakeRunner{respond: func(name string, args []string) ([]byte, error) {
		if args[len(args)-1] == "location" {
			return nil, errors.New("defaults: exit status 1")
		}
		return nil, errors.New("defaults: exit status 1")
	}}

	got, ok, err := (Screenshots{Runner: runner, Home: home}).Latest(context.Background(), now, 5*time.Minute)
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if ok || got != "" {
		t.Fatalf("Latest() = (%q, %v), want no six-minute-old default PNG", got, ok)
	}
}

func TestSafeName(t *testing.T) {
	t.Helper()
	long := strings.Repeat("a", 200) + ".png"
	for _, tc := range []struct {
		index int
		base  string
		want  string
	}{
		{0, "Screenshot 2026-10-14 at 18.53.01.png", "0-Screenshot-2026-10-14-at-18.53.01.png"},
		{1, "Screenshot 2026-10-14 at 18.53.01\u202fPM.png", "1-Screenshot-2026-10-14-at-18.53.01---PM.png"},
		{2, "../etc", "2-..-etc"},
		{3, ".env", "3-.env"},
		{4, "a'b.txt", "4-a-b.txt"},
		{5, "", "5-file"},
	} {
		if got := SafeName(tc.index, tc.base); got != tc.want {
			t.Errorf("SafeName(%d, %q) = %q, want %q", tc.index, tc.base, got, tc.want)
		}
	}
	got := SafeName(6, long)
	if len(got) != 80 || !strings.HasSuffix(got, ".png") {
		t.Fatalf("SafeName() long result = %q (len %d), want 80 bytes ending in .png", got, len(got))
	}
	if got[0] == '.' || got[0] == '-' {
		t.Fatalf("SafeName() starts with unsafe byte: %q", got)
	}
}

func setModTime(t *testing.T, name string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(name, when, when); err != nil {
		t.Fatal(err)
	}
}
