package proxy

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/bindings"
)

func TestDecodeHeader(t *testing.T) {
	t.Helper()
	tests := []struct {
		name    string
		line    string
		wantErr string
	}{
		{name: "valid", line: `{"cmd":"ctl","v":1,"session":"api","self":{"row":"r","pane_id":"p"},"cwd":"/x","argv":["tree"],"files":[3,4],"stdin":5}`},
		{name: "minimal", line: `{"cmd":"ctl","v":1,"session":"api"}`},
		{name: "session_id refused", line: `{"cmd":"ctl","v":1,"session":"api","session_id":"row"}`, wantErr: "session_id"},
		{name: "session_id null refused", line: `{"cmd":"ctl","v":1,"session":"api","session_id":null}`, wantErr: "session_id"},
		{name: "bad session", line: `{"cmd":"ctl","v":1,"session":"a b"}`, wantErr: "invalid session"},
		{name: "no session", line: `{"cmd":"ctl","v":1}`, wantErr: "invalid session"},
		{name: "version", line: `{"cmd":"ctl","v":2,"session":"api"}`, wantErr: "version"},
		{name: "not json", line: `nope`, wantErr: "bad request header"},
		{name: "nine files", line: `{"cmd":"ctl","v":1,"session":"api","files":[1,1,1,1,1,1,1,1,1]}`, wantErr: "too many files"},
		{name: "files over total", line: `{"cmd":"ctl","v":1,"session":"api","files":[8000000,8000000]}`, wantErr: "exceed"},
		{name: "negative file", line: `{"cmd":"ctl","v":1,"session":"api","files":[-1]}`, wantErr: "invalid file size"},
		{name: "stdin over limit", line: `{"cmd":"ctl","v":1,"session":"api","stdin":1048577}`, wantErr: "stdin"},
		{name: "stdin at limit", line: `{"cmd":"ctl","v":1,"session":"api","stdin":1048576}`},
		{name: "negative stdin", line: `{"cmd":"ctl","v":1,"session":"api","stdin":-1}`, wantErr: "stdin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Helper()
			header, err := DecodeHeader([]byte(test.line))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("DecodeHeader() error = %v", err)
				}
				if header.Session != "api" {
					t.Fatalf("Session = %q, want api", header.Session)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("DecodeHeader() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestHeaderRequestAndReadBody(t *testing.T) {
	t.Helper()
	header, err := DecodeHeader([]byte(`{"cmd":"ctl","v":1,"session":"api","self":{"row":"r","pane_id":"p"},"cwd":"/x","argv":["tree"],"files":[2,3],"stdin":4}`))
	if err != nil {
		t.Fatalf("DecodeHeader() error = %v", err)
	}
	req := header.Request("host-a", bindings.Binding{Row: "row-1"})
	if req.Host != "host-a" || req.Session != "api" || req.Caller.Row != "row-1" || req.Self.PaneID != "p" || req.CWD != "/x" || len(req.Argv) != 1 {
		t.Fatalf("Request = %#v", req)
	}
	files, stdin, err := ReadBody(strings.NewReader("aabbbcccc"), header)
	if err != nil {
		t.Fatalf("ReadBody() error = %v", err)
	}
	if len(files) != 2 || string(files[0]) != "aa" || string(files[1]) != "bbb" || string(stdin) != "cccc" {
		t.Fatalf("ReadBody() = %q, %q", files, stdin)
	}
	if _, _, err := ReadBody(strings.NewReader("aabb"), header); err == nil {
		t.Fatal("ReadBody() on a short body error = nil")
	}
}

func TestFramesWriteWholeFrames(t *testing.T) {
	t.Helper()
	var out bytes.Buffer
	frames := NewFrames(&out)
	_, _ = frames.Stdout().Write([]byte("hello"))
	_, _ = frames.Stderr().Write([]byte("oops\n"))
	_, _ = frames.Stdout().Write(nil)
	if err := frames.Exit(3); err != nil {
		t.Fatalf("Exit() error = %v", err)
	}
	if want := "o 5\nhelloe 5\noops\nx 3\n"; out.String() != want {
		t.Fatalf("frames = %q, want %q", out.String(), want)
	}
	if _, err := frames.Stdout().Write([]byte("late")); err == nil {
		t.Fatal("write after exit error = nil")
	}
	if err := frames.Exit(0); err == nil {
		t.Fatal("second Exit() error = nil")
	}
}

func TestFramesSplitLargeWrites(t *testing.T) {
	t.Helper()
	var out bytes.Buffer
	frames := NewFrames(&out)
	n, err := frames.Stdout().Write(bytes.Repeat([]byte("a"), frameChunk+1))
	if err != nil || n != frameChunk+1 {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	if got := strings.Count(out.String(), "o "); got != 2 {
		t.Fatalf("frame count = %d, want 2", got)
	}
}

func TestFramesConcurrentWritersKeepFramesIntact(t *testing.T) {
	t.Helper()
	var out bytes.Buffer
	frames := NewFrames(&out)
	var wg sync.WaitGroup
	for _, stream := range []struct {
		kind string
		w    io.Writer
	}{
		{"o", frames.Stdout()},
		{"e", frames.Stderr()},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				_, _ = stream.w.Write([]byte(strings.Repeat(stream.kind, 100)))
			}
		}()
	}
	wg.Wait()
	rest := out.String()
	for count := 0; rest != ""; count++ {
		want := rest[:1] + " 100\n" + strings.Repeat(rest[:1], 100)
		if !strings.HasPrefix(rest, want) {
			t.Fatalf("frame %d is torn: %q", count, rest[:min(len(rest), 40)])
		}
		rest = rest[len(want):]
	}
}

func TestFramesRefuse(t *testing.T) {
	t.Helper()
	var out bytes.Buffer
	if err := NewFrames(&out).Refuse("nope"); err != nil {
		t.Fatalf("Refuse() error = %v", err)
	}
	reason := "agtermctl (via agr): nope\n"
	if want := "e 26\n" + reason + "x 1\n"; len(reason) != 26 || out.String() != want {
		t.Fatalf("Refuse() = %q, want %q", out.String(), want)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("broken") }

func TestFramesPropagateWriteError(t *testing.T) {
	t.Helper()
	if _, err := NewFrames(failWriter{}).Stdout().Write([]byte("x")); err == nil {
		t.Fatal("Write() error = nil")
	}
}
