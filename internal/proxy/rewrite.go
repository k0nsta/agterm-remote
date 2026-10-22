package proxy

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/k0nsta/agterm-remote/internal/token"
)

// maxGeneratedNames bounds the search for a free session name.
const maxGeneratedNames = 1000

// Rewriter turns an authorized Call into what the daemon runs on the Mac
// (plan: Technical Details > Rewrites).
type Rewriter struct {
	// Agr is the stable absolute path of agr, which agterm runs on the Mac.
	Agr string
	// Root is the directory holding one temp directory per call.
	Root     string
	Sessions SessionLister
	Binds    BindingLoader

	mu       sync.Mutex
	reserved map[string]bool
}

// NewRow tells the daemon that a successful `session new` made a row to bind
// to Name on Host before `agr open` has started in it.
type NewRow struct {
	Host string
	Name string
	// Release gives the name's reservation back; the daemon calls it once the
	// row is bound (or the call failed). Safe to call more than once.
	Release func()
}

// Page is an HTML page the daemon serves through the page shim instead of
// handing it to agterm as --html.
type Page struct {
	// File is the page on disk, in Prepared.Dir.
	File string
	// Block is set when the host asked to wait for the page's outcome.
	Block bool
	// Row and Side are the overlay slot the page opens in; Side is "" on a row
	// without a split.
	Row  string
	Side string
}

// Prepared is a rewritten call.
type Prepared struct {
	// Args is the agtermctl argv, without --socket.
	Args  []string
	Stdin []byte
	// Dir holds the call's temp files, "" when there are none. It outlives the
	// call only for a Page, whose lifetime it follows.
	Dir string
	// Warnings are printed on the host's stderr before the call runs.
	Warnings []string
	// NewRow is set for `session new`.
	NewRow *NewRow
	// Page is set for `session overlay open --html`: Args then lacks the
	// page, --js and --block, which the daemon supplies.
	Page *Page
	// Cancel is the argv that undoes a blocking call killed midway.
	Cancel []string
}

// Rewrite prepares call for execution. On error nothing is left on disk.
func (r *Rewriter) Rewrite(ctx context.Context, req Request, call Call) (Prepared, error) {
	prepared, err := r.rewrite(ctx, req, call)
	if err != nil {
		if prepared.Dir != "" {
			_ = os.RemoveAll(prepared.Dir)
		}
		if prepared.NewRow != nil && prepared.NewRow.Release != nil {
			prepared.NewRow.Release()
		}
	}
	return prepared, err
}

func (r *Rewriter) rewrite(ctx context.Context, req Request, call Call) (Prepared, error) {
	prepared := Prepared{}
	if hasOpt(call.Opts, "stdin") {
		prepared.Stdin = req.Stdin
	}
	if call.Block && len(call.Targets) == 1 {
		prepared.Cancel = overlayClose(call.Targets[0])
	}
	if len(call.Files) > 0 {
		dir, files, err := r.writeFiles(req, call)
		prepared.Dir = dir
		if err != nil {
			return prepared, err
		}
		return r.rewriteHTML(prepared, call, files), nil
	}
	switch call.Leaf {
	case "session new":
		return r.rewriteNew(ctx, prepared, req, call)
	case "session overlay open":
		return r.rewriteOverlay(prepared, req, call), nil
	case "session restore":
		return r.rewriteRestore(prepared, req, call)
	}
	prepared.Args = call.Args()
	return prepared, nil
}

func hasOpt(opts []Opt, name string) bool {
	for _, opt := range opts {
		if opt.Name == name {
			return true
		}
	}
	return false
}

func withoutOpts(opts []Opt, names ...string) []Opt {
	kept := make([]Opt, 0, len(opts))
	for _, opt := range opts {
		drop := false
		for _, name := range names {
			drop = drop || opt.Name == name
		}
		if !drop {
			kept = append(kept, opt)
		}
	}
	return kept
}

func overlayClose(target Target) []string {
	args := []string{"session", "overlay", "close", "--target=" + target.Row}
	if target.Side != "" {
		args = append(args, "--pane="+target.Side)
	}
	return args
}

// writeFiles puts the body files in a fresh 0700 directory as <i>.html, 0600.
// A file keeps its body index in its name, so a reload finds the same page.
func (r *Rewriter) writeFiles(req Request, call Call) (dir string, files map[int]string, err error) {
	if err := os.MkdirAll(r.Root, 0o700); err != nil {
		return "", nil, fmt.Errorf("create temp directory: %w", err)
	}
	dir, err = os.MkdirTemp(r.Root, "")
	if err != nil {
		return "", nil, fmt.Errorf("create temp directory: %w", err)
	}
	files = make(map[int]string, len(call.Files))
	for _, ref := range call.Files {
		if ref.Index < 0 || ref.Index >= len(req.Files) {
			return dir, nil, fmt.Errorf("file @%d was not sent", ref.Index)
		}
		name := fmt.Sprintf("%s/%d.html", dir, ref.Index)
		if err := os.WriteFile(name, req.Files[ref.Index], 0o600); err != nil {
			return dir, nil, fmt.Errorf("write temp file: %w", err)
		}
		files[ref.Index] = name
	}
	return dir, files, nil
}

// rewriteHTML hands a body file to the page shim: the option that carried it,
// --js and --block are replaced by the daemon with a URL page.
func (r *Rewriter) rewriteHTML(prepared Prepared, call Call, files map[int]string) Prepared {
	opts := call.Opts
	for _, ref := range call.Files {
		opts = withoutOpts(opts, ref.Opt)
		prepared.Page = &Page{File: files[ref.Index], Block: call.Block}
		if len(call.Targets) == 1 {
			prepared.Page.Row, prepared.Page.Side = call.Targets[0].Row, call.Targets[0].Side
		}
	}
	opts = withoutOpts(opts, "js", "block")
	prepared.Args = call.render(opts, call.Pos)
	return prepared
}

func (r *Rewriter) rewriteOverlay(prepared Prepared, req Request, call Call) Prepared {
	if len(call.Command) > 0 && call.CommandOpt == "" {
		words := []string{r.Agr, "run", req.Host}
		if req.CWD != "" {
			words = append(words, "--cwd", req.CWD)
		}
		words = append(words, "--")
		words = append(words, call.Command...)
		prepared.Args = call.render(call.Opts, []string{Join(words...)})
		return prepared
	}
	for _, opt := range call.Opts {
		if opt.Name == "url" {
			prepared.Warnings = append(prepared.Warnings, localURLWarning(opt.Value, req.Host)...)
		}
	}
	prepared.Args = call.Args()
	return prepared
}

// localURLWarning warns when a URL's host is the machine it is opened on: the
// Mac, not the host the call came from. It is a hint, not a control.
func localURLWarning(raw, host string) []string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	name := strings.ToLower(parsed.Hostname())
	ip := net.ParseIP(name)
	local := name == "localhost" || strings.HasSuffix(name, ".localhost") ||
		(ip != nil && (ip.IsLoopback() || ip.IsUnspecified()))
	if !local {
		return nil
	}
	return []string{fmt.Sprintf("warning: %s opens on the Mac, where localhost is not %s", raw, host)}
}

func (r *Rewriter) rewriteRestore(prepared Prepared, req Request, call Call) (Prepared, error) {
	if len(call.Command) == 0 || len(call.Targets) != 1 {
		prepared.Args = call.Args()
		return prepared, nil
	}
	name, err := r.boundName(req.Host, call.Targets[0].PaneID)
	if err != nil {
		return prepared, err
	}
	command := Join(r.Agr, "open", req.Host, name, "--command", strings.Join(call.Command, " "))
	prepared.Args = call.render(call.Opts, []string{command})
	return prepared, nil
}

// boundName finds the session name the host's pane is bound to.
func (r *Rewriter) boundName(host, paneID string) (string, error) {
	all, err := r.Binds.Load()
	if err != nil {
		return "", fmt.Errorf("cannot read bindings: %w", err)
	}
	for _, b := range all {
		if b.Host == host && strings.EqualFold(b.PaneID, paneID) {
			return b.Name, nil
		}
	}
	return "", fmt.Errorf("pane %s is not bound to %s", paneID, host)
}

func (r *Rewriter) rewriteNew(ctx context.Context, prepared Prepared, req Request, call Call) (Prepared, error) {
	var name, cwd, command string
	for _, opt := range call.Opts {
		switch opt.Name {
		case "name":
			name = opt.Value
		case "cwd":
			cwd = opt.Value
		case "command":
			command = opt.Value
		}
	}
	generated, err := r.generateName(ctx, req, name)
	if err != nil {
		return prepared, err
	}
	words := []string{r.Agr, "open", req.Host, generated}
	if cwd != "" {
		words = append(words, "--cwd", cwd)
	}
	if command != "" {
		words = append(words, "--command", command)
	}
	opts := withoutOpts(call.Opts, "cwd", "command")
	opts = append(opts, Opt{Name: "command", Value: Join(words...), Role: "command"})
	prepared.Args = call.render(opts, call.Pos)
	prepared.NewRow = &NewRow{Host: req.Host, Name: generated, Release: r.releaser(req.Host, generated)}
	return prepared, nil
}

// generateName picks the multiplexer session name of a new row: the requested
// name when it is a valid token that is free, else the requested name or the
// caller's own session name plus -<n>. A name that exists on the host (or is
// bound to it) is never reused: attaching to it would skip the command.
func (r *Rewriter) generateName(ctx context.Context, req Request, wanted string) (string, error) {
	// Selection and reservation are one step, so two concurrent calls never
	// pick the same free name before either is bound.
	r.mu.Lock()
	defer r.mu.Unlock()
	name, err := r.pickName(ctx, req, wanted)
	if err != nil {
		return "", err
	}
	if r.reserved == nil {
		r.reserved = make(map[string]bool)
	}
	r.reserved[reservationKey(req.Host, name)] = true
	return name, nil
}

func reservationKey(host, name string) string { return host + "\x00" + name }

func (r *Rewriter) releaser(host, name string) func() {
	return func() {
		r.mu.Lock()
		delete(r.reserved, reservationKey(host, name))
		r.mu.Unlock()
	}
}

func (r *Rewriter) pickName(ctx context.Context, req Request, wanted string) (string, error) {
	taken := make(map[string]bool)
	sessions, err := r.Sessions.Sessions(ctx, req.Host)
	if err != nil {
		return "", fmt.Errorf("cannot list sessions on %s: %v", req.Host, err)
	}
	for _, session := range sessions {
		taken[session.Name] = true
	}
	all, err := r.Binds.Load()
	if err != nil {
		return "", fmt.Errorf("cannot read bindings: %w", err)
	}
	for _, b := range all {
		if b.Host == req.Host {
			taken[b.Name] = true
		}
	}
	for key := range r.reserved {
		if host, name, ok := strings.Cut(key, "\x00"); ok && host == req.Host {
			taken[name] = true
		}
	}
	base := req.Session
	if token.Valid(wanted) {
		base = wanted
		if !taken[wanted] {
			return wanted, nil
		}
	}
	for n := 2; n < maxGeneratedNames; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if token.Valid(candidate) && !taken[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no free session name on %s", req.Host)
}
