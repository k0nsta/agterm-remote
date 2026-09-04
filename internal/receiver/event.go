// Package receiver accepts status events arriving from a remote agr bridge.
package receiver

import (
	"encoding/json"
	"fmt"

	"github.com/k0nsta/agterm-remote/internal/token"
)

// Event is a decoded status event. Session is populated for agr events and
// SessionID is populated for cookbook events, whose session_id is already the
// agterm row target.
type Event struct {
	Host      string
	Session   string
	SessionID string
	State     string
	Pane      string
	PaneID    string
	Blink     bool
	AutoReset bool
}

type wireEvent struct {
	Cmd       string   `json:"cmd"`
	Session   string   `json:"session"`
	SessionID string   `json:"session_id"`
	State     string   `json:"state"`
	Pane      string   `json:"pane"`
	PaneID    string   `json:"pane_id"`
	Args      []string `json:"args"`
}

// Decode validates and decodes one line from either agr's session-based
// format or the cookbook's row-based format.
func Decode(host string, line []byte) (Event, error) {
	var wire wireEvent
	if err := json.Unmarshal(line, &wire); err != nil {
		return Event{}, fmt.Errorf("decode receiver event: %w", err)
	}
	if wire.Cmd != "session-status" {
		return Event{}, fmt.Errorf("unknown receiver event command %q", wire.Cmd)
	}
	if !token.ValidState(wire.State) {
		return Event{}, fmt.Errorf("invalid receiver event state %q", wire.State)
	}

	event := Event{
		Host:      host,
		State:     wire.State,
		Pane:      wire.Pane,
		PaneID:    wire.PaneID,
		Blink:     hasArg(wire.Args, "--blink"),
		AutoReset: hasArg(wire.Args, "--auto-reset"),
	}
	switch {
	case wire.Session != "" && wire.SessionID == "":
		if !token.Valid(wire.Session) {
			return Event{}, fmt.Errorf("invalid receiver event session %q", wire.Session)
		}
		event.Session = wire.Session
	case wire.Session == "" && wire.SessionID != "":
		if !token.Valid(wire.SessionID) {
			return Event{}, fmt.Errorf("invalid receiver event session_id %q", wire.SessionID)
		}
		event.SessionID = wire.SessionID
	case wire.Session != "" && wire.SessionID != "":
		return Event{}, fmt.Errorf("receiver event has both session and session_id")
	default:
		return Event{}, fmt.Errorf("receiver event has neither session nor session_id")
	}
	return event, nil
}

func hasArg(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}
