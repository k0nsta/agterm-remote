// Package remote contains the remote-side protocol and its local adapters.
package remote

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/k0nsta/agterm-remote/internal/token"
)

// Session is one agr-owned multiplexer session reported by the remote host.
type Session struct {
	Name     string
	Attached int
	IdleSecs int
	Cmds     string
	State    string
}

// ParseSessions decodes the five-field TSV body returned by the remote
// sessions command. A trailing newline is transport framing, not an empty
// session row.
func ParseSessions(body []byte) ([]Session, error) {
	if len(body) == 0 {
		return []Session{}, nil
	}

	text := strings.TrimSuffix(string(body), "\n")
	lines := strings.Split(text, "\n")
	sessions := make([]Session, 0, len(lines))
	for lineNumber, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			return nil, fmt.Errorf("parse sessions line %d: expected 5 tab-separated fields, got %d", lineNumber+1, len(fields))
		}

		name := fields[0]
		if !token.Valid(name) {
			log.Printf("warning: skipping remote session with invalid name %q on line %d", name, lineNumber+1)
			continue
		}

		attached, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("parse sessions line %d attached count %q: %w", lineNumber+1, fields[1], err)
		}

		idleSecs := -1
		if fields[2] != "-" {
			idleSecs, err = strconv.Atoi(fields[2])
			if err != nil {
				return nil, fmt.Errorf("parse sessions line %d idle seconds %q: %w", lineNumber+1, fields[2], err)
			}
		}

		state := fields[4]
		if state == "-" {
			state = ""
		}

		sessions = append(sessions, Session{
			Name:     name,
			Attached: attached,
			IdleSecs: idleSecs,
			Cmds:     fields[3],
			State:    state,
		})
	}

	return sessions, nil
}

// Remembered is a session the host remembers but that may no longer run: what
// a reboot left behind, which `attach` would restore.
type Remembered struct {
	Name string
	// IdleSecs is how long ago its agent or an attach last touched it.
	IdleSecs int
	Dir      string
	Agent    string
	// DirExists is false when the directory is gone, so it is not restored.
	DirExists bool
}

// ParseRemembered decodes the remote remembered command: name, seconds since
// last active, directory, agent, and 1 when the directory exists.
func ParseRemembered(body []byte) ([]Remembered, error) {
	text := strings.TrimSuffix(string(body), "\n")
	if text == "" {
		return []Remembered{}, nil
	}
	var out []Remembered
	for lineNumber, line := range strings.Split(text, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			return nil, fmt.Errorf("parse remembered line %d: expected 5 tab-separated fields, got %d", lineNumber+1, len(fields))
		}
		if !token.Valid(fields[0]) {
			continue
		}
		idle, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("parse remembered line %d age %q: %w", lineNumber+1, fields[1], err)
		}
		out = append(out, Remembered{Name: fields[0], IdleSecs: idle, Dir: fields[2], Agent: fields[3], DirExists: fields[4] == "1"})
	}
	return out, nil
}
