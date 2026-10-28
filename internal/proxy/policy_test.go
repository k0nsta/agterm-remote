package proxy

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/proxy/mocks"
)

const testHost = "homelab"

// testID is the prefix of every fixture id; two digits follow.
const testID = "00000000-0000-4000-8000-0000000000"

var (
	rowCaller    = testID + "01" // the caller's row: one pane, bound and attached
	paneCaller   = testID + "02"
	rowSib       = testID + "03" // another row of the host: bound and attached
	paneSib      = testID + "04"
	rowAgent     = testID + "05" // a local row running claude
	paneAgent    = testID + "06"
	rowMixed     = testID + "07" // left bound to the host, right a local shell
	mixedLeft    = testID + "08"
	mixedRight   = testID + "09"
	rowOther     = testID + "10" // bound to another host
	paneOther    = testID + "11"
	rowDetached  = testID + "12" // bound, but the pane is a local shell again
	paneDetached = testID + "13"
	rowShellCod  = testID + "14" // local: left a shell, right codex
	shellCodL    = testID + "15"
	shellCodR    = testID + "16"
	rowScratch   = testID + "17" // local: main pane claude, plus a scratch surface
	scratchMain  = testID + "18"
	scratchPane  = testID + "19"
	rowTwoAgents = testID + "20" // local: left claude, right codex
	twoAgentsL   = testID + "21"
	twoAgentsR   = testID + "22"
	rowTwoShells = testID + "23" // local: two shells
	twoShellsL   = testID + "24"
	twoShellsR   = testID + "25"
	rowGone      = testID + "98"
	paneGone     = testID + "99"
)

func attachedFG(t *testing.T, host, name string) []string {
	t.Helper()
	return []string{"/opt/homebrew/bin/agr", "open", host, name}
}

type fixture struct {
	rows   []agterm.TreeRow
	binds  []bindings.Binding
	caller bindings.Binding
	hdr    Header
}

func bind(t *testing.T, row, pane, pos, host, name string) bindings.Binding {
	t.Helper()
	return bindings.Binding{Row: row, PaneID: pane, Pane: pos, Host: host, Name: name}
}

func surfaces(t *testing.T, pairs ...string) []agterm.Surface {
	t.Helper()
	var list []agterm.Surface
	for i := 0; i < len(pairs); i += 2 {
		list = append(list, agterm.Surface{Kind: pairs[i], PaneID: pairs[i+1], Visible: true})
	}
	return list
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{}
	f.caller = bind(t, rowCaller, paneCaller, "left", testHost, "w1")
	f.binds = []bindings.Binding{
		f.caller,
		bind(t, rowSib, paneSib, "left", testHost, "w2"),
		bind(t, rowMixed, mixedLeft, "left", testHost, "w3"),
		bind(t, rowOther, paneOther, "left", "cloud", "c1"),
		bind(t, rowDetached, paneDetached, "left", testHost, "w4"),
	}
	f.rows = []agterm.TreeRow{
		{ID: rowCaller, Name: "w1", Surfaces: surfaces(t, "left", paneCaller), Foreground: attachedFG(t, testHost, "w1")},
		{ID: rowSib, Name: "w2", Surfaces: surfaces(t, "left", paneSib), Foreground: attachedFG(t, testHost, "w2")},
		{ID: rowAgent, Name: "orchestrator", Surfaces: surfaces(t, "left", paneAgent), Foreground: []string{"claude", "--resume"}},
		{ID: rowMixed, Name: "w3", HasSplit: true, Surfaces: surfaces(t, "left", mixedLeft, "right", mixedRight),
			Foreground: attachedFG(t, testHost, "w3"), SplitForegroundShell: "zsh"},
		{ID: rowOther, Name: "c1", Surfaces: surfaces(t, "left", paneOther), Foreground: attachedFG(t, "cloud", "c1")},
		{ID: rowDetached, Name: "w4", Surfaces: surfaces(t, "left", paneDetached), ForegroundShell: "zsh"},
		{ID: rowShellCod, Name: "shellcod", HasSplit: true, Surfaces: surfaces(t, "left", shellCodL, "right", shellCodR),
			ForegroundShell: "zsh", SplitForeground: []string{"codex"}},
		{ID: rowScratch, Name: "scratchy", Surfaces: surfaces(t, "left", scratchMain, "scratch", scratchPane),
			Foreground: []string{"claude"}},
		{ID: rowTwoAgents, Name: "two", HasSplit: true, Surfaces: surfaces(t, "left", twoAgentsL, "right", twoAgentsR),
			Foreground: []string{"/usr/local/bin/claude"}, SplitForeground: []string{"codex"}},
		{ID: rowTwoShells, Name: "shells", HasSplit: true, Surfaces: surfaces(t, "left", twoShellsL, "right", twoShellsR),
			ForegroundShell: "zsh", SplitForegroundShell: "zsh"},
	}
	f.hdr = Header{Session: "w1", Self: Self{Row: rowCaller, PaneID: paneCaller}, CWD: "/home/u", Files: []int{10}}
	return f
}

func (f *fixture) row(t *testing.T, id string) *agterm.TreeRow {
	t.Helper()
	for i := range f.rows {
		if f.rows[i].ID == id {
			return &f.rows[i]
		}
	}
	t.Fatalf("no fixture row %s", id)
	return nil
}

func (f *fixture) grant(t *testing.T, grants ...bindings.Grant) {
	t.Helper()
	f.caller.Grants = append(f.caller.Grants, grants...)
}

func (f *fixture) policy(t *testing.T) *Policy {
	t.Helper()
	ctrl := gomock.NewController(t)
	tree := mocks.NewMockTreeReader(ctrl)
	tree.EXPECT().TreeRows(gomock.Any()).Return(f.rows, nil).AnyTimes()
	binds := mocks.NewMockBindingLoader(ctrl)
	binds.EXPECT().Load().Return(f.storeWithCaller(t), nil).AnyTimes()
	return NewPolicy(tree, binds)
}

// storeWithCaller is f.binds with the caller's entry (grants included) as
// f.caller has it now; a caller that is not in f.binds is added.
func (f *fixture) storeWithCaller(t *testing.T) []bindings.Binding {
	t.Helper()
	var out []bindings.Binding
	seen := false
	for _, b := range f.binds {
		if b.Host == f.caller.Host && b.Name == f.caller.Name {
			b, seen = f.caller, true
		}
		out = append(out, b)
	}
	if !seen {
		out = append(out, f.caller)
	}
	return out
}

func (f *fixture) authorize(t *testing.T, argv ...string) Decision {
	t.Helper()
	return f.authorizeStdin(t, "", argv...)
}

func (f *fixture) authorizeStdin(t *testing.T, stdin string, argv ...string) Decision {
	t.Helper()
	return f.policy(t).Authorize(context.Background(), testHost, f.caller, f.hdr, argv, []byte(stdin))
}

func argvOf(t *testing.T, s string) []string {
	t.Helper()
	return strings.Fields(s)
}

func wantAllow(t *testing.T, got Decision) {
	t.Helper()
	if got.Verdict != Allow {
		t.Fatalf("verdict = %v (%s), want Allow", got.Verdict, got.Reason)
	}
}

func wantRefuse(t *testing.T, got Decision, substr string) {
	t.Helper()
	if got.Verdict != Refuse {
		t.Fatalf("verdict = %v, want Refuse (%q)", got.Verdict, substr)
	}
	if !strings.Contains(got.Reason, substr) {
		t.Fatalf("reason = %q, want it to contain %q", got.Reason, substr)
	}
}

func TestAuthorizeAllowedLeaves(t *testing.T) {
	own := fmt.Sprintf("--target=%s --pane-id=%s", rowCaller, paneCaller)
	tests := []struct {
		name string
		argv string
		want string
	}{
		{"version", "version", "version"},
		{"tree", "tree --json", "tree --json"},
		{"events run", "events --json --run abc --after 3", "events --json --run=abc --after=3"},
		{"help", "help session type", "help session type"},
		{"--help", "--help", "--help"},
		{"leaf --help", "session type --help", "session type --help"},
		{"window and socket stripped", "tree --window w --socket=/tmp/x --json", "tree --json"},
		{"text", "session text", "session text " + own},
		{"text of a sibling's only pane", "session text --target " + rowSib,
			fmt.Sprintf("session text --target=%s --pane-id=%s", rowSib, paneSib)},
		{"text options", "session text --all --lines 20", "session text " + own + " --all --lines=20"},
		{"overlay result", "session overlay result --page p1 --block",
			fmt.Sprintf("session overlay result --target=%s --page=p1 --block", rowCaller)},
		{"overlay text", "session overlay text --all --lines 5",
			fmt.Sprintf("session overlay text --target=%s --all --lines=5", rowCaller)},
		{"type owned, stdin", "session type --stdin", "session type " + own + " --stdin"},
		{"type owned, text", "session type hello", "session type " + own + " hello"},
		{"type sibling, target= form", fmt.Sprintf("session type --target=%s --pane-id=%s hi", rowSib, paneSib),
			fmt.Sprintf("session type --target=%s --pane-id=%s hi", rowSib, paneSib)},
		{"status", "session status completed --blink --color #ff0000 --sound Ping",
			fmt.Sprintf("session status --target=%s --blink --color=#ff0000 --sound=Ping completed", rowCaller)},
		{"status with pane id", "session status active --pane-id " + paneCaller,
			fmt.Sprintf("session status --target=%s --pane-id=%s active", rowCaller, paneCaller)},
		{"context", "session context working", fmt.Sprintf("session context --target=%s working", rowCaller)},
		{"context clear", "session context --clear", fmt.Sprintf("session context --target=%s --clear", rowCaller)},
		{"rename", "session rename tidy", fmt.Sprintf("session rename --target=%s tidy", rowCaller)},
		{"hud", "session hud building --hide-after 5", fmt.Sprintf("session hud --target=%s --hide-after=5 building", rowCaller)},
		{"hud open", "session hud open building", fmt.Sprintf("session hud open --target=%s building", rowCaller)},
		{"hud update", "session hud update done --sticky", fmt.Sprintf("session hud update --target=%s --sticky done", rowCaller)},
		{"hud close", "session hud close", fmt.Sprintf("session hud close --target=%s", rowCaller)},
		{"overlay open html", "session overlay open --html @0 --js --block --size-percent 70",
			fmt.Sprintf("session overlay open --target=%s --html=@0 --js --block --size-percent=70", rowCaller)},
		{"overlay open html, = form", "session overlay open --html=@0", fmt.Sprintf("session overlay open --target=%s --html=@0", rowCaller)},
		{"overlay open command", "session overlay open htop --wait", fmt.Sprintf("session overlay open --target=%s --wait htop", rowCaller)},
		{"overlay open command after --", "session overlay open -- top",
			fmt.Sprintf("session overlay open --target=%s top", rowCaller)},
		{"overlay open url", "session overlay open --url https://example.com/a",
			fmt.Sprintf("session overlay open --target=%s --url=https://example.com/a", rowCaller)},
		{"overlay close", "session overlay close", fmt.Sprintf("session overlay close --target=%s", rowCaller)},
		{"overlay submit", "session overlay submit --value yes", fmt.Sprintf("session overlay submit --target=%s --value=yes", rowCaller)},
		{"overlay resize", "session overlay resize --size-percent 60", fmt.Sprintf("session overlay resize --target=%s --size-percent=60", rowCaller)},
		{"overlay resize full", "session overlay resize --full", fmt.Sprintf("session overlay resize --target=%s --full", rowCaller)},
		{"new", "session new --name job --cwd /srv --command=make --no-select --wait --workspace dev",
			"session new --name=job --cwd=/srv --command=make --no-select --wait --workspace=dev"},
		{"close", fmt.Sprintf("session close --target %s --target=%s", rowCaller, rowSib),
			fmt.Sprintf("session close --target=%s --target=%s", rowCaller, rowSib)},
		{"close self", "session close", fmt.Sprintf("session close --target=%s", rowCaller)},
		{"restore command", "session restore make", "session restore " + own + " make"},
		{"restore none", "session restore --none", "session restore " + own + " --none"},
		{"notify", "notify done --title Build", fmt.Sprintf("notify --target=%s --title=Build done", rowCaller)},
		{"split visibility", "session split visibility on --axis horizontal",
			fmt.Sprintf("session split visibility --target=%s --axis=horizontal on", rowCaller)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			got := f.authorize(t, argvOf(t, tt.argv)...)
			wantAllow(t, got)
			if line := strings.Join(got.Call.Args(), " "); line != tt.want {
				t.Fatalf("Args() = %q, want %q", line, tt.want)
			}
		})
	}
}

func TestAuthorizeNormalisedFields(t *testing.T) {
	f := newFixture(t)
	got := f.authorize(t, "session", "overlay", "open", "--html", "@0", "--block")
	wantAllow(t, got)
	if !got.Call.Block || !reflect.DeepEqual(got.Call.Files, []FileRef{{Opt: "html", Index: 0}}) {
		t.Fatalf("Call = %#v, want Block and file 0", got.Call)
	}
	if got.Call.PaneBy != PaneBySide || got.Call.Targets[0].Side != "" {
		t.Fatalf("a row without a split passes no --pane: %#v", got.Call)
	}

	got = f.authorize(t, "session", "new", "--command=echo 'hi'")
	wantAllow(t, got)
	if got.Call.CommandOpt != "command" || !reflect.DeepEqual(got.Call.Command, []string{"echo 'hi'"}) {
		t.Fatalf("Call = %#v, want the --command value", got.Call)
	}

	got = f.authorize(t, "session", "overlay", "open", "--", "ls -la | head")
	wantAllow(t, got)
	if got.Call.CommandOpt != "" || !reflect.DeepEqual(got.Call.Command, []string{"ls -la | head"}) {
		t.Fatalf("Call = %#v, want the positional command", got.Call)
	}
	// agtermctl takes one COMMAND, a shell line; a second word is refused
	// here as it would be there.
	if got := f.authorize(t, "session", "overlay", "open", "--", "sh", "-c", "x"); got.Verdict == Allow {
		t.Fatalf("three positionals were allowed: %#v", got.Call)
	}

	got = f.authorize(t, "session", "overlay", "result", "--block")
	wantAllow(t, got)
	if got.Call.Block {
		t.Fatalf("only an overlay open with --block has a cancel hook: %#v", got.Call)
	}
}

func TestAuthorizeOverlaySideOnSplitRow(t *testing.T) {
	f := newFixture(t)
	f.caller = bind(t, rowMixed, mixedLeft, "left", testHost, "w3")
	f.hdr.Self = Self{Row: rowMixed, PaneID: mixedLeft}
	got := f.authorize(t, "session", "overlay", "close")
	wantAllow(t, got)
	if line := strings.Join(got.Call.Args(), " "); line != fmt.Sprintf("session overlay close --target=%s --pane=left", rowMixed) {
		t.Fatalf("Args() = %q", line)
	}
	wantRefuse(t, f.authorize(t, "session", "overlay", "close", "--pane", "right"), "not bound to "+testHost)
	wantRefuse(t, f.authorize(t, "session", "overlay", "close", "--pane-id", mixedRight), "--pane-id is not allowed")
}

func TestAuthorizeRefusedLeavesAndOptions(t *testing.T) {
	tests := []struct {
		name string
		argv string
		want string
	}{
		{"scratch", "session scratch on", "`session scratch` is not available"},
		{"restart", "session restart", "`session restart` is not available"},
		{"paste", "session paste", "`session paste` is not available"},
		{"reveal", "session reveal", "`session reveal` is not available"},
		{"duplicate", "session duplicate", "`session duplicate` is not available"},
		{"split close", "session split close", "`session split close` is not available"},
		{"swap", "session swap", "`session swap` is not available"},
		{"overlay navigate", "session overlay navigate browser", "`session overlay navigate` is not available"},
		{"run-job", "session overlay run-job x", "`session overlay run-job` is not available"},
		{"hud --file", "session hud --file /etc/passwd", "--file is not allowed"},
		{"hud open --file", "session hud open --file /etc/passwd", "--file is not allowed"},
		{"background", "session background image /x.png", "`session background` is not available"},
		{"persistent", "session overlay open --url https://a.b --persistent", "--persistent is not allowed"},
		{"browse", "session overlay open --url https://a.b --browse", "--browse is not allowed"},
		{"pick", "pick open", "`pick` is not available"},
		{"ask", "ask open title", "`ask` is not available"},
		{"dashboard", "dashboard", "`dashboard` is not available"},
		{"unknown leaf", "session frobnicate", "not in the allowlist"},
		{"overlay cwd", "session overlay open --html @0 --cwd /tmp", "--cwd is not allowed"},
		{"hud follow", "session hud open hi --follow", "--follow is not allowed"},
		{"new after", "session new --after " + rowSib, "--after is not allowed"},
		{"new create-workspace", "session new --create-workspace", "--create-workspace is not allowed"},
		{"type select", "session type --select hi", "--select is not allowed"},
		{"type pane", "session type --pane left hi", "--pane is not allowed"},
		{"url with js", "session overlay open --url https://a.b --js", "--js is not allowed with --url"},
		{"url file scheme", "session overlay open --url file:///etc/passwd", "http or https"},
		{"url no host", "session overlay open --url http://", "http or https"},
		{"two sources", "session overlay open --html @0 --url https://a.b", "exactly one"},
		{"no source", "session overlay open --block", "exactly one"},
		{"html not a reference", "session overlay open --html /etc/passwd", "not a file reference"},
		{"html missing index", "session overlay open --html @3", "did not send"},
		{"html leading zero", "session overlay open --html @01", "not a file reference"},
		{"empty command", "session new --command=", "is empty"},
		{"empty target", "session text --target=", "--target is empty"},
		{"status bad state", "session status loud", "not allowed"},
		{"split visibility bad arg", "session split visibility toggle", "not allowed"},
		{"notify without body", "notify", "needs 1 argument"},
		{"too many positionals", "session rename a b", "at most 1"},
		{"target twice", fmt.Sprintf("session text --target %s --target %s", rowCaller, rowSib), "more than once"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			wantRefuse(t, f.authorize(t, argvOf(t, tt.argv)...), tt.want)
		})
	}
}

func TestAuthorizeTargets(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{"prefix", []string{"session", "text", "--target", rowSib[:8]}, "full row id"},
		{"active", []string{"session", "text", "--target", "active"}, "full row id"},
		{"unknown row", []string{"session", "text", "--target", testID + "77"}, "no such row"},
		{"pane prefix", []string{"session", "text", "--pane-id", paneCaller[:8]}, "full pane id"},
		{"pane of another row", []string{"session", "text", "--pane-id", paneSib}, "is not in row"},
		{"row bound to another host", []string{"session", "text", "--target", rowOther}, "bound to another host"},
		{"local row read", []string{"session", "text", "--target", rowAgent, "--pane-id", paneAgent}, "reads only its own panes"},
		{"local row write", []string{"session", "overlay", "close", "--target", rowAgent}, "not bound to " + testHost},
		{"local row restore", []string{"session", "restore", "x", "--target", rowAgent, "--pane-id", paneAgent}, "writes only to its own panes"},
		{"unbound split pane read", []string{"session", "text", "--target", rowMixed, "--pane-id", mixedRight}, "reads only its own panes"},
		{"unbound split pane overlay", []string{"session", "overlay", "close", "--target", rowMixed, "--pane", "right"}, "not bound to " + testHost},
		{"detached pane text", []string{"session", "text", "--target", rowDetached, "--pane-id", paneDetached}, "not attached"},
		{"detached pane type", []string{"session", "type", "--target", rowDetached, "--pane-id", paneDetached, "hi"}, "not attached"},
		{"detached pane overlay", []string{"session", "overlay", "close", "--target", rowDetached}, "not attached"},
		{"close with an unowned target", []string{"session", "close", "--target", rowCaller, "--target", rowAgent}, "not bound to " + testHost},
		{"close a detached row", []string{"session", "close", "--target", rowDetached}, "not attached"},
		{"close another host's row", []string{"session", "close", "--target", rowOther}, "bound to another host"},
		{"rename a row with an unowned split", []string{"session", "rename", "x", "--target", rowMixed}, "every pane of the row"},
		{"resize a row with one owned and one local pane", []string{"session", "overlay", "resize", "--full", "--target", rowMixed}, "every pane of the row"},
		{"hud on a mixed row", []string{"session", "hud", "hi", "--target", rowMixed}, "every pane of the row"},
		{"notify another row", []string{"notify", "hi", "--target", rowSib}, "caller's own row"},
		{"pane and side disagree", []string{"session", "status", "idle", "--pane", "right", "--pane-id", paneCaller}, "has no right pane"},
		{"status pane of another row", []string{"session", "status", "idle", "--pane-id", paneSib}, "is not in row"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			wantRefuse(t, f.authorize(t, tt.argv...), tt.want)
		})
	}
}

func TestAuthorizeSelfMarkersMapStaleIDs(t *testing.T) {
	f := newFixture(t)
	f.hdr.Self = Self{Row: testID + "60", PaneID: testID + "61"} // inherited from an earlier attach
	got := f.authorize(t, "session", "type", "--target", testID+"60", "--pane-id", testID+"61", "hi")
	wantAllow(t, got)
	want := []Target{{Row: rowCaller, PaneID: paneCaller}}
	if !reflect.DeepEqual(got.Call.Targets, want) {
		t.Fatalf("Targets = %#v, want %#v", got.Call.Targets, want)
	}
	got = f.authorize(t, "session", "overlay", "close", "--target", strings.ToUpper(testID+"60"))
	wantAllow(t, got)
	if got.Call.Targets[0].Row != rowCaller {
		t.Fatalf("Targets = %#v, want the caller's row (markers match case-insensitively)", got.Call.Targets)
	}
}

func TestAuthorizeFullIDsAreCaseInsensitive(t *testing.T) {
	f := newFixture(t)
	f.rows[1].ID = strings.ToLower(rowSib)
	got := f.authorize(t, "session", "text", "--target", strings.ToUpper(rowSib))
	wantAllow(t, got)
	if got.Call.Targets[0].Row != rowSib {
		t.Fatalf("Targets = %#v, want the tree's spelling of the id", got.Call.Targets)
	}
}

func TestAuthorizeCallerRowGone(t *testing.T) {
	f := newFixture(t)
	f.rows = f.rows[1:]
	wantRefuse(t, f.authorize(t, "session", "rename", "x"), "no such row")
	wantRefuse(t, f.authorize(t, "notify", "hi"), "your row is gone")
	wantAllow(t, f.authorize(t, "tree"))
}

func TestAuthorizeStateReadFailures(t *testing.T) {
	f := newFixture(t)
	ctrl := gomock.NewController(t)
	tree := mocks.NewMockTreeReader(ctrl)
	tree.EXPECT().TreeRows(gomock.Any()).Return(nil, errors.New("socket closed"))
	binds := mocks.NewMockBindingLoader(ctrl)
	got := NewPolicy(tree, binds).Authorize(context.Background(), testHost, f.caller, f.hdr, []string{"session", "text"}, nil)
	wantRefuse(t, got, "cannot read agterm state")

	tree = mocks.NewMockTreeReader(ctrl)
	tree.EXPECT().TreeRows(gomock.Any()).Return(f.rows, nil)
	binds = mocks.NewMockBindingLoader(ctrl)
	binds.EXPECT().Load().Return(nil, errors.New("locked"))
	got = NewPolicy(tree, binds).Authorize(context.Background(), testHost, f.caller, f.hdr, []string{"session", "text"}, nil)
	wantRefuse(t, got, "cannot read agterm state")
}

func TestAuthorizeRowLevelRefusesLocalScratch(t *testing.T) {
	f := newFixture(t)
	// a scratch terminal is a local Mac shell the host cannot own, so a
	// row-level call that would take it down is refused.
	f.row(t, rowCaller).Surfaces = surfaces(t, "left", paneCaller, "scratch", testID+"40")
	wantRefuse(t, f.authorize(t, "session", "rename", "x"), "local scratch terminal")
	wantRefuse(t, f.authorize(t, "session", "close"), "local scratch terminal")
}

func TestAuthorizeRefusesWhenCallerBindingIsGone(t *testing.T) {
	f := newFixture(t)
	f.grant(t, bindings.Grant{Row: rowAgent, PaneID: paneAgent, Kind: bindings.GrantConsent})
	wantAllow(t, f.authorize(t, "session", "type", "--target", rowAgent, "hi"))
	// agr end removed the binding while the body was still uploading.
	var left []bindings.Binding
	for _, b := range f.binds {
		if b.Name != f.caller.Name {
			left = append(left, b)
		}
	}
	f.binds = left
	f.caller.Grants = nil
	ctrl := gomock.NewController(t)
	tree := mocks.NewMockTreeReader(ctrl)
	tree.EXPECT().TreeRows(gomock.Any()).Return(f.rows, nil).AnyTimes()
	binds := mocks.NewMockBindingLoader(ctrl)
	binds.EXPECT().Load().Return(f.binds, nil).AnyTimes()
	stale := f.caller
	stale.Grants = []bindings.Grant{{Row: rowAgent, PaneID: paneAgent, Kind: bindings.GrantConsent}}
	got := NewPolicy(tree, binds).Authorize(context.Background(), testHost, stale, f.hdr, []string{"session", "type", "--target", rowAgent, "hi"}, nil)
	wantRefuse(t, got, "binding no longer exists")
}

func TestAuthorizeConsentPaneResolution(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		wantPane string
		wantErr  []string
	}{
		{"pane id given", []string{"session", "type", "--target", rowTwoAgents, "--pane-id", twoAgentsR, "hi"}, twoAgentsR, nil},
		{"omitted, one program pane", []string{"session", "type", "--target", rowShellCod, "hi"}, shellCodR, nil},
		{"omitted, one pane at all", []string{"session", "type", "--target", rowAgent, "hi"}, paneAgent, nil},
		{"omitted, two program panes", []string{"session", "type", "--target", rowTwoAgents, "hi"}, "", []string{"2 candidate panes", twoAgentsL, twoAgentsR}},
		{"omitted, zero program panes", []string{"session", "type", "--target", rowTwoShells, "hi"}, "", []string{"0 candidate panes", twoShellsL, twoShellsR}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			got := f.authorize(t, tt.argv...)
			if tt.wantErr != nil {
				if got.Verdict != Refuse || got.Consent != nil {
					t.Fatalf("verdict = %v, consent = %v, want a plain refusal", got.Verdict, got.Consent)
				}
				for _, part := range tt.wantErr {
					if !strings.Contains(got.Reason, part) {
						t.Fatalf("reason = %q, want it to contain %q", got.Reason, part)
					}
				}
				return
			}
			if got.Verdict != NeedsConsent || got.Consent == nil {
				t.Fatalf("verdict = %v (%s), want NeedsConsent", got.Verdict, got.Reason)
			}
			if got.Consent.PaneID != tt.wantPane {
				t.Fatalf("Consent.PaneID = %s, want %s", got.Consent.PaneID, tt.wantPane)
			}
		})
	}
}

func TestAuthorizeConsentDetails(t *testing.T) {
	f := newFixture(t)
	got := f.authorizeStdin(t, "line one\nline\ttwo\n", "session", "type", "--stdin", "--target", rowAgent, "--pane-id", paneAgent)
	if got.Verdict != NeedsConsent {
		t.Fatalf("verdict = %v (%s), want NeedsConsent", got.Verdict, got.Reason)
	}
	want := &Consent{Row: rowAgent, RowName: "orchestrator", PaneID: paneAgent,
		Text: "line one\nline\ttwo\n", Preview: "line one⏎line⇥two⏎"}
	if !reflect.DeepEqual(got.Consent, want) {
		t.Fatalf("Consent = %#v, want %#v", got.Consent, want)
	}
	line := strings.Join(got.Call.Args(), " ")
	if line != fmt.Sprintf("session type --target=%s --pane-id=%s --stdin", rowAgent, paneAgent) || got.Call.Granted {
		t.Fatalf("Call = %q (granted %t), want the injected ids and no grant", line, got.Call.Granted)
	}
}

func TestPreviewCutsLongText(t *testing.T) {
	long := strings.Repeat("é", 301)
	got := Preview(long)
	want := strings.Repeat("é", 300) + "… (301 characters)"
	if got != want {
		t.Fatalf("Preview() = %q, want %q", got, want)
	}
	if exact := strings.Repeat("a", 300); Preview(exact) != exact {
		t.Fatalf("Preview() cut a 300 character text")
	}
}

func TestAuthorizeConsentRefusals(t *testing.T) {
	f := newFixture(t)
	typeInto := func(row, pane, stdin string) Decision {
		t.Helper()
		return f.authorizeStdin(t, stdin, "session", "type", "--stdin", "--target", row, "--pane-id", pane)
	}
	// a pane bound to any host: no consent
	got := typeInto(rowOther, paneOther, "hi")
	wantRefuse(t, got, "bound to another host")
	if got.Consent != nil {
		t.Fatal("a pane bound to another host must not ask the user")
	}
	// a shell, python3, vim, an unknown foreground, a scratch surface
	f.row(t, rowAgent).Foreground = nil
	f.row(t, rowAgent).ForegroundShell = "zsh"
	wantRefuse(t, typeInto(rowAgent, paneAgent, "hi"), "at a shell (zsh)")
	for _, program := range []string{"python3", "vim", "less"} {
		f.row(t, rowAgent).Foreground = []string{"/usr/bin/" + program}
		got = typeInto(rowAgent, paneAgent, "hi")
		wantRefuse(t, got, fmt.Sprintf("runs %q", program))
		if got.Consent != nil {
			t.Fatalf("%s must not ask the user", program)
		}
	}
	f.row(t, rowAgent).Foreground = nil
	f.row(t, rowAgent).ForegroundShell = ""
	wantRefuse(t, typeInto(rowAgent, paneAgent, "hi"), "unknown")
	wantRefuse(t, typeInto(rowScratch, scratchPane, "hi"), "no readable program")
	// a sanitiser failure: no consent
	got = typeInto(rowTwoAgents, twoAgentsL, "bad\x1b[Z")
	wantRefuse(t, got, "control character")
	if got.Consent != nil {
		t.Fatal("a sanitiser failure must not ask the user")
	}
	// text from both sources, or none
	wantRefuse(t, f.authorizeStdin(t, "x", "session", "type", "--stdin", "--target", rowTwoAgents, "--pane-id", twoAgentsL, "y"), "not both")
	wantRefuse(t, f.authorize(t, "session", "type", "--target", rowTwoAgents, "--pane-id", twoAgentsL), "no text")
	// every other write leaf on a local pane
	for _, argv := range []string{
		"session text --target " + rowTwoAgents + " --pane-id " + twoAgentsL,
		"session overlay close --target " + rowTwoAgents,
		"session overlay submit --value x --target " + rowTwoAgents,
		"session restore x --target " + rowTwoAgents + " --pane-id " + twoAgentsL,
		"session status idle --target " + rowTwoAgents,
		"session close --target " + rowTwoAgents,
		"notify hi --target " + rowTwoAgents,
	} {
		got = f.authorize(t, argvOf(t, argv)...)
		if got.Verdict != Refuse || got.Consent != nil {
			t.Fatalf("%s: verdict = %v consent = %v, want a plain refusal", argv, got.Verdict, got.Consent)
		}
	}
}

func TestAuthorizeProgramRulePerSurface(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(row *agterm.TreeRow)
		pane   string
		want   string // "" = needs consent
	}{
		{"left uses foreground", func(r *agterm.TreeRow) {}, twoAgentsL, ""},
		{"right uses splitForeground", func(r *agterm.TreeRow) {}, twoAgentsR, ""},
		{"left ignores splitForeground", func(r *agterm.TreeRow) { r.Foreground = nil; r.ForegroundShell = "zsh" }, twoAgentsL, "at a shell"},
		{"right ignores foreground", func(r *agterm.TreeRow) { r.SplitForeground = nil; r.SplitForegroundShell = "zsh" }, twoAgentsR, "at a shell"},
		{"right without hasSplit is unknown", func(r *agterm.TreeRow) { r.HasSplit = false }, twoAgentsR, "no readable program"},
		{"codex on the left", func(r *agterm.TreeRow) { r.Foreground = []string{"codex"} }, twoAgentsL, ""},
		{"basename is compared", func(r *agterm.TreeRow) { r.Foreground = []string{"/opt/x/claude-wrapper"} }, twoAgentsL, "runs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			tt.mutate(f.row(t, rowTwoAgents))
			got := f.authorize(t, "session", "type", "--target", rowTwoAgents, "--pane-id", tt.pane, "hi")
			if tt.want == "" {
				if got.Verdict != NeedsConsent {
					t.Fatalf("verdict = %v (%s), want NeedsConsent", got.Verdict, got.Reason)
				}
				return
			}
			wantRefuse(t, got, tt.want)
		})
	}
}

func TestAuthorizeGrants(t *testing.T) {
	for _, kind := range []string{bindings.GrantOrchestrator, bindings.GrantConsent} {
		t.Run(kind+" allowed for claude and codex", func(t *testing.T) {
			f := newFixture(t)
			f.grant(t,
				bindings.Grant{Row: rowAgent, PaneID: paneAgent, Kind: kind},
				bindings.Grant{Row: rowTwoAgents, PaneID: twoAgentsR, Kind: kind})
			got := f.authorizeStdin(t, "hello\n", "session", "type", "--stdin", "--target", rowAgent, "--pane-id", paneAgent)
			wantAllow(t, got)
			if !got.Call.Granted {
				t.Fatal("Call.Granted = false")
			}
			if got.Consent == nil || got.Consent.Row != rowAgent || got.Consent.PaneID != paneAgent || got.Consent.Text != "hello\n" {
				t.Fatalf("Consent = %+v, want the granted pane and text for the notice", got.Consent)
			}
			got = f.authorize(t, "session", "type", "--target", rowTwoAgents, "--pane-id", twoAgentsR, "hello")
			wantAllow(t, got)
		})

		t.Run(kind+" refused outside an agent", func(t *testing.T) {
			f := newFixture(t)
			f.grant(t, bindings.Grant{Row: rowAgent, PaneID: paneAgent, Kind: kind})
			row := f.row(t, rowAgent)
			cases := []struct {
				name       string
				fg         []string
				shell      string
				wantSubstr string
			}{
				{"shell", nil, "zsh", "at a shell"},
				{"python3", []string{"python3", "-i"}, "", `runs "python3"`},
				{"vim", []string{"/usr/bin/vim"}, "", `runs "vim"`},
				{"unknown", nil, "", "unknown"},
			}
			for _, c := range cases {
				row.Foreground, row.ForegroundShell = c.fg, c.shell
				got := f.authorize(t, "session", "type", "--target", rowAgent, "--pane-id", paneAgent, "hi")
				if got.Verdict != Refuse || !strings.Contains(got.Reason, c.wantSubstr) {
					t.Fatalf("%s: verdict = %v reason = %q, want a refusal containing %q", c.name, got.Verdict, got.Reason, c.wantSubstr)
				}
			}
		})

		t.Run(kind+" refused on a scratch surface", func(t *testing.T) {
			f := newFixture(t)
			f.grant(t, bindings.Grant{Row: rowScratch, PaneID: scratchPane, Kind: kind})
			wantRefuse(t, f.authorize(t, "session", "type", "--target", rowScratch, "--pane-id", scratchPane, "hi"), "no readable program")
		})

		t.Run(kind+" does not cover another pane of the row", func(t *testing.T) {
			f := newFixture(t)
			f.grant(t, bindings.Grant{Row: rowTwoAgents, PaneID: twoAgentsL, Kind: kind})
			got := f.authorize(t, "session", "type", "--target", rowTwoAgents, "--pane-id", twoAgentsR, "hi")
			if got.Verdict == Allow {
				t.Fatal("a grant for one pane allowed typing into another")
			}
			f.row(t, rowTwoAgents).SplitForeground = nil
			f.row(t, rowTwoAgents).SplitForegroundShell = "zsh"
			wantRefuse(t, f.authorize(t, "session", "type", "--target", rowTwoAgents, "--pane-id", twoAgentsR, "hi"), "at a shell")
		})

		t.Run(kind+" covers session type only", func(t *testing.T) {
			f := newFixture(t)
			f.grant(t, bindings.Grant{Row: rowAgent, PaneID: paneAgent, Kind: kind})
			for _, argv := range []string{
				"notify hi --target " + rowAgent,
				"session text --target " + rowAgent + " --pane-id " + paneAgent,
				"session overlay open --html @0 --target " + rowAgent,
				"session overlay close --target " + rowAgent,
				"session restore x --target " + rowAgent + " --pane-id " + paneAgent,
				"session status idle --target " + rowAgent,
				"session close --target " + rowAgent,
			} {
				if got := f.authorize(t, argvOf(t, argv)...); got.Verdict != Refuse {
					t.Fatalf("%s: verdict = %v, want Refuse", argv, got.Verdict)
				}
			}
		})
	}
}

func TestAuthorizeGrantForAPaneBoundToAHostIsUnusable(t *testing.T) {
	f := newFixture(t)
	f.grant(t, bindings.Grant{Row: rowOther, PaneID: paneOther, Kind: bindings.GrantConsent})
	got := f.authorize(t, "session", "type", "--target", rowOther, "--pane-id", paneOther, "hi")
	wantRefuse(t, got, "bound to another host")
	if got.Consent != nil {
		t.Fatal("must not ask the user")
	}
}

func TestAuthorizeReportsGoneGrantsForDropping(t *testing.T) {
	f := newFixture(t)
	gone := bindings.Grant{Row: rowGone, PaneID: paneGone, Kind: bindings.GrantConsent}
	gonePane := bindings.Grant{Row: rowAgent, PaneID: testID + "88", Kind: bindings.GrantOrchestrator}
	live := bindings.Grant{Row: rowAgent, PaneID: paneAgent, Kind: bindings.GrantConsent}
	f.grant(t, gone, gonePane, live)

	got := f.authorize(t, "session", "type", "--target", rowGone, "--pane-id", paneGone, "hi")
	wantRefuse(t, got, "no such row")
	if want := []bindings.Grant{gone, gonePane}; !reflect.DeepEqual(got.Drop, want) {
		t.Fatalf("Drop = %v, want %v", got.Drop, want)
	}

	got = f.authorize(t, "session", "type", "--target", rowAgent, "--pane-id", testID+"88", "hi")
	wantRefuse(t, got, "is not in row")
	if len(got.Drop) != 2 {
		t.Fatalf("Drop = %v, want the two gone grants", got.Drop)
	}

	// the live grant still works, and the gone ones are reported on any call
	got = f.authorize(t, "session", "type", "--target", rowAgent, "--pane-id", paneAgent, "hi")
	wantAllow(t, got)
	if len(got.Drop) != 2 {
		t.Fatalf("Drop = %v, want the two gone grants", got.Drop)
	}
}

func TestSanitizeTyped(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		wantErr string
	}{
		{"plain", "hello world", ""},
		{"newline and tab", "a\n\tb\n", ""},
		{"unicode text", "héllo — 日本語 🙂", ""},
		{"escape", "a\x1bb", "U+001B"},
		{"shift-tab", "\x1b[Z", "U+001B"},
		{"paste end", "\x1b[201~", "U+001B"},
		{"carriage return", "a\rb", "U+000D"},
		{"NUL", "a\x00b", "U+0000"},
		{"backspace", "a\bb", "U+0008"},
		{"DEL", "a\x7fb", "U+007F"},
		{"C1 control", "a\u009bb", "U+009B"},
		{"bidi override", "a\u202eb", "U+202E"},
		{"bidi isolate", "a\u2066b", "U+2066"},
		{"zero-width space", "a\u200bb", "U+200B"},
		{"zero-width joiner", "a\u200db", "U+200D"},
		{"byte order mark", "\xef\xbb\xbfa", "U+FEFF"},
		{"invalid utf-8", "a\xffb", "UTF-8"},
		{"limit", strings.Repeat("a", MaxTypedText), ""},
		{"over the limit", strings.Repeat("a", MaxTypedText+1), "at most"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SanitizeTyped([]byte(tt.text))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("SanitizeTyped() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("SanitizeTyped() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestAuthorizeSanitisesGrantedAndConsentedTypingOnly(t *testing.T) {
	f := newFixture(t)
	f.grant(t, bindings.Grant{Row: rowAgent, PaneID: paneAgent, Kind: bindings.GrantOrchestrator})
	bad := "go\x1b[Z"
	wantRefuse(t, f.authorizeStdin(t, bad, "session", "type", "--stdin", "--target", rowAgent, "--pane-id", paneAgent), "control character")
	wantRefuse(t, f.authorizeStdin(t, bad, "session", "type", "--stdin", "--target", rowTwoAgents, "--pane-id", twoAgentsL), "control character")
	wantRefuse(t, f.authorizeStdin(t, strings.Repeat("a", MaxTypedText+1), "session", "type", "--stdin", "--target", rowAgent, "--pane-id", paneAgent), "at most")
	wantAllow(t, f.authorizeStdin(t, strings.Repeat("a", MaxTypedText), "session", "type", "--stdin", "--target", rowAgent, "--pane-id", paneAgent))
	// an owned pane takes any text
	wantAllow(t, f.authorizeStdin(t, bad, "session", "type", "--stdin"))
}

func TestCheckAgentAppliesTheProgramRuleAgain(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	policy := f.policy(t)
	ctx := context.Background()
	if err := policy.CheckAgent(ctx, rowAgent, paneAgent); err != nil {
		t.Fatalf("CheckAgent() on a claude pane error = %v", err)
	}
	if err := policy.CheckAgent(ctx, rowShellCod, shellCodR); err != nil {
		t.Fatalf("CheckAgent() on the codex split pane error = %v", err)
	}
	for _, tc := range []struct {
		name, row, pane, want string
	}{
		{"shell pane", rowShellCod, shellCodL, "at a shell"},
		{"scratch surface", rowScratch, scratchPane, "no readable program"},
		{"pane bound to a host", rowSib, paneSib, "bound to a host"},
		{"pane not in the row", rowAgent, paneGone, "is gone"},
		{"row gone", rowGone, paneGone, "is gone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := policy.CheckAgent(ctx, tc.row, tc.pane); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CheckAgent() error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	f.row(t, rowAgent).Foreground = nil
	f.row(t, rowAgent).ForegroundShell = "zsh"
	if err := f.policy(t).CheckAgent(ctx, rowAgent, paneAgent); err == nil {
		t.Fatal("CheckAgent() after the pane became a shell error = nil")
	}
}

func TestCheckAgentReportsStateReadFailures(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	tree := mocks.NewMockTreeReader(ctrl)
	binds := mocks.NewMockBindingLoader(ctrl)
	tree.EXPECT().TreeRows(gomock.Any()).Return(nil, errors.New("agterm down"))
	if err := NewPolicy(tree, binds).CheckAgent(context.Background(), rowAgent, paneAgent); err == nil || !strings.Contains(err.Error(), "agterm down") {
		t.Fatalf("CheckAgent() error = %v, want the tree failure", err)
	}
	tree.EXPECT().TreeRows(gomock.Any()).Return(nil, nil)
	binds.EXPECT().Load().Return(nil, errors.New("disk"))
	if err := NewPolicy(tree, binds).CheckAgent(context.Background(), rowAgent, paneAgent); err == nil || !strings.Contains(err.Error(), "disk") {
		t.Fatalf("CheckAgent() error = %v, want the binding failure", err)
	}
}

func TestPreviewPartsCutsAtThreeHundredCharacters(t *testing.T) {
	t.Helper()
	shown, total, cut := PreviewParts(strings.Repeat("é", 300))
	if cut || total != 300 || len([]rune(shown)) != 300 {
		t.Fatalf("PreviewParts(300) = %d runes, total %d, cut %v, want it whole", len([]rune(shown)), total, cut)
	}
	shown, total, cut = PreviewParts("a\nb\tc" + strings.Repeat("x", 400))
	if !cut || total != 405 || len([]rune(shown)) != 300 || !strings.HasPrefix(shown, "a⏎b⇥c") {
		t.Fatalf("PreviewParts(405) = %q (%d runes), total %d, cut %v, want 300 runes with ⏎ and ⇥", shown, len([]rune(shown)), total, cut)
	}
}

func TestAttachedAcceptsOpenFlagsAnywhere(t *testing.T) {
	b := bind(t, rowCaller, paneCaller, "left", testHost, "w1")
	tests := []struct {
		name string
		fg   []string
		want bool
	}{
		{"plain", []string{"agr", "open", testHost, "w1"}, true},
		{"no name", []string{"agr", "open", testHost}, true},
		{"flag before host", []string{"agr", "open", "--cwd", "/srv", testHost, "w1"}, true},
		{"flag between", []string{"agr", "open", testHost, "--parent", "p", "w1"}, true},
		{"flags after", []string{"agr", "open", testHost, "w1", "--command", "claude", "--orchestrator", "x"}, true},
		{"other host", []string{"agr", "open", "--cwd", "/srv", "cloud", "w1"}, false},
		{"other name", []string{"agr", "open", "--cwd", "/srv", testHost, "w2"}, false},
		{"flag without value", []string{"agr", "open", testHost, "--cwd"}, false},
		{"only flags", []string{"agr", "open", "--cwd", "/srv"}, false},
		{"not open", []string{"agr", "run", "--cwd", "/srv", testHost}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := agterm.TreeRow{ID: rowCaller, Surfaces: surfaces(t, "left", paneCaller), Foreground: tt.fg}
			if got := attached(row, b); got != tt.want {
				t.Fatalf("attached(%v) = %v, want %v", tt.fg, got, tt.want)
			}
		})
	}
}
