package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/token"
)

// Protocol version and body limits (plan: Technical Details > Wire).
const (
	Version  = 1
	MaxFiles = 8
	// MaxFilesTotal bounds the bytes of all files together.
	MaxFilesTotal = 8 << 20
	MaxStdin      = 1 << 20
	// frameChunk caps one o/e frame so a large write never becomes a single
	// huge frame.
	frameChunk = 64 << 10
)

// Self is the caller's own AGTERM_* environment as the host saw it. It may be
// stale; the listener's binding is the authority.
type Self struct {
	Row    string `json:"row"`
	PaneID string `json:"pane_id"`
}

// Header is the decoded first line of a ctl call. It carries only what the
// host sent; nothing in it is trusted until the daemon authorizes the call.
type Header struct {
	Session string
	Self    Self
	CWD     string
	Argv    []string
	// Files holds the byte count of each file in the body; no names are sent.
	Files []int
	// Stdin is the byte count of the stdin part of the body.
	Stdin int
}

// Request is a Header after the listener resolved the caller and read the body.
type Request struct {
	Host    string
	Session string
	// Caller is the binding of the pane that made the call.
	Caller bindings.Binding
	Self   Self
	CWD    string
	Argv   []string
	Files  [][]byte
	Stdin  []byte
}

type wireHeader struct {
	Cmd       string          `json:"cmd"`
	V         int             `json:"v"`
	Session   string          `json:"session"`
	SessionID json.RawMessage `json:"session_id"`
	Self      Self            `json:"self"`
	CWD       string          `json:"cwd"`
	Argv      []string        `json:"argv"`
	Files     []int           `json:"files"`
	Stdin     int             `json:"stdin"`
}

// DecodeHeader decodes and validates one ctl header line.
func DecodeHeader(line []byte) (Header, error) {
	var wire wireHeader
	if err := json.Unmarshal(line, &wire); err != nil {
		return Header{}, fmt.Errorf("bad request header: %w", err)
	}
	if wire.Cmd != "ctl" {
		return Header{}, fmt.Errorf("unexpected command %q", wire.Cmd)
	}
	if wire.V != Version {
		return Header{}, fmt.Errorf("unsupported protocol version %d", wire.V)
	}
	if len(wire.SessionID) > 0 {
		return Header{}, errors.New("session_id is not accepted")
	}
	if !token.Valid(wire.Session) {
		return Header{}, fmt.Errorf("invalid session %q", wire.Session)
	}
	if len(wire.Files) > MaxFiles {
		return Header{}, fmt.Errorf("too many files: %d (at most %d)", len(wire.Files), MaxFiles)
	}
	total := 0
	for _, size := range wire.Files {
		if size < 0 || size > MaxFilesTotal {
			return Header{}, fmt.Errorf("invalid file size %d", size)
		}
		total += size
		if total > MaxFilesTotal {
			return Header{}, fmt.Errorf("files exceed %d bytes", MaxFilesTotal)
		}
	}
	if wire.Stdin < 0 || wire.Stdin > MaxStdin {
		return Header{}, fmt.Errorf("invalid stdin size %d (at most %d)", wire.Stdin, MaxStdin)
	}
	return Header{
		Session: wire.Session, Self: wire.Self, CWD: wire.CWD,
		Argv: wire.Argv, Files: wire.Files, Stdin: wire.Stdin,
	}, nil
}

// Request builds the request for a resolved caller; the body is attached by
// the caller of ReadBody.
func (h Header) Request(host string, caller bindings.Binding) Request {
	return Request{
		Host: host, Session: h.Session, Caller: caller,
		Self: h.Self, CWD: h.CWD, Argv: h.Argv,
	}
}

// ReadBody reads the files, then stdin, exactly as the header announced.
func ReadBody(r io.Reader, h Header) (files [][]byte, stdin []byte, err error) {
	for _, size := range h.Files {
		data := make([]byte, size)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, nil, fmt.Errorf("read file: %w", err)
		}
		files = append(files, data)
	}
	stdin = make([]byte, h.Stdin)
	if _, err := io.ReadFull(r, stdin); err != nil {
		return nil, nil, fmt.Errorf("read stdin: %w", err)
	}
	return files, stdin, nil
}

// FrameWriter streams a call's output to the host. Both writers are safe to
// use from separate goroutines.
type FrameWriter interface {
	Stdout() io.Writer
	Stderr() io.Writer
}

// Frames writes `o <n>`, `e <n>` and `x <code>` frames to one connection. A
// mutex keeps frames whole when stdout and stderr are copied concurrently.
type Frames struct {
	mu     sync.Mutex
	w      io.Writer
	exited bool
}

// NewFrames returns a frame writer on w.
func NewFrames(w io.Writer) *Frames { return &Frames{w: w} }

type frameStream struct {
	frames *Frames
	kind   byte
}

func (f *Frames) Stdout() io.Writer { return frameStream{f, 'o'} }
func (f *Frames) Stderr() io.Writer { return frameStream{f, 'e'} }

func (s frameStream) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > frameChunk {
			chunk = chunk[:frameChunk]
		}
		if err := s.frames.frame(s.kind, chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func (f *Frames) frame(kind byte, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exited {
		return errors.New("frames closed")
	}
	buf := append([]byte{kind, ' '}, strconv.Itoa(len(data))...)
	buf = append(buf, '\n')
	buf = append(buf, data...)
	_, err := f.w.Write(buf)
	return err
}

// Exit writes the one `x <code>` frame. Later writes and exits are refused, so
// a call never reports two exit codes.
func (f *Frames) Exit(code int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exited {
		return errors.New("frames closed")
	}
	f.exited = true
	_, err := io.WriteString(f.w, "x "+strconv.Itoa(code)+"\n")
	return err
}

// Refuse writes a refusal: the reason on stderr, then exit code 1.
func (f *Frames) Refuse(reason string) error {
	if err := f.frame('e', []byte("agtermctl (via agr): "+reason+"\n")); err != nil {
		return err
	}
	return f.Exit(1)
}
