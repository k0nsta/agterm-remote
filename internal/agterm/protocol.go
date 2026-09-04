// Package agterm contains the client for agterm's local control protocol.
package agterm

import (
	"encoding/json"
	"errors"
	"strings"
)

// Request is one newline-delimited agterm control request.
type Request struct {
	Cmd    string `json:"cmd"`
	Target string `json:"target,omitempty"`
	Args   any    `json:"args,omitempty"`
}

// StatusArgs are the optional arguments accepted by session.status.
type StatusArgs struct {
	Status    string `json:"status"`
	Blink     *bool  `json:"blink,omitempty"`
	AutoReset *bool  `json:"autoReset,omitempty"`
	Pane      string `json:"pane,omitempty"`
	PaneID    string `json:"paneID,omitempty"`
}

// Response is one newline-delimited agterm control response.
type Response struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

var (
	// ErrRefused identifies agterm's pane-ownership refusal for a status push.
	ErrRefused = errors.New("blocked status owned by pane")
	// ErrUnknownTarget identifies a row that agterm no longer knows.
	ErrUnknownTarget error = errorText("no such session:")
	// ErrUnsupported identifies a command that is not available in this agterm
	// version. Doctor renders this as n/a rather than as a broken check.
	ErrUnsupported = errors.New("agterm command unsupported")
)

type errorText string

func (e errorText) Error() string { return string(e) }

// IsDecodeFailure reports whether an agterm error asks the caller to retry via
// agtermctl. Both spellings have appeared in agterm responses.
func IsDecodeFailure(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "invalid request") ||
		strings.Contains(message, "decodingerror")
}
