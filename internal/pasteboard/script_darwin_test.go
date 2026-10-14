package pasteboard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// fillScript puts one kind of content on a named private pasteboard.
const fillScript = `
ObjC.import('AppKit')
function run(argv) {
    const pb = $.NSPasteboard.pasteboardWithName(argv[0])
    pb.clearContents
    if (argv[1] === 'text') pb.setStringForType($('hello'), 'public.utf8-plain-text')
    if (argv[1] === 'file') pb.setStringForType($.NSURL.fileURLWithPath(argv[2]).absoluteString, 'public.file-url')
    if (argv[1] === 'tiff') {
        const rep = $.NSBitmapImageRep.alloc.initWithBitmapDataPlanesPixelsWidePixelsHighBitsPerSampleSamplesPerPixelHasAlphaIsPlanarColorSpaceNameBytesPerRowBitsPerPixel(null, 2, 2, 8, 4, true, false, 'NSDeviceRGBColorSpace', 0, 0)
        pb.setDataForType(rep.TIFFRepresentation, 'public.tiff')
    }
}
`

const releaseScript = `
ObjC.import('AppKit')
function run(argv) { $.NSPasteboard.pasteboardWithName(argv[0]).releaseGlobally }
`

func runJXA(t *testing.T, script string, args ...string) {
	t.Helper()
	out, err := exec.Command("osascript", append([]string{"-l", "JavaScript", "-e", script}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("osascript: %v: %s", err, out)
	}
}

func TestClipboardScriptOnPrivatePasteboard(t *testing.T) {
	if _, err := exec.LookPath("osascript"); err != nil {
		t.Skip("osascript not available")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	board := fmt.Sprintf("agr-test-%d", os.Getpid())
	t.Cleanup(func() { runJXA(t, releaseScript, board) })

	tests := []struct {
		kind  string
		want  Content
		image bool
	}{
		{kind: "empty", want: Content{}},
		{kind: "text", want: Content{}},
		{kind: "file", want: Content{Files: []string{file}}},
		{kind: "tiff", want: Content{Image: true}, image: true},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			runJXA(t, fillScript, board, tt.kind, file)
			png := filepath.Join(dir, tt.kind+".png")
			got, err := (Clipboard{board: board}).Read(context.Background(), png)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Read() = %#v, want %#v", got, tt.want)
			}
			_, statErr := os.Stat(png)
			if (statErr == nil) != tt.image {
				t.Fatalf("png written = %v, want %v", statErr == nil, tt.image)
			}
		})
	}
}
