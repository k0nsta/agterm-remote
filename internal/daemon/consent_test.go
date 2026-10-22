package daemon

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/proxy"
)

// typeInto is a `session type` from a session of the host into a local agent
// pane, the call that needs the user's consent.
func typeInto(t *testing.T, env *proxyEnv, session, row, pane, text string) proxy.Request {
	t.Helper()
	return env.request(t, session, "session", "type", "--target", row, "--pane-id", pane, text)
}

func answer(id string) agterm.AskAnswer {
	return agterm.AskAnswer{State: agterm.AskAnswered, ID: id}
}

func typedCalls(t *testing.T, env *proxyEnv) int {
	t.Helper()
	count := 0
	for _, args := range env.ctl.execArgs(t) {
		if len(args) > 1 && args[0] == "session" && args[1] == "type" {
			count++
		}
	}
	return count
}

func TestConsentOnce(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	env.ctl.results = []agterm.AskAnswer{answer("once")}
	code, frames := env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "hello"))
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q, want the text typed", code, frames.stderr(t))
	}
	want := "session type --target=" + rowAgent + " --pane-id=" + paneAgent + " hello"
	if got := env.ctl.execArgs(t); len(got) != 1 || strings.Join(got[0], " ") != want {
		t.Fatalf("exec = %q, want %q", got, want)
	}
	if caller, _ := env.store.ByHostName(pxHost, "w1"); len(caller.Grants) != 0 {
		t.Fatalf("grants = %+v, want none for Allow once", caller.Grants)
	}
	// Once means once: the next call asks again.
	if code, _ := env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "again")); code != 0 {
		t.Fatalf("second call code = %d, want 0", code)
	}
	if env.ctl.askCount(t) != 2 {
		t.Fatalf("dialogs = %d, want one per call", env.ctl.askCount(t))
	}
}

func TestConsentPairStoresTheGrantAndSkipsLaterDialogs(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	env.ctl.results = []agterm.AskAnswer{answer("pair")}
	if code, frames := env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "one")); code != 0 {
		t.Fatalf("code = %d, stderr = %q, want the text typed", code, frames.stderr(t))
	}
	caller, _ := env.store.ByHostName(pxHost, "w1")
	want := bindings.Grant{Row: rowAgent, PaneID: paneAgent, Kind: bindings.GrantConsent}
	if len(caller.Grants) != 1 || caller.Grants[0] != want {
		t.Fatalf("grants = %+v, want %+v", caller.Grants, want)
	}
	if code, frames := env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "two")); code != 0 {
		t.Fatalf("second call: code = %d, stderr = %q, want it through", code, frames.stderr(t))
	}
	if env.ctl.askCount(t) != 1 || typedCalls(t, env) != 2 {
		t.Fatalf("dialogs = %d, typed = %d, want one dialog and two typed calls", env.ctl.askCount(t), typedCalls(t, env))
	}
}

func TestConsentDeniedAndCancelled(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name   string
		result agterm.AskAnswer
	}{
		{"deny", answer("deny")},
		{"cancelled dialog", agterm.AskAnswer{State: agterm.AskCancelled}},
		{"unknown button", answer("surprise")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newProxyEnv(t)
			env.ctl.results = []agterm.AskAnswer{tc.result}
			code, frames := env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "hello"))
			if code != 1 || !strings.Contains(frames.stderr(t), "the user declined") {
				t.Fatalf("code = %d, stderr = %q, want a refusal", code, frames.stderr(t))
			}
			if typedCalls(t, env) != 0 {
				t.Fatal("text was typed after a refusal")
			}
			// 60 s of refusal for that target row, without a dialog.
			code, frames = env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "again"))
			if code != 1 || !strings.Contains(frames.stderr(t), "less than a minute ago") || env.ctl.askCount(t) != 1 {
				t.Fatalf("retry: code = %d, stderr = %q, dialogs = %d, want a refusal with no new dialog", code, frames.stderr(t), env.ctl.askCount(t))
			}
			// Another row is not affected; the window ends.
			env.ctl.mu.Lock()
			env.ctl.results = []agterm.AskAnswer{answer("once")}
			env.ctl.resultCall = 0
			env.ctl.mu.Unlock()
			if code, _ := env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent2, paneAgnt2, "other")); code != 0 {
				t.Fatalf("another row: code = %d, want it asked and typed", code)
			}
			env.advance(t, 61*time.Second)
			if code, _ := env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "later")); code != 0 {
				t.Fatalf("after the window: code = %d, want a new dialog", code)
			}
		})
	}
}

func TestConsentTimeoutCancelsTheDialog(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	env.d.proxy.consentTimeout = 40 * time.Millisecond
	code, frames := env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "hello"))
	if code != 1 || !strings.Contains(frames.stderr(t), "no answer") {
		t.Fatalf("code = %d, stderr = %q, want a no-answer refusal", code, frames.stderr(t))
	}
	if got := env.ctl.cancelled(t); len(got) != 1 || got[0] != "D1" {
		t.Fatalf("ask cancel calls = %q, want the dialog cancelled", got)
	}
	code, frames = env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "again"))
	if code != 1 || !strings.Contains(frames.stderr(t), "less than a minute ago") || env.ctl.askCount(t) != 1 {
		t.Fatalf("retry: code = %d, stderr = %q, dialogs = %d, want 60 s of refusal", code, frames.stderr(t), env.ctl.askCount(t))
	}
}

func TestConsentOneDialogPerHost(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	first, _ := env.callAsync(t, ctx, typeInto(t, env, "w1", rowAgent, paneAgent, "hello"))
	waitFor(t, "the dialog to open", func() bool { return env.ctl.askCount(t) == 1 })

	for _, tc := range []struct{ session, row, pane string }{
		{"w1", rowAgent2, paneAgnt2}, // the same session, another target
		{"w2", rowAgent, paneAgent},  // another session of the host
		{"w2", rowAgent2, paneAgnt2},
	} {
		code, frames := env.callCtx(t, context.Background(), typeInto(t, env, tc.session, tc.row, tc.pane, "hi"))
		if code != 1 || !strings.Contains(frames.stderr(t), "a request to the user is already open") {
			t.Fatalf("%s -> %s: code = %d, stderr = %q, want the open-request refusal", tc.session, tc.row, code, frames.stderr(t))
		}
	}
	if env.ctl.askCount(t) != 1 {
		t.Fatalf("dialogs = %d, want the one", env.ctl.askCount(t))
	}

	// The client of the open dialog goes away: the dialog is cancelled, nothing
	// is stored or typed, and that target row is refused for a minute.
	cancel()
	if code := receive(t, first, "the canceled call"); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if got := env.ctl.cancelled(t); len(got) != 1 || got[0] != "D1" {
		t.Fatalf("ask cancel calls = %q, want the dialog cancelled", got)
	}
	if caller, _ := env.store.ByHostName(pxHost, "w1"); len(caller.Grants) != 0 || typedCalls(t, env) != 0 {
		t.Fatalf("grants = %+v, typed = %d, want nothing stored or typed", caller.Grants, typedCalls(t, env))
	}
	code, frames := env.callCtx(t, context.Background(), typeInto(t, env, "w2", rowAgent, paneAgent, "hi"))
	if code != 1 || !strings.Contains(frames.stderr(t), "less than a minute ago") || env.ctl.askCount(t) != 1 {
		t.Fatalf("after a client cancel: code = %d, stderr = %q, dialogs = %d, want a refusal with no dialog", code, frames.stderr(t), env.ctl.askCount(t))
	}
	// The flag is free again for another row.
	env.ctl.mu.Lock()
	env.ctl.results = []agterm.AskAnswer{answer("once")}
	env.ctl.resultCall = 0
	env.ctl.mu.Unlock()
	if code, _ := env.callCtx(t, context.Background(), typeInto(t, env, "w2", rowAgent2, paneAgnt2, "hi")); code != 0 {
		t.Fatalf("another row after the cancel: code = %d, want 0", code)
	}
}

func TestConsentHostShutdownCancelsTheDialogWithoutAMemory(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	hostCtx, stopHost := context.WithCancel(context.Background())
	host := env.d.proxy.newHost(pxHost, hostCtx)
	done := make(chan int, 1)
	go func() { done <- host.Ctl(hostCtx, typeInto(t, env, "w1", rowAgent, paneAgent, "hello"), &recFrames{}) }()
	waitFor(t, "the dialog to open", func() bool { return env.ctl.askCount(t) == 1 })
	stopHost()
	receive(t, done, "the call")
	if got := env.ctl.cancelled(t); len(got) != 1 {
		t.Fatalf("ask cancel calls = %q, want the dialog cancelled", got)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.refused) != 0 || host.dialog {
		t.Fatalf("refused = %v, dialog = %v, want a clean state after shutdown", host.refused, host.dialog)
	}
}

func TestConsentRecheckRefusesAPaneThatBecameAShell(t *testing.T) {
	t.Helper()
	for _, button := range []string{"once", "pair"} {
		t.Run(button, func(t *testing.T) {
			env := newProxyEnv(t)
			env.ctl.results = []agterm.AskAnswer{answer(button)}
			// The pane runs the agent when the call is checked, and is a
			// shell by the time the user answers.
			env.ctl.onResult = func(int) {
				env.ctl.setRow(t, rowAgent, func(row *agterm.TreeRow) {
					row.Foreground, row.ForegroundShell = nil, "zsh"
				})
			}
			code, frames := env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "rm -rf ~"))
			if code != 1 || !strings.Contains(frames.stderr(t), "the pane is no longer running an agent") {
				t.Fatalf("code = %d, stderr = %q, want the re-check refusal", code, frames.stderr(t))
			}
			if typedCalls(t, env) != 0 {
				t.Fatal("text was typed into a shell")
			}
			if caller, _ := env.store.ByHostName(pxHost, "w1"); len(caller.Grants) != 0 {
				t.Fatalf("grants = %+v, want none after a failed re-check", caller.Grants)
			}
		})
	}
}

func TestConsentDialogOpenOnTheRowIsRefused(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	env.ctl.askErr = agterm.ErrAskPending
	code, frames := env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "hello"))
	if code != 1 || !strings.Contains(frames.stderr(t), "the user has a dialog open on that row") {
		t.Fatalf("code = %d, stderr = %q, want the busy-row refusal", code, frames.stderr(t))
	}
	env.ctl.mu.Lock()
	env.ctl.askErr = nil
	env.ctl.results = []agterm.AskAnswer{answer("once")}
	env.ctl.mu.Unlock()
	if code, _ := env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "hello")); code != 0 {
		t.Fatalf("retry code = %d, want it through (the busy row is not remembered)", code)
	}
}

func TestConsentNeverAsksForACallThatCannotProceed(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	for _, text := range []string{"esc\x1b[Z", "bidi\u202e"} {
		code, frames := env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, text))
		if code != 1 || env.ctl.askCount(t) != 0 {
			t.Fatalf("%q: code = %d, stderr = %q, dialogs = %d, want a refusal with no dialog", text, code, frames.stderr(t), env.ctl.askCount(t))
		}
	}
	env.ctl.setRow(t, rowAgent, func(row *agterm.TreeRow) { row.Foreground, row.ForegroundShell = nil, "zsh" })
	if code, _ := env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "hi")); code != 1 || env.ctl.askCount(t) != 0 {
		t.Fatalf("shell pane: code = %d, dialogs = %d, want a refusal with no dialog", code, env.ctl.askCount(t))
	}
}

func TestConsentStaleGrantsAreDropped(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	gone := bindings.Grant{Row: pxID + "98", PaneID: pxID + "99", Kind: bindings.GrantOrchestrator}
	live := bindings.Grant{Row: rowAgent, PaneID: paneAgent, Kind: bindings.GrantOrchestrator}
	for _, grant := range []bindings.Grant{gone, live} {
		if err := env.store.AddGrant(pxHost, "w1", grant); err != nil {
			t.Fatalf("AddGrant() error = %v", err)
		}
	}
	if code, _ := env.call(t, "w1", "session", "type", "--target", rowAgent, "--pane-id", paneAgent, "hi"); code != 0 {
		t.Fatalf("typing under a live grant: code = %d, want 0 with no dialog", code)
	}
	caller, _ := env.store.ByHostName(pxHost, "w1")
	if len(caller.Grants) != 1 || caller.Grants[0] != live {
		t.Fatalf("grants = %+v, want only the live one kept", caller.Grants)
	}
	if env.ctl.askCount(t) != 0 {
		t.Fatalf("dialogs = %d, want none under a grant", env.ctl.askCount(t))
	}
}

func TestConsentDialogContent(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	env.ctl.results = []agterm.AskAnswer{answer("deny")}
	env.callCtx(t, context.Background(), typeInto(t, env, "w1", rowAgent, paneAgent, "hello\nthere"))
	env.ctl.mu.Lock()
	request := env.ctl.asks[0]
	env.ctl.mu.Unlock()
	if request.Title != "Anything on homelab wants to type into “orchestrator”" {
		t.Fatalf("Title = %q", request.Title)
	}
	if request.Message != "From session w1 on homelab: ⟦hello⏎there⟧" {
		t.Fatalf("Message = %q", request.Message)
	}
	if request.Row != rowAgent || request.PaneID != paneAgent || request.Default != "deny" {
		t.Fatalf("anchor = %s/%s default %q, want the target pane and a deny default", request.Row, request.PaneID, request.Default)
	}
	want := []agterm.AskButton{{ID: "once", Label: "Allow once"}, {ID: "pair", Label: "Allow homelab here until it closes"}, {ID: "deny", Label: "Deny"}}
	if len(request.Buttons) != 3 || request.Buttons[0] != want[0] || request.Buttons[1] != want[1] || request.Buttons[2] != want[2] {
		t.Fatalf("Buttons = %+v, want %+v", request.Buttons, want)
	}
}

func TestConsentDialogCutsAt300CharactersAndEscapesInvisibles(t *testing.T) {
	t.Helper()
	env := newProxyEnv(t)
	req := env.request(t, "w1")
	text := strings.Repeat("é", 299) + "XY" + "\ttail"
	consent := &proxy.Consent{Row: rowAgent, RowName: "bad\u202ename\x1b[2J\n", PaneID: paneAgent, Text: text}
	request := env.host.dialogFor(req, consent)

	wantShown := strings.Repeat("é", 299) + "X"
	wantMessage := "From session w1 on homelab: ⟦" + wantShown + "⟧… (306 characters)"
	if request.Message != wantMessage {
		t.Fatalf("Message = %q, want %q", request.Message, wantMessage)
	}
	if request.Title != `Anything on homelab wants to type into “bad\u{202E}name\u{001B}[2J\u{000A}”` {
		t.Fatalf("Title = %q, want the row name's invisibles escaped", request.Title)
	}
	for _, field := range []string{request.Title, request.Message} {
		for _, r := range field {
			if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
				t.Fatalf("%q contains the invisible character %U", field, r)
			}
		}
	}
	short := env.host.dialogFor(req, &proxy.Consent{RowName: "r", Text: strings.Repeat("a", 300)})
	if strings.Contains(short.Message, "characters)") {
		t.Fatalf("Message = %q, want no length note at exactly 300 characters", short.Message)
	}
}
