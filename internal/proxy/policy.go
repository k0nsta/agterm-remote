package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
)

// MaxTypedText bounds text typed into a pane the host does not own.
const MaxTypedText = 64 << 10

// previewRunes is how much of the typed text a consent preview carries.
const previewRunes = 300

// DefaultAgents are the program names `session type` may reach in a local pane.
var DefaultAgents = []string{"claude", "codex"}

type scope uint8

const (
	// scopeNone calls name no row (version, tree, events, session new).
	scopeNone scope = iota
	// scopeCaller calls act on the caller's own row only (notify).
	scopeCaller
	// scopePane calls act on one pane.
	scopePane
	// scopeRow calls act on whole rows: every pane must be the host's.
	scopeRow
)

type paneBy uint8

const (
	// byID passes the pane to agtermctl as --pane-id.
	byID paneBy = iota
	// bySide passes it as --pane left|right (overlays take no pane id).
	bySide
)

// leafSpec is one allowlisted agtermctl leaf with its option schema.
type leafSpec struct {
	path  []string
	scope scope
	// write leaves need the pane attached; reads need it bound only.
	write bool
	by    paneBy
	opts  []optSpec
	// maxPos is the most positionals allowed; -1 is unlimited.
	maxPos int
	minPos int
	// posAllowed limits the first positional when set.
	posAllowed []string
	// posCommand makes the positionals a command that is rewritten.
	posCommand bool
	// typing marks `session type`, the one leaf that may reach a local pane.
	typing bool
	// check validates a parsed call beyond the schema.
	check func(args parsedArgs) error
}

func (l *leafSpec) name() string { return strings.Join(l.path, " ") }

func (l *leafSpec) option(name string) (optSpec, bool) {
	for _, spec := range l.opts {
		if spec.name == name {
			return spec, true
		}
	}
	for _, spec := range universalOpts {
		if spec.name == name {
			return spec, true
		}
	}
	return optSpec{}, false
}

func words(s string) []string { return strings.Fields(s) }

var (
	optTarget = optSpec{name: "target", value: true, role: roleRow}
	optPaneID = optSpec{name: "pane-id", value: true, role: rolePane}
	optPane   = optSpec{name: "pane", value: true, role: roleSide, allowed: []string{"left", "right", "primary", "split"}}
	optBlock  = optSpec{name: "block"}
)

func plainValues(names ...string) []optSpec {
	specs := make([]optSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, optSpec{name: name, value: true})
	}
	return specs
}

func plainFlags(names ...string) []optSpec {
	specs := make([]optSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, optSpec{name: name})
	}
	return specs
}

func join(groups ...[]optSpec) []optSpec {
	var all []optSpec
	for _, group := range groups {
		all = append(all, group...)
	}
	return all
}

// hudOpts are the message options of `session hud`; --file is not among them.
var hudOpts = join(
	plainValues("font-size", "detail", "spinner-style", "position", "background-color", "text-color", "size-percent", "hide-after"),
	plainFlags("markdown", "spinner", "sticky", "no-frame"),
	[]optSpec{optTarget, optPane, optPaneID},
)

// allowlist is the per-leaf allowlist (plan: Technical Details > Argv parsing
// and allowlist). A leaf or option that is not here is refused.
var allowlist = []leafSpec{
	{path: words("version"), maxPos: 0},
	{path: words("help"), maxPos: -1},
	{path: words("tree"), maxPos: 0},
	{path: words("events"), maxPos: 0, opts: join(plainValues("kind", "run", "after", "limit"))},

	{path: words("session text"), scope: scopePane, by: byID, maxPos: 0,
		opts: join(plainFlags("all"), plainValues("lines"), []optSpec{optTarget, optPane, optPaneID})},
	{path: words("session overlay result"), scope: scopePane, by: bySide, maxPos: 0,
		opts: join(plainValues("page"), []optSpec{optBlock, optTarget, optPane})},
	{path: words("session overlay text"), scope: scopePane, by: bySide, maxPos: 0,
		opts: join(plainFlags("all"), plainValues("lines"), []optSpec{optTarget, optPane})},

	{path: words("session type"), scope: scopePane, write: true, by: byID, typing: true, maxPos: 1,
		opts: join(plainFlags("stdin"), []optSpec{optTarget, optPaneID})},

	{path: words("session status"), scope: scopeRow, write: true, by: byID, maxPos: 1, minPos: 1,
		posAllowed: []string{"idle", "active", "completed", "blocked"},
		opts: join(plainFlags("blink", "auto-reset"), plainValues("sound", "color", "shape"),
			[]optSpec{optTarget, optPane, optPaneID})},
	{path: words("session context"), scope: scopeRow, write: true, maxPos: 1,
		opts: join(plainFlags("clear"), []optSpec{optTarget})},
	{path: words("session rename"), scope: scopeRow, write: true, maxPos: 1, minPos: 1,
		opts: []optSpec{optTarget}},
	{path: words("session hud"), scope: scopeRow, write: true, maxPos: 1, minPos: 1, opts: hudOpts},
	{path: words("session hud open"), scope: scopeRow, write: true, maxPos: 1, minPos: 1, opts: hudOpts},
	{path: words("session hud update"), scope: scopeRow, write: true, maxPos: 1, minPos: 1, opts: hudOpts},
	{path: words("session hud close"), scope: scopeRow, write: true, maxPos: 0,
		opts: []optSpec{optTarget, optPane, optPaneID}},

	{path: words("session overlay open"), scope: scopePane, write: true, by: bySide, maxPos: -1, posCommand: true,
		opts: join(
			[]optSpec{{name: "html", value: true, role: roleFile}, {name: "url", value: true}},
			plainFlags("js", "wait"), plainValues("size-percent", "background-color"),
			[]optSpec{optBlock, optTarget, optPane}),
		check: checkOverlayOpen},
	{path: words("session overlay close"), scope: scopePane, write: true, by: bySide, maxPos: 0,
		opts: []optSpec{optTarget, optPane}},
	{path: words("session overlay submit"), scope: scopePane, write: true, by: bySide, maxPos: 0,
		opts: join(plainValues("value"), []optSpec{optTarget, optPane})},
	{path: words("session overlay resize"), scope: scopeRow, write: true, maxPos: 0,
		opts: join(plainValues("size-percent"), plainFlags("full"), []optSpec{optTarget})},

	{path: words("session new"), maxPos: 0,
		opts: join(plainValues("name", "cwd", "workspace"), plainFlags("no-select", "wait"),
			[]optSpec{{name: "command", value: true, role: roleCommand}})},
	{path: words("session close"), scope: scopeRow, write: true, maxPos: 0,
		opts: []optSpec{{name: "target", value: true, role: roleRow, repeat: true}}},
	{path: words("session restore"), scope: scopePane, write: true, by: byID, maxPos: 1, posCommand: true,
		opts: join(plainFlags("none", "clear"), []optSpec{optTarget, optPane, optPaneID})},
	{path: words("notify"), scope: scopeCaller, write: true, maxPos: 1, minPos: 1,
		opts: join(plainValues("title"), []optSpec{optTarget})},
	{path: words("session split visibility"), scope: scopeRow, write: true, maxPos: 1, minPos: 1,
		posAllowed: []string{"on", "off"},
		opts:       join(plainValues("axis"), []optSpec{optTarget})},
}

// refusedLeaves are named in the refusal, so a host learns it is policy and
// not a typo. A prefix covers everything under it.
var refusedLeaves = []struct {
	path string
	why  string
}{
	{"pick", "it opens a picker on the Mac"},
	{"ask", "it opens a dialog on the Mac"},
	{"dashboard", "it changes the Mac's window"},
	{"quick", "it reaches the Mac's quick terminal"},
	{"workspace", "workspaces are the Mac's"},
	{"window", "windows are the Mac's"},
	{"surface", "it acts on terminals the host does not own"},
	{"sidebar", "it changes the Mac's window"},
	{"font", "it changes the Mac's settings"},
	{"keymap", "it changes the Mac's settings"},
	{"hooks", "it runs commands on the Mac"},
	{"config", "it changes the Mac's settings"},
	{"theme", "it changes the Mac's settings"},
	{"browser", "it reaches the Mac's browser storage"},
	{"restore", "it reaches the Mac's restore state"},
	{"terminfo", "it installs files"},
	{"clipboard", "it reaches the Mac's clipboard"},
	{"zmx", "it manages daemons on the Mac"},
	{"session duplicate", "it starts a shell on the Mac"},
	{"session select", "it moves the user's selection"},
	{"session go", "it moves the user's selection"},
	{"session reveal", "it opens Finder on the Mac"},
	{"session move", "it rearranges the user's rows"},
	{"session swap", "it rearranges the user's panes"},
	{"session lead", "it takes the terminal's lead"},
	{"session scratch", "it starts a shell on the Mac"},
	{"session focus", "it moves the user's focus"},
	{"session copy", "it reads the Mac's selection"},
	{"session paste", "it pastes the Mac's clipboard"},
	{"session select-all", "it changes the Mac's selection"},
	{"session restart", "it replaces a shell on the Mac"},
	{"session flag", "it changes the user's flagged set"},
	{"session seen", "it changes the user's badges"},
	{"session search", "it searches the Mac's scrollback"},
	{"session background", "it reads an image from the Mac"},
	{"session split close", "it closes a pane the host does not own"},
	{"session overlay reload", "it acts on an overlay the host did not open"},
	{"session overlay navigate", "it opens a browser or Finder on the Mac"},
	{"session overlay run-job", "it runs a job on the Mac"},
	{"session overlay copy", "it reads the Mac's clipboard"},
}

func matchLeaf(argv []string) (*leafSpec, []string, error) {
	if len(argv) == 0 {
		return nil, nil, errors.New("no command given")
	}
	var best *leafSpec
	for i := range allowlist {
		leaf := &allowlist[i]
		if hasPrefix(argv, leaf.path) && (best == nil || len(leaf.path) > len(best.path)) {
			best = leaf
		}
	}
	if best != nil {
		return best, argv[len(best.path):], nil
	}
	for _, refused := range refusedLeaves {
		if hasPrefix(argv, words(refused.path)) {
			return nil, nil, fmt.Errorf("`%s` is not available from a host: %s", refused.path, refused.why)
		}
	}
	shown := argv
	if len(shown) > 3 {
		shown = shown[:3]
	}
	return nil, nil, fmt.Errorf("`%s` is not available from a host (not in the allowlist)", strings.Join(shown, " "))
}

func hasPrefix(argv, prefix []string) bool {
	if len(argv) < len(prefix) {
		return false
	}
	for i, word := range prefix {
		if argv[i] != word {
			return false
		}
	}
	return true
}

// checkOverlayOpen requires exactly one source for the overlay and keeps
// host pages static: a URL must be http(s), and --js is for --html pages only
// (a host's JavaScript runs only under the page shim).
func checkOverlayOpen(args parsedArgs) error {
	sources := 0
	var rawURL string
	var js bool
	for _, opt := range args.opts {
		switch opt.spec.name {
		case "html":
			sources++
		case "url":
			sources++
			rawURL = opt.value
		case "js":
			js = true
		}
	}
	if len(args.pos) > 0 {
		sources++
	}
	if sources != 1 {
		return errors.New("give exactly one of --html, --url or a command")
	}
	if rawURL != "" {
		parsed, err := url.Parse(rawURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("--url must be an http or https URL, got %q", rawURL)
		}
		if js {
			return errors.New("--js is not allowed with --url from a host (a host's JavaScript runs only in --html pages, under the page shim)")
		}
	}
	return nil
}

// Verdict is the outcome of Authorize.
type Verdict uint8

const (
	// Refuse means the call must not run; Reason says why.
	Refuse Verdict = iota
	// Allow means the call may run as Call describes it.
	Allow
	// NeedsConsent means the call may run only if the user agrees; Consent
	// describes what to ask.
	NeedsConsent
)

// PaneBy values of a Call say how its pane is passed to agtermctl.
const (
	PaneByID   = "id"
	PaneBySide = "side"
)

// Opt is one option that passes the schema.
type Opt struct {
	Name  string
	Value string
	Flag  bool
	// Role is "" for a plain option, "file" (Value is @<i>) or "command"
	// (Value is rewritten before it runs).
	Role string
}

// Target is one resolved row with the pane the call acts on.
type Target struct {
	Row    string
	PaneID string
	// Side is left or right on a row with a split, else empty.
	Side string
}

// FileRef is an option that names a body file.
type FileRef struct {
	Opt   string
	Index int
}

// Call is an authorized call, normalised: targets are full ids from the tree,
// --window and --socket are gone, and options are in --name=value form.
type Call struct {
	// Leaf is the subcommand path joined by spaces, e.g. "session type".
	Leaf string
	Path []string
	// Opts are the plain, file and command options, without row and pane
	// options (those are Targets).
	Opts []Opt
	// Pos are the positionals (after `--` in Args).
	Pos     []string
	Targets []Target
	// PaneBy is PaneByID or PaneBySide, or "" when no pane is passed.
	PaneBy string
	Files  []FileRef
	// Command is the command line to rewrite: the --command value, or the
	// positionals of `overlay open` / `restore`. CommandOpt is the option
	// that carried it, "" for positionals.
	Command    []string
	CommandOpt string
	// Block is set for a --block overlay open, which needs its cancel hook.
	Block bool
	// Granted is set when a grant allowed typing into a local pane.
	Granted bool
	// Help is set for a --help call, which Args reduces to the path.
	Help bool
}

// Args renders the normalised argv for the real agtermctl, without --socket.
func (c Call) Args() []string {
	args := append([]string(nil), c.Path...)
	if c.Help {
		return append(args, "--help")
	}
	for _, target := range c.Targets {
		args = append(args, "--target="+target.Row)
		switch c.PaneBy {
		case PaneByID:
			if target.PaneID != "" {
				args = append(args, "--pane-id="+target.PaneID)
			}
		case PaneBySide:
			if target.Side != "" {
				args = append(args, "--pane="+target.Side)
			}
		}
	}
	for _, opt := range c.Opts {
		if opt.Flag {
			args = append(args, "--"+opt.Name)
		} else {
			args = append(args, "--"+opt.Name+"="+opt.Value)
		}
	}
	// positionals follow the options; `--` is needed only for one that
	// starts like an option.
	for _, word := range c.Pos {
		if strings.HasPrefix(word, "-") {
			args = append(args, "--")
			break
		}
	}
	return append(args, c.Pos...)
}

// Consent is what the daemon asks the user when a host wants to type into a
// local agent pane it holds no grant for.
type Consent struct {
	Row     string
	RowName string
	PaneID  string
	// Text is what would be typed.
	Text string
	// Preview is Text cut to 300 characters on one line (newline as ⏎, tab
	// as ⇥), with the full length when cut.
	Preview string
}

// Decision is the result of Authorize.
type Decision struct {
	Verdict Verdict
	// Reason is the refusal text, without the `agtermctl (via agr):` prefix.
	Reason  string
	Call    Call
	Consent *Consent
	// Drop lists grants of the caller whose pane or row no longer exists.
	// Authorize stays pure; the daemon drops them from the store.
	Drop []bindings.Grant
}

// Policy decides whether a host's agtermctl call may run.
type Policy struct {
	tree   TreeReader
	binds  BindingLoader
	agents []string
}

// NewPolicy returns a policy that reads agterm's tree and the bindings once
// per call. agents are the program names `session type` may reach in a local
// pane; none given means DefaultAgents.
func NewPolicy(tree TreeReader, binds BindingLoader, agents ...string) *Policy {
	if len(agents) == 0 {
		agents = DefaultAgents
	}
	return &Policy{tree: tree, binds: binds, agents: agents}
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type world struct {
	rows  map[string]agterm.TreeRow
	bound map[string]bindings.Binding
}

func key(id string) string { return strings.ToLower(id) }

// panes lists the left and right surfaces of a row: the panes a host can own.
func panes(row agterm.TreeRow) []agterm.Surface {
	var list []agterm.Surface
	for _, surface := range row.Surfaces {
		if surface.Kind == "left" || surface.Kind == "right" {
			list = append(list, surface)
		}
	}
	return list
}

func surfaceByID(row agterm.TreeRow, id string) (agterm.Surface, bool) {
	for _, surface := range row.Surfaces {
		if strings.EqualFold(surface.PaneID, id) {
			return surface, true
		}
	}
	return agterm.Surface{}, false
}

func paneIDs(row agterm.TreeRow) string {
	var ids []string
	for _, surface := range panes(row) {
		ids = append(ids, surface.PaneID)
	}
	return strings.Join(ids, ", ")
}

// foreground returns the argv and shell name of the program in a surface, from
// the fields of that surface alone. ok is false for a surface without any.
func foreground(row agterm.TreeRow, kind string) (argv []string, shell string, ok bool) {
	switch kind {
	case "left":
		return row.Foreground, row.ForegroundShell, true
	case "right":
		if row.HasSplit {
			return row.SplitForeground, row.SplitForegroundShell, true
		}
	}
	return nil, "", false
}

// attached reports whether the pane of b still runs `agr open` for b (Spike
// results > Attached). After a detach the binding stays while the pane is a
// local Mac shell, which this check tells apart.
func attached(row agterm.TreeRow, b bindings.Binding) bool {
	surface, found := surfaceByID(row, b.PaneID)
	if !found {
		return false
	}
	argv, _, ok := foreground(row, surface.Kind)
	if !ok || len(argv) < 3 {
		return false
	}
	if path.Base(argv[0]) != "agr" || argv[1] != "open" || argv[2] != b.Host {
		return false
	}
	if len(argv) > 3 && !strings.HasPrefix(argv[3], "-") && argv[3] != b.Name {
		return false
	}
	return true
}

// SanitizeTyped checks text typed into a pane the host does not own: valid
// UTF-8, no C0 control other than \n and \t, no DEL or C1 control, no Unicode
// Cf character (bidi, zero-width), at most 64 KiB. That blocks \e[Z (Claude's
// mode cycle), \e[201~ (paste end) and hidden text.
func SanitizeTyped(text []byte) error {
	if len(text) > MaxTypedText {
		return fmt.Errorf("text is %d bytes (at most %d)", len(text), MaxTypedText)
	}
	if !utf8.Valid(text) {
		return errors.New("text is not valid UTF-8")
	}
	for _, r := range string(text) {
		switch {
		case r == '\n' || r == '\t':
		case unicode.Is(unicode.Cc, r):
			return fmt.Errorf("text contains the control character U+%04X", r)
		case unicode.Is(unicode.Cf, r):
			return fmt.Errorf("text contains the invisible character U+%04X", r)
		}
	}
	return nil
}

// Preview renders text for a consent dialog: at most 300 characters on one
// line, a newline shown as ⏎ and a tab as ⇥, with the full length when cut.
func Preview(text string) string {
	runes := []rune(text)
	cut := len(runes) > previewRunes
	if cut {
		runes = runes[:previewRunes]
	}
	shown := strings.NewReplacer("\n", "⏎", "\t", "⇥").Replace(string(runes))
	if cut {
		shown += fmt.Sprintf("… (%d characters)", len([]rune(text)))
	}
	return shown
}

// Authorize decides one call. It is pure: it reads the tree and the bindings
// and changes nothing, so the daemon applies Drop itself. stdin is the body's
// stdin part, needed to vet text typed with --stdin.
func (p *Policy) Authorize(ctx context.Context, host string, caller bindings.Binding, hdr Header, argv []string, stdin []byte) Decision {
	a := &authz{p: p, host: host, caller: caller, hdr: hdr, stdin: stdin}
	decision := a.run(ctx, argv)
	decision.Drop = a.drop
	return decision
}

type authz struct {
	p      *Policy
	host   string
	caller bindings.Binding
	hdr    Header
	stdin  []byte

	leaf *leafSpec
	call Call
	// raw option values by role.
	rows      []string
	paneID    string
	side      string
	stdinFlag bool
	args      parsedArgs

	w    world
	drop []bindings.Grant
}

func refuse(format string, args ...any) Decision {
	return Decision{Verdict: Refuse, Reason: fmt.Sprintf(format, args...)}
}

func (a *authz) run(ctx context.Context, argv []string) Decision {
	if len(argv) > 0 && (argv[0] == "--help" || argv[0] == "-h") {
		return Decision{Verdict: Allow, Call: Call{Leaf: "help", Path: nil, Help: true}}
	}
	leaf, rest, err := matchLeaf(argv)
	if err != nil {
		return refuse("%v", err)
	}
	a.leaf = leaf
	args, err := parseArgs(leaf, rest)
	if err != nil {
		return refuse("%s: %v", leaf.name(), err)
	}
	if args.help {
		return Decision{Verdict: Allow, Call: Call{Leaf: leaf.name(), Path: leaf.path, Help: true}}
	}
	a.args = args
	if err := a.normalise(); err != nil {
		return refuse("%s: %v", leaf.name(), err)
	}
	if leaf.scope == scopeNone {
		return Decision{Verdict: Allow, Call: a.call}
	}
	if err := a.load(ctx); err != nil {
		return refuse("cannot read agterm state: %v", err)
	}
	switch leaf.scope {
	case scopeCaller:
		return a.callerScope()
	case scopePane:
		return a.paneScope()
	default:
		return a.rowScope()
	}
}

// normalise checks the schema-level rules and fills the call's options.
func (a *authz) normalise() error {
	leaf, args := a.leaf, a.args
	a.call = Call{Leaf: leaf.name(), Path: leaf.path}
	if len(args.pos) < leaf.minPos {
		return fmt.Errorf("needs %d argument(s), got %d", leaf.minPos, len(args.pos))
	}
	if leaf.maxPos >= 0 && len(args.pos) > leaf.maxPos {
		return fmt.Errorf("takes at most %d argument(s), got %d", leaf.maxPos, len(args.pos))
	}
	if len(leaf.posAllowed) > 0 && len(args.pos) > 0 && !slices.Contains(leaf.posAllowed, args.pos[0]) {
		return fmt.Errorf("%q is not allowed (one of %s)", args.pos[0], strings.Join(leaf.posAllowed, ", "))
	}
	if leaf.check != nil {
		if err := leaf.check(args); err != nil {
			return err
		}
	}
	for _, opt := range args.opts {
		if opt.value == "" && (opt.spec.role == roleRow || opt.spec.role == rolePane || opt.spec.role == roleSide) {
			return fmt.Errorf("--%s is empty", opt.spec.name)
		}
		switch opt.spec.role {
		case roleRow:
			a.rows = append(a.rows, opt.value)
		case rolePane:
			a.paneID = opt.value
		case roleSide:
			a.side = opt.value
		case roleFile:
			index, err := fileIndex(opt.value, len(a.hdr.Files))
			if err != nil {
				return fmt.Errorf("--%s: %v", opt.spec.name, err)
			}
			a.call.Files = append(a.call.Files, FileRef{Opt: opt.spec.name, Index: index})
			a.call.Opts = append(a.call.Opts, Opt{Name: opt.spec.name, Value: opt.value, Role: "file"})
		case roleCommand:
			if strings.TrimSpace(opt.value) == "" {
				return fmt.Errorf("--%s is empty", opt.spec.name)
			}
			a.call.Command = []string{opt.value}
			a.call.CommandOpt = opt.spec.name
			a.call.Opts = append(a.call.Opts, Opt{Name: opt.spec.name, Value: opt.value, Role: "command"})
		case rolePlain:
			if opt.spec.name == "stdin" {
				a.stdinFlag = true
			}
			a.call.Opts = append(a.call.Opts, Opt{Name: opt.spec.name, Value: opt.value, Flag: !opt.spec.value})
		}
		// only a blocking overlay open has a cancel hook to run.
		if opt.spec.name == "block" && leaf.name() == "session overlay open" {
			a.call.Block = true
		}
	}
	if len(a.rows) > 1 && !a.repeatsRows() {
		return errors.New("--target given more than once")
	}
	a.call.Pos = args.pos
	if leaf.posCommand && len(args.pos) > 0 {
		for _, word := range args.pos {
			if strings.TrimSpace(word) == "" {
				return errors.New("the command is empty")
			}
		}
		a.call.Command = args.pos
	}
	return nil
}

func (a *authz) repeatsRows() bool {
	for _, spec := range a.leaf.opts {
		if spec.role == roleRow {
			return spec.repeat
		}
	}
	return false
}

func fileIndex(value string, count int) (int, error) {
	if !strings.HasPrefix(value, "@") {
		return 0, fmt.Errorf("%q is not a file reference (@<i>)", value)
	}
	index, err := strconv.Atoi(value[1:])
	if err != nil || index < 0 || strconv.Itoa(index) != value[1:] {
		return 0, fmt.Errorf("%q is not a file reference (@<i>)", value)
	}
	if index >= count {
		return 0, fmt.Errorf("%q names a file the request did not send", value)
	}
	return index, nil
}

func (a *authz) load(ctx context.Context) error {
	rows, err := a.p.tree.TreeRows(ctx)
	if err != nil {
		return err
	}
	all, err := a.p.binds.Load()
	if err != nil {
		return err
	}
	a.w = world{rows: make(map[string]agterm.TreeRow, len(rows)), bound: make(map[string]bindings.Binding, len(all))}
	for _, row := range rows {
		a.w.rows[key(row.ID)] = row
	}
	for _, b := range all {
		if b.PaneID != "" {
			a.w.bound[key(b.PaneID)] = b
		}
	}
	for _, grant := range a.caller.Grants {
		row, ok := a.w.rows[key(grant.Row)]
		if ok {
			_, ok = surfaceByID(row, grant.PaneID)
		}
		if !ok {
			a.drop = append(a.drop, grant)
		}
	}
	return nil
}

// mapRow resolves a --target value to a live row: the caller's current row
// for an empty value or a self marker, else a full id found in the tree.
func (a *authz) mapRow(raw string) (agterm.TreeRow, error) {
	id := raw
	switch {
	case raw == "":
		id = a.caller.Row
	case a.hdr.Self.Row != "" && strings.EqualFold(raw, a.hdr.Self.Row):
		id = a.caller.Row
	case !uuidPattern.MatchString(raw):
		return agterm.TreeRow{}, fmt.Errorf("--target must be a full row id from `tree --json`, got %q (prefixes and `active` are not accepted)", raw)
	}
	row, ok := a.w.rows[key(id)]
	if !ok {
		return agterm.TreeRow{}, fmt.Errorf("no such row %s", id)
	}
	return row, nil
}

func (a *authz) isCallerRow(row agterm.TreeRow) bool { return strings.EqualFold(row.ID, a.caller.Row) }

func (a *authz) targetRows() ([]agterm.TreeRow, error) {
	raws := a.rows
	if len(raws) == 0 {
		raws = []string{""}
	}
	var rows []agterm.TreeRow
	seen := make(map[string]bool)
	for _, raw := range raws {
		row, err := a.mapRow(raw)
		if err != nil {
			return nil, err
		}
		if !seen[key(row.ID)] {
			seen[key(row.ID)] = true
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// requestedPane resolves --pane-id / --pane to a surface of row. given is
// false when neither was passed.
func (a *authz) requestedPane(row agterm.TreeRow) (surface agterm.Surface, given bool, err error) {
	if a.paneID != "" {
		id := a.paneID
		if a.hdr.Self.PaneID != "" && strings.EqualFold(id, a.hdr.Self.PaneID) {
			id = a.caller.PaneID
		} else if !uuidPattern.MatchString(id) {
			return agterm.Surface{}, true, fmt.Errorf("--pane-id must be a full pane id, got %q", id)
		}
		found, ok := surfaceByID(row, id)
		if !ok {
			return agterm.Surface{}, true, fmt.Errorf("pane %s is not in row %s", id, row.ID)
		}
		surface = found
		given = true
	}
	if a.side != "" {
		kind := "left"
		if a.side == "right" || a.side == "split" {
			kind = "right"
		}
		var found agterm.Surface
		ok := false
		for _, candidate := range panes(row) {
			if candidate.Kind == kind {
				found, ok = candidate, true
			}
		}
		if !ok {
			return agterm.Surface{}, true, fmt.Errorf("row %s has no %s pane", row.ID, kind)
		}
		if given && !strings.EqualFold(found.PaneID, surface.PaneID) {
			return agterm.Surface{}, true, errors.New("--pane and --pane-id name different panes")
		}
		surface = found
		given = true
	}
	return surface, given, nil
}

func (a *authz) target(row agterm.TreeRow, surface agterm.Surface) Target {
	target := Target{Row: row.ID, PaneID: surface.PaneID}
	if row.HasSplit && (surface.Kind == "left" || surface.Kind == "right") {
		target.Side = surface.Kind
	}
	return target
}

func (a *authz) ownedBy(row agterm.TreeRow, surface agterm.Surface) (bindings.Binding, bool) {
	b, ok := a.w.bound[key(surface.PaneID)]
	if !ok || b.Host != a.host || !strings.EqualFold(b.Row, row.ID) {
		return bindings.Binding{}, false
	}
	return b, true
}

func (a *authz) callerScope() Decision {
	row, ok := a.w.rows[key(a.caller.Row)]
	if !ok {
		return refuse("%s: your row is gone", a.leaf.name())
	}
	for _, raw := range a.rows {
		mapped, err := a.mapRow(raw)
		if err != nil {
			return refuse("%s: %v", a.leaf.name(), err)
		}
		if !a.isCallerRow(mapped) {
			return refuse("%s: it targets the caller's own row only", a.leaf.name())
		}
	}
	surface, found := surfaceByID(row, a.caller.PaneID)
	if !found {
		return refuse("%s: your pane is not in the row", a.leaf.name())
	}
	if refusal := a.checkOwned(row, surface); refusal != "" {
		return refuse("%s: %s", a.leaf.name(), refusal)
	}
	a.call.Targets = []Target{{Row: row.ID}}
	return Decision{Verdict: Allow, Call: a.call}
}

// checkOwned returns why a pane is not the host's to act on, or "".
func (a *authz) checkOwned(row agterm.TreeRow, surface agterm.Surface) string {
	b, ok := a.ownedBy(row, surface)
	if !ok {
		if other, bound := a.w.bound[key(surface.PaneID)]; bound && other.Host != a.host {
			return fmt.Sprintf("pane %s is bound to another host", surface.PaneID)
		}
		return fmt.Sprintf("pane %s is not bound to %s", surface.PaneID, a.host)
	}
	if a.leaf.write && !attached(row, b) {
		return fmt.Sprintf("pane %s is not attached yet; retry once `agr open` runs in it (a detached pane stays refused)", surface.PaneID)
	}
	return ""
}

func (a *authz) rowScope() Decision {
	rows, err := a.targetRows()
	if err != nil {
		return refuse("%s: %v", a.leaf.name(), err)
	}
	for _, row := range rows {
		list := panes(row)
		if len(list) == 0 {
			return refuse("%s: row %s has no pane", a.leaf.name(), row.ID)
		}
		for _, surface := range list {
			if why := a.checkOwned(row, surface); why != "" {
				return refuse("%s: row %s: %s (a row-level call needs every pane of the row)", a.leaf.name(), row.ID, why)
			}
		}
		target := Target{Row: row.ID}
		if a.paneID != "" || a.side != "" {
			surface, _, err := a.requestedPane(row)
			if err != nil {
				return refuse("%s: %v", a.leaf.name(), err)
			}
			target.PaneID = surface.PaneID
			a.call.PaneBy = PaneByID
		}
		a.call.Targets = append(a.call.Targets, target)
	}
	return Decision{Verdict: Allow, Call: a.call}
}

func (a *authz) paneScope() Decision {
	rows, err := a.targetRows()
	if err != nil {
		return refuse("%s: %v", a.leaf.name(), err)
	}
	row := rows[0]
	surface, err := a.choosePane(row)
	if err != nil {
		return refuse("%s: %v", a.leaf.name(), err)
	}
	a.call.PaneBy = PaneByID
	if a.leaf.by == bySide {
		a.call.PaneBy = PaneBySide
	}
	a.call.Targets = []Target{a.target(row, surface)}

	if b, bound := a.w.bound[key(surface.PaneID)]; bound && b.Host != a.host {
		return refuse("%s: pane %s is bound to another host", a.leaf.name(), surface.PaneID)
	}
	if _, owned := a.ownedBy(row, surface); owned {
		if why := a.checkOwned(row, surface); why != "" {
			return refuse("%s: %s", a.leaf.name(), why)
		}
		return Decision{Verdict: Allow, Call: a.call}
	}
	if _, bound := a.w.bound[key(surface.PaneID)]; bound {
		return refuse("%s: pane %s has a stale binding", a.leaf.name(), surface.PaneID)
	}
	if !a.leaf.typing {
		if a.leaf.write {
			return refuse("%s: pane %s is not bound to %s; a host writes only to its own panes (only `session type` can reach an agent pane, with the user's consent)", a.leaf.name(), surface.PaneID, a.host)
		}
		return refuse("%s: pane %s is not bound to %s; a host reads only its own panes", a.leaf.name(), surface.PaneID, a.host)
	}
	return a.typeLocal(row, surface)
}

// choosePane resolves the pane a pane-class call acts on.
func (a *authz) choosePane(row agterm.TreeRow) (agterm.Surface, error) {
	surface, given, err := a.requestedPane(row)
	if err != nil {
		return agterm.Surface{}, err
	}
	if given {
		return surface, nil
	}
	if a.isCallerRow(row) {
		found, ok := surfaceByID(row, a.caller.PaneID)
		if !ok {
			return agterm.Surface{}, errors.New("your pane is not in the row")
		}
		return found, nil
	}
	if only := panes(row); len(only) == 1 {
		return only[0], nil
	}
	var candidates []agterm.Surface
	for _, candidate := range panes(row) {
		if _, owned := a.ownedBy(row, candidate); owned {
			candidates = append(candidates, candidate)
			continue
		}
		if _, bound := a.w.bound[key(candidate.PaneID)]; !bound && a.leaf.typing && a.p.agentRunning(row, candidate.Kind) == nil {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) != 1 {
		return agterm.Surface{}, fmt.Errorf("row %s has %d candidate panes; pass --pane-id (panes: %s)", row.ID, len(candidates), paneIDs(row))
	}
	return candidates[0], nil
}

// agentRunning applies the program rule to one surface.
func (p *Policy) agentRunning(row agterm.TreeRow, kind string) error {
	argv, shell, ok := foreground(row, kind)
	if !ok {
		return fmt.Errorf("the %s surface has no readable program", kind)
	}
	if len(argv) == 0 {
		if shell != "" {
			return fmt.Errorf("the pane is at a shell (%s), not running %s", shell, strings.Join(p.agents, " or "))
		}
		return fmt.Errorf("the pane's program is unknown, not %s", strings.Join(p.agents, " or "))
	}
	if name := path.Base(argv[0]); !slices.Contains(p.agents, name) {
		return fmt.Errorf("the pane runs %q, not %s", name, strings.Join(p.agents, " or "))
	}
	return nil
}

// typeText returns the text `session type` would type.
func (a *authz) typeText() ([]byte, error) {
	switch {
	case a.stdinFlag && len(a.args.pos) > 0:
		return nil, errors.New("give the text as an argument or with --stdin, not both")
	case a.stdinFlag:
		return a.stdin, nil
	case len(a.args.pos) == 1:
		return []byte(a.args.pos[0]), nil
	}
	return nil, errors.New("no text to type")
}

// typeLocal decides `session type` into a local pane the host does not own:
// the program rule and the sanitiser first (a call that cannot proceed never
// asks the user), then the caller's grants, else consent.
func (a *authz) typeLocal(row agterm.TreeRow, surface agterm.Surface) Decision {
	name := a.leaf.name()
	if _, bound := a.w.bound[key(surface.PaneID)]; bound {
		return refuse("%s: pane %s is bound to a host", name, surface.PaneID)
	}
	if err := a.p.agentRunning(row, surface.Kind); err != nil {
		return refuse("%s: %v", name, err)
	}
	text, err := a.typeText()
	if err != nil {
		return refuse("%s: %v", name, err)
	}
	if err := SanitizeTyped(text); err != nil {
		return refuse("%s: %v", name, err)
	}
	for _, grant := range a.caller.Grants {
		if strings.EqualFold(grant.Row, row.ID) && strings.EqualFold(grant.PaneID, surface.PaneID) {
			a.call.Granted = true
			return Decision{Verdict: Allow, Call: a.call}
		}
	}
	return Decision{Verdict: NeedsConsent, Call: a.call, Consent: &Consent{
		Row: row.ID, RowName: row.Name, PaneID: surface.PaneID,
		Text: string(text), Preview: Preview(string(text)),
	}}
}
