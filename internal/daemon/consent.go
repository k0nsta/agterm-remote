package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/proxy"
)

// The consent flow (plan: Technical Details > Grants): a host that wants to
// type into a local agent pane it holds no grant for asks the user first.
const (
	// consentTimeout is how long a dialog waits for an answer.
	consentTimeout = 120 * time.Second
	// consentPoll is how often the dialog is read.
	consentPoll = 250 * time.Millisecond
	// consentRefusal is how long a host is refused, without a dialog, for a
	// target row after a deny, a timeout or a client cancel.
	consentRefusal = 60 * time.Second
)

// Button ids of a consent dialog.
const (
	buttonOnce = "once"
	buttonPair = "pair"
	buttonDeny = "deny"
)

type consentOutcome uint8

const (
	outcomeOnce consentOutcome = iota
	outcomePair
	outcomeDeclined
	outcomeTimeout
	outcomeGone
)

// consent asks the user and returns why the call must not run, or "" when it
// may. At most one dialog per host is open, and a refused target row is not
// asked about again for a minute.
func (h *hostCtl) consent(ctx context.Context, req proxy.Request, c *proxy.Consent) string {
	if c == nil {
		return "consent was required but not described"
	}
	key := strings.ToLower(c.Row)
	h.mu.Lock()
	if h.dialog {
		h.mu.Unlock()
		return "a request to the user is already open"
	}
	if until, found := h.refused[key]; found {
		if h.svc.now().Before(until) {
			h.mu.Unlock()
			return "the user declined a request to that row less than a minute ago; try again later"
		}
		delete(h.refused, key)
	}
	h.dialog = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.dialog = false
		h.mu.Unlock()
	}()

	dialog := h.dialogFor(req, c)
	id, err := h.svc.ctl.AskOpen(ctx, dialog)
	switch {
	case errors.Is(err, agterm.ErrAskPending):
		return "the user has a dialog open on that row"
	case err != nil:
		return "cannot ask the user: " + err.Error()
	}
	// The dialog sits on its own row, which the user is often not looking at.
	if err := h.svc.ctl.Notify(ctx, c.Row, dialog.Title, dialog.Message); err != nil {
		h.svc.logf("warning: notify consent dialog for %s: %v", h.host, err)
	}

	switch h.await(ctx, id) {
	case outcomeOnce:
		return h.afterAnswer(ctx, req, c, false)
	case outcomePair:
		return h.afterAnswer(ctx, req, c, true)
	case outcomeTimeout:
		h.refuseRow(key)
		return "no answer from the user"
	case outcomeGone:
		// A client cancel is remembered; a host that is stopping needs no
		// memory.
		if h.ctx.Err() == nil {
			h.refuseRow(key)
		}
		return "the request was cancelled"
	}
	h.refuseRow(key)
	return "the user declined"
}

func (h *hostCtl) refuseRow(key string) {
	h.mu.Lock()
	h.refused[key] = h.svc.now().Add(h.svc.refusalWindow)
	h.mu.Unlock()
}

// afterAnswer re-checks the program rule on a fresh tree read: the answer may
// have come long after Authorize looked, and the pane may be a shell by now.
func (h *hostCtl) afterAnswer(ctx context.Context, req proxy.Request, c *proxy.Consent, pair bool) string {
	if err := h.svc.auth.CheckAgent(ctx, c.Row, c.PaneID); err != nil {
		h.svc.logf("consent re-check for %s: %v", h.host, err)
		return "the pane is no longer running an agent"
	}
	if pair {
		grant := bindings.Grant{Row: c.Row, PaneID: c.PaneID, Kind: bindings.GrantConsent}
		if err := h.svc.store.AddGrant(h.host, req.Session, grant); err != nil {
			// The call was allowed by the user; only the memory of it is lost.
			h.svc.logf("warning: store consent grant for %s/%s: %v", h.host, req.Session, err)
		}
	}
	return ""
}

// await polls the dialog until it is answered, cancelled, times out, or the
// call's context ends. A dialog nobody is waiting for is cancelled, so it
// cannot be clicked after the fact.
func (h *hostCtl) await(ctx context.Context, id string) consentOutcome {
	timeout := time.NewTimer(h.svc.consentTimeout)
	defer timeout.Stop()
	for {
		answer, err := h.svc.ctl.AskResult(ctx, id)
		switch {
		case err != nil && ctx.Err() == nil:
			h.svc.logf("warning: read consent dialog for %s: %v", h.host, err)
		case err == nil && answer.State == agterm.AskCancelled:
			return outcomeDeclined
		case err == nil && answer.State == agterm.AskAnswered:
			switch answer.ID {
			case buttonOnce:
				return outcomeOnce
			case buttonPair:
				return outcomePair
			}
			return outcomeDeclined
		}
		wait := time.NewTimer(h.svc.consentPoll)
		select {
		case <-ctx.Done():
			wait.Stop()
			h.cancelDialog(ctx, id)
			return outcomeGone
		case <-timeout.C:
			wait.Stop()
			h.cancelDialog(ctx, id)
			return outcomeTimeout
		case <-wait.C:
		}
	}
}

func (h *hostCtl) cancelDialog(ctx context.Context, id string) {
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), h.svc.cancelTimeout)
	defer cancel()
	if err := h.svc.ctl.AskCancel(cancelCtx, id); err != nil {
		h.svc.logf("warning: cancel consent dialog %s for %s: %v", id, h.host, err)
	}
}

// dialogFor builds the dialog. Everything from the host or from the row's own
// name is shown with control and invisible characters escaped, and the typed
// text sits between ⟦ ⟧, so it cannot pass for the dialog's own wording.
func (h *hostCtl) dialogFor(req proxy.Request, c *proxy.Consent) agterm.AskRequest {
	shown, total, cut := proxy.PreviewParts(c.Text)
	message := fmt.Sprintf("From session %s on %s: ⟦%s⟧", visible(req.Session), visible(h.host), visible(shown))
	if cut {
		message += fmt.Sprintf("… (%d characters)", total)
	}
	return agterm.AskRequest{
		Title:   fmt.Sprintf("Anything on %s wants to type into “%s”", visible(h.host), visible(c.RowName)),
		Message: message,
		Buttons: []agterm.AskButton{
			{ID: buttonOnce, Label: "Allow once"},
			{ID: buttonPair, Label: fmt.Sprintf("Allow %s here until it closes", visible(h.host))},
			{ID: buttonDeny, Label: "Deny"},
		},
		Default: buttonDeny,
		Row:     c.Row,
		PaneID:  c.PaneID,
	}
}

// visible shows control and format (bidi, zero-width) characters as \u{XXXX}.
func visible(s string) string {
	var out strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			fmt.Fprintf(&out, `\u{%04X}`, r)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
