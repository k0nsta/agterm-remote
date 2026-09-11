package agterm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
)

// Ctl adapts agtermctl's command-line interface to the small interfaces used
// by the daemon and CLI. The paths are kept on the adapter so every command
// uses the same explicitly resolved binary and control socket.
type Ctl struct {
	path   string
	sock   string
	run    Runner
	out    Outputter
	stream Streamer
}

// NewCtl constructs an agtermctl adapter. Nil dependencies are replaced by a
// single process runner, which keeps production wiring small while allowing
// tests to inject each command shape independently.
func NewCtl(path, sock string, runner Runner, outputter Outputter, streamer Streamer) *Ctl {
	if path == "" {
		path = CtlPath()
	}
	if sock == "" {
		sock = SocketPath()
	}
	execRunner := &ExecRunner{}
	if runner == nil {
		runner = execRunner
	}
	if outputter == nil {
		outputter = execRunner
	}
	if streamer == nil {
		streamer = execRunner
	}
	return &Ctl{path: path, sock: sock, run: runner, out: outputter, stream: streamer}
}

// HudOpen opens an agterm HUD for a row.
func (c *Ctl) HudOpen(ctx context.Context, row, message string) error {
	return c.run.Run(ctx, c.path, "session", "hud", "open", message, "--target", row, "--socket", c.sock)
}

// HudClose closes an agterm HUD for a row.
func (c *Ctl) HudClose(ctx context.Context, row string) error {
	return c.run.Run(ctx, c.path, "session", "hud", "close", "--target", row, "--socket", c.sock)
}

// Rename changes the display name of an agterm row.
func (c *Ctl) Rename(ctx context.Context, row, name string) error {
	return c.run.Run(ctx, c.path, "session", "rename", name, "--target", row, "--socket", c.sock)
}

// Context sets the purpose/context line for an agterm row. Older agterm
// versions do not have this subcommand, so that specific failure is harmless.
func (c *Ctl) Context(ctx context.Context, row, text string) error {
	err := c.run.Run(ctx, c.path, "session", "context", text, "--target", row, "--socket", c.sock)
	if err != nil && unsupportedCommand(err) {
		log.Printf("warning: agterm session context unavailable: %v", err)
		return nil
	}
	return err
}

// RestoreMode reads agterm's restore mode. The command was added in agterm
// 0.26; older versions report an unknown subcommand and are represented by
// ErrUnsupported for doctor.
func (c *Ctl) RestoreMode(ctx context.Context) (string, error) {
	if c == nil || c.out == nil {
		return "", errors.New("agterm restore-mode client is unavailable")
	}
	out, exit, err := c.out.Output(ctx, nil, c.path, "restore", "mode", "--json", "--socket", c.sock)
	if err != nil || exit != 0 {
		if unsupportedCommand(err) {
			return "", ErrUnsupported
		}
		if err == nil {
			err = fmt.Errorf("command exited with status %d", exit)
		}
		return "", commandError("restore mode", out, exit, err)
	}
	mode, err := decodeRestoreMode(out)
	if err != nil {
		return "", err
	}
	return mode, nil
}

// SupportsContext checks for the agterm 0.26 session context command without
// mutating a row. It uses --help so no target or context text is required.
func (c *Ctl) SupportsContext(ctx context.Context) (bool, error) {
	if c == nil || c.run == nil {
		return false, errors.New("agterm context probe is unavailable")
	}
	err := c.run.Run(ctx, c.path, "session", "context", "--help", "--socket", c.sock)
	if err == nil {
		return true, nil
	}
	if unsupportedCommand(err) {
		return false, ErrUnsupported
	}
	return false, err
}

func unsupportedCommand(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unknown subcommand") ||
		strings.Contains(message, "unknown command")
}

func decodeRestoreMode(out []byte) (string, error) {
	var value any
	if err := json.Unmarshal(bytes.TrimSpace(out), &value); err != nil {
		return "", fmt.Errorf("decode agterm restore mode: %w", err)
	}
	if mode := restoreModeValue(value); mode != "" {
		return mode, nil
	}
	return "", errors.New("decode agterm restore mode: missing mode")
}

func restoreModeValue(value any) string {
	if mode, ok := value.(string); ok {
		return mode
	}
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"mode", "restoreMode"} {
		if mode, ok := object[key].(string); ok {
			return mode
		}
	}
	if result, ok := object["result"]; ok {
		return restoreModeValue(result)
	}
	return ""
}

// Tree returns the ids of all live agterm rows.
func (c *Ctl) Tree(ctx context.Context) ([]string, error) {
	out, exit, err := c.out.Output(ctx, nil, c.path, "tree", "--json", "--socket", c.sock)
	if err != nil || exit != 0 {
		if err == nil {
			err = fmt.Errorf("command exited with status %d", exit)
		}
		return nil, commandError("tree", out, exit, err)
	}

	var response struct {
		Result *struct {
			Tree *struct {
				Workspaces []struct {
					Sessions []struct {
						ID string `json:"id"`
					} `json:"sessions"`
				} `json:"workspaces"`
			} `json:"tree"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		return nil, fmt.Errorf("decode agterm tree: %w", err)
	}
	if response.Result == nil || response.Result.Tree == nil {
		return nil, errors.New("decode agterm tree: missing result.tree")
	}

	rows := make([]string, 0)
	for _, workspace := range response.Result.Tree.Workspaces {
		for _, session := range workspace.Sessions {
			if session.ID != "" {
				rows = append(rows, session.ID)
			}
		}
	}
	return rows, nil
}

// PickItem is one row offered to agterm's native picker.
type PickItem struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Subtitle string `json:"subtitle,omitempty"`
}

// PickResult is the decoded result of agtermctl pick.
type PickResult struct {
	Kind  string
	ID    string
	Query string
}

// ErrCancelled identifies a picker cancellation.
var ErrCancelled = errors.New("agterm picker cancelled")

// Pick opens agterm's picker with the supplied items.
func (c *Ctl) Pick(ctx context.Context, items []PickItem, prompt string) (PickResult, error) {
	stdin, err := json.Marshal(items)
	if err != nil {
		return PickResult{}, fmt.Errorf("encode picker items: %w", err)
	}
	out, exit, runErr := c.out.Output(ctx, stdin, c.path, "pick", "open", "--prompt", prompt, "--allow-custom", "--socket", c.sock)
	if runErr != nil && exit != 2 {
		return PickResult{}, commandError("pick", out, exit, runErr)
	}
	return DecodePick(out, exit)
}

// DecodePick decodes agtermctl's single bare JSON picker result.
func DecodePick(out []byte, exit int) (PickResult, error) {
	if exit == 2 {
		return PickResult{}, ErrCancelled
	}
	if exit != 0 {
		return PickResult{}, fmt.Errorf("agtermctl pick exited with status %d", exit)
	}

	var result struct {
		Kind  string `json:"result"`
		ID    string `json:"id"`
		Query string `json:"query"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &result); err != nil {
		return PickResult{}, fmt.Errorf("decode agterm picker result: %w", err)
	}
	switch result.Kind {
	case "picked":
		if result.ID == "" {
			return PickResult{}, errors.New("agterm picker returned an empty picked id")
		}
		return PickResult{Kind: result.Kind, ID: result.ID}, nil
	case "custom":
		return PickResult{Kind: result.Kind, Query: result.Query}, nil
	case "cancelled":
		return PickResult{}, ErrCancelled
	default:
		return PickResult{}, fmt.Errorf("agterm picker returned unknown result %q", result.Kind)
	}
}

// ClosedRows streams ids from agterm's session.closed event stream.
func (c *Ctl) ClosedRows(ctx context.Context) (<-chan string, error) {
	reader, stop, err := c.stream.Stream(ctx, c.path, "events", "--json", "--kind", "session.closed", "--socket", c.sock)
	if err != nil {
		return nil, err
	}
	if reader == nil || stop == nil {
		return nil, errors.New("agterm events stream returned no reader or stop function")
	}

	rows := make(chan string)
	go func() {
		defer close(rows)
		defer func() {
			if err := stop(); err != nil && ctx.Err() == nil {
				log.Printf("warning: stopping agterm event stream: %v", err)
			}
		}()

		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			row, err := decodeClosedRow(scanner.Bytes())
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("warning: decode agterm session.closed event: %v", err)
				continue
			}
			select {
			case rows <- row:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			log.Printf("warning: read agterm event stream: %v", err)
		}
	}()
	return rows, nil
}

func decodeClosedRow(line []byte) (string, error) {
	var value any
	if err := json.Unmarshal(line, &value); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", errors.New("event is not an object")
	}
	if row, found := findClosedRow(object); found {
		return row, nil
	}
	return "", errors.New("session.closed event has no row id")
}

func findClosedRow(object map[string]any) (string, bool) {
	kind := stringValue(object["kind"])
	if kind == "" {
		kind = stringValue(object["event"])
	}
	if kind == "session.closed" {
		for _, key := range []string{"session_id", "sessionID", "id", "target"} {
			if row := stringValue(object[key]); row != "" {
				return row, true
			}
		}
		if session, ok := object["session"].(map[string]any); ok {
			for _, key := range []string{"id", "session_id"} {
				if row := stringValue(session[key]); row != "" {
					return row, true
				}
			}
		}
	}
	if nested, ok := object["event"].(map[string]any); ok {
		return findClosedRow(nested)
	}
	return "", false
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func commandError(command string, output []byte, exit int, err error) error {
	if len(bytes.TrimSpace(output)) == 0 {
		return fmt.Errorf("agtermctl %s (exit %d): %w", command, exit, err)
	}
	return fmt.Errorf("agtermctl %s (exit %d): %w: %s", command, exit, err, strings.TrimSpace(string(output)))
}
