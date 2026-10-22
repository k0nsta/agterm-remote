package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/proxy"
	"github.com/k0nsta/agterm-remote/internal/receiver"
)

var _ receiver.CtlSink = (*hostCtl)(nil)

const (
	// maxCtlCalls is the most calls in flight per host; the next is refused.
	maxCtlCalls = 16
	// ctlCancelTimeout bounds a cancel hook, which runs after the call's own
	// context is gone.
	ctlCancelTimeout = 2 * time.Second
	// tempMaxAge is how long a page's temp directory may live; the sweep
	// removes older ones.
	tempMaxAge = 24 * time.Hour
	// tempSweepEvery is how often the sweep runs.
	tempSweepEvery = time.Hour
	// prebindAttempts and prebindWait bound the wait for a new row's panes.
	prebindAttempts = 10
	prebindWait     = 100 * time.Millisecond
)

var uuidInText = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// proxyService runs a host's agtermctl calls on the Mac (plan: Solution
// Overview). Its timings are fields so tests can shorten them.
type proxyService struct {
	ctl      CtlBackend
	auth     Authorizer
	rewriter Rewriter
	store    ProxyStore
	// versions reads agterm's version; nil means every agterm is accepted.
	versions versioner
	shim     *pageShim
	// dir is where the calls' temp directories live.
	dir  string
	logf func(format string, args ...any)
	now  func() time.Time

	cancelTimeout  time.Duration
	consentTimeout time.Duration
	consentPoll    time.Duration
	refusalWindow  time.Duration
	pagePoll       time.Duration
	prebindWait    time.Duration
}

func newProxyService(d *Daemon, config Config) *proxyService {
	service := &proxyService{
		ctl: config.Ctl, auth: config.Authorizer, rewriter: config.Rewriter, store: config.ProxyStore,
		dir: d.dirs.Proxy(), logf: d.logf, now: time.Now,

		cancelTimeout: ctlCancelTimeout, consentTimeout: consentTimeout, consentPoll: consentPoll,
		refusalWindow: consentRefusal, pagePoll: pagePollInterval, prebindWait: prebindWait,
	}
	if versions, ok := d.sink.(versioner); ok {
		service.versions = versions
	}
	service.shim = newPageShim(service.pageLive)
	return service
}

// pageLive reports whether agterm lists the page under the row. A row that is
// not in the tree is an error, not "gone": the tree can be limited to the
// frontmost window, and a page must not be dismissed because of that.
func (s *proxyService) pageLive(ctx context.Context, row, pageID string) (bool, error) {
	rows, err := s.ctl.TreeRows(ctx)
	if err != nil {
		return false, err
	}
	for _, candidate := range rows {
		if !strings.EqualFold(candidate.ID, row) {
			continue
		}
		for _, overlay := range candidate.HTMLOverlays {
			if overlay.ID == pageID {
				return true, nil
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("row %s is not in the tree", row)
}

// clearTemp removes every leftover temp directory (daemon start).
func (s *proxyService) clearTemp() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		_ = os.RemoveAll(filepath.Join(s.dir, entry.Name()))
	}
}

// sweepTemp removes temp directories older than tempMaxAge.
func (s *proxyService) sweepTemp() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil && s.now().Sub(info.ModTime()) > tempMaxAge {
			_ = os.RemoveAll(filepath.Join(s.dir, entry.Name()))
		}
	}
}

// startProxy clears leftovers and runs the temp sweep until ctx ends.
func (d *Daemon) startProxy(ctx context.Context, every time.Duration) {
	if d.proxy == nil {
		return
	}
	d.proxy.clearTemp()
	d.children.Add(1)
	go func() {
		defer d.children.Done()
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.proxy.sweepTemp()
			}
		}
	}()
}

// hostCtl is one host's CtlSink. It owns the host's in-flight accounting: at
// most maxCtlCalls calls, and a drain that stopHost waits for.
type hostCtl struct {
	svc  *proxyService
	host string
	// ctx ends when the host is stopped or the daemon closes.
	ctx context.Context

	mu      sync.Mutex
	calls   int
	running int
	closed  bool
	drained chan struct{}
	// dialog is set while a consent dialog of this host is open.
	dialog bool
	// refused maps a lower-cased target row to when its refusal ends.
	refused map[string]time.Time

	verMu   sync.Mutex
	verDone bool
	verMsg  string
}

func (s *proxyService) newHost(host string, ctx context.Context) *hostCtl {
	return &hostCtl{svc: s, host: host, ctx: ctx, refused: make(map[string]time.Time)}
}

// enter counts a call in, or says why it is refused.
func (h *hostCtl) enter() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.closed:
		return "the host is stopping"
	case h.calls >= maxCtlCalls:
		return fmt.Sprintf("too many calls in flight (at most %d per host)", maxCtlCalls)
	}
	h.calls++
	h.running++
	return ""
}

func (h *hostCtl) leave() {
	h.mu.Lock()
	h.calls--
	h.finishLocked()
	h.mu.Unlock()
}

func (h *hostCtl) finishLocked() {
	h.running--
	if h.running == 0 && h.drained != nil {
		close(h.drained)
		h.drained = nil
	}
}

// track runs fn on a goroutine that shutdown waits for. It does not count
// toward the cap. It reports false, running nothing, once the host is closed.
func (h *hostCtl) track(fn func()) bool {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return false
	}
	h.running++
	h.mu.Unlock()
	go func() {
		defer func() {
			h.mu.Lock()
			h.finishLocked()
			h.mu.Unlock()
		}()
		fn()
	}()
	return true
}

// shutdown refuses new calls and waits for the running ones, which end because
// the host's context is canceled.
func (h *hostCtl) shutdown() {
	h.mu.Lock()
	h.closed = true
	if h.running == 0 {
		h.mu.Unlock()
		return
	}
	drained := make(chan struct{})
	h.drained = drained
	h.mu.Unlock()
	<-drained
}

// versionRefusal is why every call is refused on this agterm, or "". The
// version is read once per host start; a failed read is retried by the next
// call.
func (h *hostCtl) versionRefusal(ctx context.Context) string {
	h.verMu.Lock()
	defer h.verMu.Unlock()
	if h.verDone {
		return h.verMsg
	}
	if h.svc.versions == nil {
		h.verDone = true
		return ""
	}
	version, err := h.svc.versions.Version(ctx)
	if err != nil {
		return "cannot read the agterm version: " + err.Error()
	}
	h.verDone = true
	if agterm.VersionLess(version, agterm.MinProxyVersion) {
		h.verMsg = fmt.Sprintf("needs agterm %s (found %s)", agterm.MinProxyVersion, version)
	}
	return h.verMsg
}

func refuse(frames proxy.FrameWriter, reason string) int {
	_, _ = fmt.Fprintf(frames.Stderr(), "agtermctl (via agr): %s\n", reason)
	return 1
}

func headerOf(req proxy.Request) proxy.Header {
	sizes := make([]int, 0, len(req.Files))
	for _, file := range req.Files {
		sizes = append(sizes, len(file))
	}
	return proxy.Header{
		Session: req.Session, Self: req.Self, CWD: req.CWD, Argv: req.Argv,
		Files: sizes, Stdin: len(req.Stdin),
	}
}

// Ctl implements receiver.CtlSink: authorize, rewrite, run.
func (h *hostCtl) Ctl(ctx context.Context, req proxy.Request, frames proxy.FrameWriter) int {
	if why := h.enter(); why != "" {
		return refuse(frames, why)
	}
	defer h.leave()
	if why := h.versionRefusal(ctx); why != "" {
		return refuse(frames, why)
	}
	decision := h.svc.auth.Authorize(ctx, h.host, req.Caller, headerOf(req), req.Argv, req.Stdin)
	for _, grant := range decision.Drop {
		if err := h.svc.store.DropGrant(h.host, req.Session, grant); err != nil {
			h.svc.logf("warning: drop stale grant of %s/%s: %v", h.host, req.Session, err)
		}
	}
	switch decision.Verdict {
	case proxy.Allow:
	case proxy.NeedsConsent:
		if why := h.consent(ctx, req, decision.Consent); why != "" {
			return refuse(frames, why)
		}
	default:
		return refuse(frames, decision.Reason)
	}
	return h.run(ctx, req, decision.Call, frames)
}

func (h *hostCtl) run(ctx context.Context, req proxy.Request, call proxy.Call, frames proxy.FrameWriter) int {
	prepared, err := h.svc.rewriter.Rewrite(ctx, req, call)
	if err != nil {
		return refuse(frames, fmt.Sprintf("%s: %v", call.Leaf, err))
	}
	for _, warning := range prepared.Warnings {
		_, _ = fmt.Fprintf(frames.Stderr(), "agtermctl (via agr): %s\n", warning)
	}
	if prepared.NewRow != nil && prepared.NewRow.Release != nil {
		defer prepared.NewRow.Release()
	}
	if prepared.Page != nil {
		return h.runPage(ctx, prepared, frames)
	}
	if code, handled := h.driveShimPage(ctx, call); handled {
		return code
	}
	if prepared.NewRow != nil {
		return h.runNewRow(ctx, req, prepared, frames)
	}
	return h.exec(ctx, prepared, frames.Stdout(), frames.Stderr())
}

// exec runs the prepared command. When the call's context ends first (the
// client left, or the host is stopping) the process is killed and the cancel
// hook runs on a fresh context.
func (h *hostCtl) exec(ctx context.Context, prepared proxy.Prepared, stdout, stderr io.Writer) int {
	code, err := h.svc.ctl.Exec(ctx, prepared.Stdin, stdout, stderr, prepared.Args...)
	if ctx.Err() != nil {
		h.cancelHook(ctx, prepared.Cancel)
		return 1
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "agtermctl (via agr): %v\n", err)
		return 1
	}
	if code < 0 {
		return 1
	}
	return code
}

// cancelHook runs argv on a context that outlives ctx but not for long.
func (h *hostCtl) cancelHook(ctx context.Context, argv []string) {
	if len(argv) == 0 {
		return
	}
	hookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), h.svc.cancelTimeout)
	defer cancel()
	if _, err := h.svc.ctl.Exec(hookCtx, nil, io.Discard, io.Discard, argv...); err != nil {
		h.svc.logf("warning: cancel hook for %s: %v", h.host, err)
	}
}

// runNewRow runs `session new` and binds the row it made before `agr open`
// has started in it, so the host can address the row at once.
func (h *hostCtl) runNewRow(ctx context.Context, req proxy.Request, prepared proxy.Prepared, frames proxy.FrameWriter) int {
	var out bytes.Buffer
	code := h.exec(ctx, prepared, io.MultiWriter(frames.Stdout(), &out), frames.Stderr())
	if code != 0 {
		return code
	}
	if err := h.preBind(ctx, req, *prepared.NewRow, out.String()); err != nil {
		_, _ = fmt.Fprintf(frames.Stderr(), "agtermctl (via agr): warning: the new row is not bound yet: %v\n", err)
	}
	return 0
}

func (h *hostCtl) preBind(ctx context.Context, req proxy.Request, row proxy.NewRow, output string) error {
	id := uuidInText.FindString(output)
	if id == "" {
		return fmt.Errorf("agtermctl did not report its id")
	}
	var rowID, paneID string
	for attempt := 0; attempt < prebindAttempts && paneID == ""; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(h.svc.prebindWait):
			}
		}
		rows, err := h.svc.ctl.TreeRows(ctx)
		if err != nil {
			continue
		}
		for _, candidate := range rows {
			if !strings.EqualFold(candidate.ID, id) {
				continue
			}
			for _, surface := range candidate.Surfaces {
				if surface.Kind == "left" && surface.PaneID != "" {
					rowID, paneID = candidate.ID, surface.PaneID
				}
			}
		}
	}
	if paneID == "" {
		return fmt.Errorf("row %s has no pane yet", id)
	}
	return h.svc.store.Bind(bindings.Binding{
		Row: rowID, PaneID: paneID, Pane: "left", Host: row.Host, Name: row.Name,
		Mux: req.Caller.Mux, BoundAt: h.svc.now().UTC(),
	})
}

// driveShimPage handles `session overlay submit|close` on a slot that holds a
// shim page: it drives the page's one transition instead of acting on
// whatever agterm shows there.
func (h *hostCtl) driveShimPage(ctx context.Context, call proxy.Call) (code int, handled bool) {
	if (call.Leaf != "session overlay submit" && call.Leaf != "session overlay close") || len(call.Targets) != 1 {
		return 0, false
	}
	target := call.Targets[0]
	p := h.svc.shim.inSlot(h.host, target.Row, target.Side)
	if p == nil {
		return 0, false
	}
	state, value := pageDismissed, ""
	if call.Leaf == "session overlay submit" {
		state = pageSubmitted
		for _, opt := range call.Opts {
			if opt.Name == "value" {
				value = opt.Value
			}
		}
	}
	if p.transition(state, value) {
		h.closePageOverlay(ctx, p)
	}
	return 0, true
}

// pageResult is the part of agtermctl's reply to an overlay open the daemon
// reads.
type pageResult struct {
	Result struct {
		PageID string `json:"pageID"`
	} `json:"result"`
}

// runPage opens a host's HTML page as a URL page through the shim.
func (h *hostCtl) runPage(ctx context.Context, prepared proxy.Prepared, frames proxy.FrameWriter) int {
	page := prepared.Page
	p, url, err := h.svc.shim.register(h.host, page.Row, page.Side, page.File, prepared.Dir)
	if err != nil {
		removeDir(prepared.Dir)
		return refuse(frames, "session overlay open: "+err.Error())
	}
	// --json gives the page id; the reply goes to the host as agterm sent it.
	args := append(append([]string(nil), prepared.Args...), "--url="+url, "--js")
	if !containsArg(args, "--json") {
		args = append(args, "--json")
	}
	var out bytes.Buffer
	opened := prepared
	opened.Args = args
	if code := h.exec(ctx, opened, &out, frames.Stderr()); code != 0 || ctx.Err() != nil {
		h.svc.shim.release(p)
		_, _ = frames.Stdout().Write(out.Bytes())
		return max(code, 1)
	}
	var reply pageResult
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &reply); err != nil || reply.Result.PageID == "" {
		h.svc.shim.release(p)
		return refuse(frames, "session overlay open: agterm did not report a page id")
	}
	p.bind(reply.Result.PageID)

	if !page.Block {
		_, _ = frames.Stdout().Write(out.Bytes())
		if !h.track(func() { h.watchPage(p) }) {
			h.svc.shim.release(p)
		}
		return 0
	}
	if !h.svc.shim.wait(ctx, p, h.svc.pagePoll) {
		// The client left or the host is stopping: close what it opened.
		h.cancelHook(ctx, prepared.Cancel)
		h.svc.shim.release(p)
		return 1
	}
	h.closePageOverlay(ctx, p)
	body, exit := p.outcome()
	h.svc.shim.release(p)
	_, _ = frames.Stdout().Write(body)
	return exit
}

// watchPage ends a non-blocking page: when it submits, closes or leaves the
// overlay slots, or the host stops, its token and temp directory go.
func (h *hostCtl) watchPage(p *page) {
	defer h.svc.shim.release(p)
	if !h.svc.shim.wait(h.ctx, p, h.svc.pagePoll) {
		return
	}
	h.closePageOverlay(h.ctx, p)
}

// closePageOverlay closes the overlay once the page is done, but only while
// agterm still shows that page: a page that was replaced in the slot is not
// the daemon's to close.
func (h *hostCtl) closePageOverlay(ctx context.Context, p *page) {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), h.svc.cancelTimeout)
	defer cancel()
	if live, err := h.svc.pageLive(closeCtx, p.row, p.id()); err != nil || !live {
		return
	}
	args := []string{"session", "overlay", "close", "--target=" + p.row}
	if p.side != "" {
		args = append(args, "--pane="+p.side)
	}
	if _, err := h.svc.ctl.Exec(closeCtx, nil, io.Discard, io.Discard, args...); err != nil {
		h.svc.logf("warning: close page overlay for %s: %v", h.host, err)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
