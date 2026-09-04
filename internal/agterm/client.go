package agterm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// MinTestedVersion is the oldest agterm version whose control behavior agr
	// has been tested against.
	MinTestedVersion = "0.25.0"
	maxControlLine   = 1 << 20
)

// Client speaks one request/response exchange per Unix socket connection.
type Client struct {
	sock    string
	ctl     string
	timeout time.Duration
	run     Runner
}

// NewClient constructs a client with explicit paths and dependencies. An
// empty ctl path is replaced with the process-wide CtlPath resolution.
func NewClient(sock, ctl string, timeout time.Duration, runner Runner) *Client {
	if ctl == "" {
		ctl = CtlPath()
	}
	return &Client{sock: sock, ctl: ctl, timeout: timeout, run: runner}
}

// Do sends one request and reads one response line.
func (c *Client) Do(ctx context.Context, request Request) (Response, error) {
	if c == nil {
		return Response{}, errors.New("nil agterm client")
	}
	requestContext, cancel := c.requestContext(ctx)
	defer cancel()

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(requestContext, "unix", c.sock)
	if err != nil {
		return Response{}, contextError(requestContext, err)
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := requestContext.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stopOnCancel := make(chan struct{})
	go func() {
		select {
		case <-requestContext.Done():
			_ = conn.Close()
		case <-stopOnCancel:
		}
	}()
	defer close(stopOnCancel)

	payload, err := json.Marshal(request)
	if err != nil {
		return Response{}, err
	}
	payload = append(payload, '\n')
	if err := writeAll(conn, payload); err != nil {
		return Response{}, contextError(requestContext, err)
	}

	line, err := readControlLine(bufio.NewReader(conn))
	if err != nil {
		return Response{}, contextError(requestContext, err)
	}
	var response Response
	if err := json.Unmarshal(line, &response); err != nil {
		return Response{}, fmt.Errorf("decode agterm response: %w", err)
	}
	return response, nil
}

// Version performs the agterm version handshake and returns the app version.
func (c *Client) Version(ctx context.Context) (string, error) {
	response, err := c.Do(ctx, Request{Cmd: "version"})
	if err != nil {
		return "", err
	}
	if !response.OK {
		return "", responseError(response.Error)
	}

	var result struct {
		App struct {
			Version string `json:"version"`
		} `json:"app"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return "", fmt.Errorf("decode agterm version: %w", err)
	}
	if result.App.Version == "" {
		return "", errors.New("agterm version response has no app.version")
	}
	if versionLess(result.App.Version, MinTestedVersion) {
		versionWarningOnce.Do(func() {
			log.Printf("warning: agterm %s is older than the tested minimum %s", result.App.Version, MinTestedVersion)
		})
	}
	return result.App.Version, nil
}

// Status pushes one status update. A decode-failure response is retried via
// agtermctl so a transient protocol mismatch cannot drop an agent event.
func (c *Client) Status(ctx context.Context, target string, args StatusArgs) error {
	response, err := c.Do(ctx, Request{Cmd: "session.status", Target: target, Args: args})
	if err != nil {
		return err
	}
	if response.OK {
		return nil
	}

	if IsDecodeFailure(errors.New(response.Error)) {
		fallbackWarningOnce.Do(func() {
			log.Printf("warning: falling back to agtermctl for session.status")
		})
		if c.run == nil {
			return errors.New("agtermctl fallback is unavailable")
		}
		fallbackArgs := []string{"session", "status", args.Status, "--target", target, "--socket", c.sock}
		if args.Pane != "" {
			fallbackArgs = append(fallbackArgs, "--pane", args.Pane)
		}
		if args.PaneID != "" {
			fallbackArgs = append(fallbackArgs, "--pane-id", args.PaneID)
		}
		if args.Blink != nil && *args.Blink {
			fallbackArgs = append(fallbackArgs, "--blink")
		}
		if args.AutoReset != nil && *args.AutoReset {
			fallbackArgs = append(fallbackArgs, "--auto-reset")
		}
		return c.run.Run(ctx, c.ctl, fallbackArgs...)
	}
	return responseError(response.Error)
}

var (
	fallbackWarningOnce sync.Once
	versionWarningOnce  sync.Once
)

func (c *Client) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.timeout)
}

func contextError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return err
}

func writeAll(w io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := w.Write(payload)
		if written > 0 {
			payload = payload[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func readControlLine(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 4096)
	for {
		part, err := reader.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > maxControlLine {
			return nil, errors.New("agterm response exceeds 1 MiB")
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == nil {
			return bytesTrimSpace(line), nil
		}
		if errors.Is(err, io.EOF) && len(line) > 0 {
			return bytesTrimSpace(line), nil
		}
		return nil, err
	}
}

func bytesTrimSpace(value []byte) []byte {
	return bytes.TrimSpace(value)
}

func responseError(message string) error {
	if strings.HasPrefix(message, ErrRefused.Error()) {
		return fmt.Errorf("%w: %s", ErrRefused, message)
	}
	if strings.HasPrefix(message, ErrUnknownTarget.Error()) {
		return fmt.Errorf("%w %s", ErrUnknownTarget, strings.TrimSpace(strings.TrimPrefix(message, ErrUnknownTarget.Error())))
	}
	if message == "" {
		return errors.New("agterm request failed without an error message")
	}
	return errors.New(message)
}

func versionLess(got, minimum string) bool {
	gotParts, gotOK := versionParts(got)
	minParts, minOK := versionParts(minimum)
	if !gotOK || !minOK {
		return false
	}
	for i := range minParts {
		if gotParts[i] != minParts[i] {
			return gotParts[i] < minParts[i]
		}
	}
	return false
}

func versionParts(value string) ([3]int, bool) {
	var parts [3]int
	value = strings.TrimPrefix(value, "v")
	chunks := strings.SplitN(value, ".", 4)
	if len(chunks) < 3 {
		return parts, false
	}
	for i := 0; i < 3; i++ {
		chunk := chunks[i]
		if i == 2 {
			chunk = strings.SplitN(chunk, "-", 2)[0]
		}
		number, err := strconv.Atoi(chunk)
		if err != nil || number < 0 {
			return parts, false
		}
		parts[i] = number
	}
	return parts, true
}
