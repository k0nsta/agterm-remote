package proxy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/k0nsta/agterm-remote/internal/proxy/mocks"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

const testAgr = "/opt/homebrew/bin/agr"

func (f *fixture) rewriter(t *testing.T, sessions ...string) *Rewriter {
	t.Helper()
	ctrl := gomock.NewController(t)
	lister := mocks.NewMockSessionLister(ctrl)
	var existing []remote.Session
	for _, name := range sessions {
		existing = append(existing, remote.Session{Name: name})
	}
	lister.EXPECT().Sessions(gomock.Any(), testHost).Return(existing, nil).AnyTimes()
	binds := mocks.NewMockBindingLoader(ctrl)
	binds.EXPECT().Load().Return(f.binds, nil).AnyTimes()
	return &Rewriter{Agr: testAgr, Root: filepath.Join(t.TempDir(), "proxy"), Sessions: lister, Binds: binds}
}

func (f *fixture) request(t *testing.T, stdin string) Request {
	t.Helper()
	return Request{
		Host: testHost, Session: f.caller.Name, Caller: f.caller, Self: f.hdr.Self, CWD: f.hdr.CWD,
		Files: [][]byte{[]byte("<html><body>hi</body></html>")}, Stdin: []byte(stdin),
	}
}

// prepare authorizes argv and rewrites the resulting call.
func (f *fixture) prepare(t *testing.T, rewriter *Rewriter, argv ...string) Prepared {
	t.Helper()
	decision := f.authorize(t, argv...)
	wantAllow(t, decision)
	prepared, err := rewriter.Rewrite(context.Background(), f.request(t, ""), decision.Call)
	if err != nil {
		t.Fatalf("Rewrite(%v) error = %v", argv, err)
	}
	return prepared
}

func TestRewriteSessionNewBecomesAgrOpen(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	prepared := f.prepare(t, f.rewriter(t), "session", "new", "--name", "w9", "--cwd", "/home/u/p",
		"--command", "make test", "--no-select")
	command := `'/opt/homebrew/bin/agr' 'open' 'myhost' 'w9' '--cwd' '/home/u/p' '--command' 'make test'`
	want := []string{"session", "new", "--name=w9", "--no-select", "--command=" + command}
	if !reflect.DeepEqual(prepared.Args, want) {
		t.Fatalf("Args = %q, want %q", prepared.Args, want)
	}
	if prepared.NewRow == nil || prepared.NewRow.Host != testHost || prepared.NewRow.Name != "w9" {
		t.Fatalf("NewRow = %+v, want myhost/w9", prepared.NewRow)
	}
}

func TestRewriteSessionNewWithoutCommandOrCwd(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	prepared := f.prepare(t, f.rewriter(t), "session", "new", "--name", "w9")
	want := []string{"session", "new", "--name=w9", "--command=" + `'/opt/homebrew/bin/agr' 'open' 'myhost' 'w9'`}
	if !reflect.DeepEqual(prepared.Args, want) {
		t.Fatalf("Args = %q, want %q", prepared.Args, want)
	}
}

func TestRewriteSessionNewNames(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name     string
		argv     []string
		existing []string
		want     string
	}{
		{name: "requested and free", argv: []string{"--name", "build"}, want: "build"},
		{name: "requested but taken", argv: []string{"--name", "build"}, existing: []string{"build"}, want: "build-2"},
		{name: "requested but taken twice", argv: []string{"--name", "build"}, existing: []string{"build", "build-2"}, want: "build-3"},
		{name: "invalid token uses the caller's name", argv: []string{"--name", "my build"}, want: "w1-2"},
		{name: "no name uses the caller's name", want: "w1-2"},
		{name: "caller's name taken on the host", existing: []string{"w1-2"}, want: "w1-3"},
		{name: "a bound name is taken too", argv: []string{"--name", "w2"}, want: "w2-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			prepared := f.prepare(t, f.rewriter(t, tc.existing...), append([]string{"session", "new"}, tc.argv...)...)
			if prepared.NewRow == nil || prepared.NewRow.Name != tc.want {
				t.Fatalf("NewRow = %+v, want name %q", prepared.NewRow, tc.want)
			}
			if !strings.Contains(prepared.Args[len(prepared.Args)-1], "'open' 'myhost' '"+tc.want+"'") {
				t.Fatalf("command %q does not open %s", prepared.Args[len(prepared.Args)-1], tc.want)
			}
		})
	}
}

func TestRewriteSessionNewSessionListFailureIsRefused(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	ctrl := gomock.NewController(t)
	lister := mocks.NewMockSessionLister(ctrl)
	lister.EXPECT().Sessions(gomock.Any(), testHost).Return(nil, errors.New("ssh: no route"))
	binds := mocks.NewMockBindingLoader(ctrl)
	binds.EXPECT().Load().Return(f.binds, nil).AnyTimes()
	rewriter := &Rewriter{Agr: testAgr, Root: t.TempDir(), Sessions: lister, Binds: binds}
	decision := f.authorize(t, "session", "new")
	if _, err := rewriter.Rewrite(context.Background(), f.request(t, ""), decision.Call); err == nil || !strings.Contains(err.Error(), "no route") {
		t.Fatalf("Rewrite() error = %v, want the session list failure", err)
	}
}

func TestRewriteOverlayCommandBecomesAgrRun(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	prepared := f.prepare(t, f.rewriter(t), "session", "overlay", "open", "--block", "--", `vim 'a b' "it's"`)
	command := `'/opt/homebrew/bin/agr' 'run' 'myhost' '--cwd' '/home/u' '--' 'sh' '-c' 'vim '\''a b'\'' "it'\''s"'`
	want := []string{"session", "overlay", "open", "--target=" + rowCaller, "--block", command}
	if !reflect.DeepEqual(prepared.Args, want) {
		t.Fatalf("Args = %q, want %q", prepared.Args, want)
	}
	if want := []string{"session", "overlay", "close", "--target=" + rowCaller}; !reflect.DeepEqual(prepared.Cancel, want) {
		t.Fatalf("Cancel = %q, want %q", prepared.Cancel, want)
	}
}

func TestRewriteOverlayCommandOmitsEmptyCwdAndKeepsSide(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	f.caller = bind(t, rowMixed, mixedLeft, "left", testHost, "w3")
	f.hdr.Self = Self{Row: rowMixed, PaneID: mixedLeft}
	f.hdr.CWD = ""
	rewriter := f.rewriter(t)
	decision := f.authorize(t, "session", "overlay", "open", "--block", "top")
	wantAllow(t, decision)
	request := f.request(t, "")
	request.CWD = ""
	prepared, err := rewriter.Rewrite(context.Background(), request, decision.Call)
	if err != nil {
		t.Fatalf("Rewrite() error = %v", err)
	}
	command := `'/opt/homebrew/bin/agr' 'run' 'myhost' '--' 'sh' '-c' 'top'`
	want := []string{"session", "overlay", "open", "--target=" + rowMixed, "--pane=left", "--block", command}
	if !reflect.DeepEqual(prepared.Args, want) {
		t.Fatalf("Args = %q, want %q", prepared.Args, want)
	}
	if want := []string{"session", "overlay", "close", "--target=" + rowMixed, "--pane=left"}; !reflect.DeepEqual(prepared.Cancel, want) {
		t.Fatalf("Cancel = %q, want %q", prepared.Cancel, want)
	}
}

func TestRewriteRestoreBecomesAgrOpenCommand(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	prepared := f.prepare(t, f.rewriter(t), "session", "restore", "claude --resume")
	command := `'/opt/homebrew/bin/agr' 'open' 'myhost' 'w1' '--command' 'claude --resume'`
	want := []string{"session", "restore", "--target=" + rowCaller, "--pane-id=" + paneCaller, command}
	if !reflect.DeepEqual(prepared.Args, want) {
		t.Fatalf("Args = %q, want %q", prepared.Args, want)
	}
}

func TestRewriteRestoreWithoutCommandPassesThrough(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	prepared := f.prepare(t, f.rewriter(t), "session", "restore", "--none")
	want := []string{"session", "restore", "--target=" + rowCaller, "--pane-id=" + paneCaller, "--none"}
	if !reflect.DeepEqual(prepared.Args, want) {
		t.Fatalf("Args = %q, want %q", prepared.Args, want)
	}
}

func TestRewriteURLWarnsForLocalHosts(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		url  string
		warn bool
	}{
		{"http://localhost:8080/x", true},
		{"http://127.0.0.1:3000", true},
		{"http://127.5.5.5", true},
		{"http://[::1]:80/", true},
		{"http://0.0.0.0:9", true},
		{"http://app.localhost", true},
		{"https://example.com/", false},
		{"http://192.168.1.5/", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			f := newFixture(t)
			prepared := f.prepare(t, f.rewriter(t), "session", "overlay", "open", "--url", tc.url)
			if got := len(prepared.Warnings) == 1; got != tc.warn {
				t.Fatalf("Warnings = %q, want warning %v", prepared.Warnings, tc.warn)
			}
			if tc.warn && !strings.Contains(prepared.Warnings[0], tc.url) {
				t.Fatalf("warning %q does not name the URL", prepared.Warnings[0])
			}
			if want := []string{"session", "overlay", "open", "--target=" + rowCaller, "--url=" + tc.url}; !reflect.DeepEqual(prepared.Args, want) {
				t.Fatalf("Args = %q, want %q", prepared.Args, want)
			}
		})
	}
}

func TestRewriteHTMLWritesTempFileForTheShim(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	prepared := f.prepare(t, f.rewriter(t), "session", "overlay", "open", "--html", "@0", "--js", "--block", "--size-percent", "60")
	want := []string{"session", "overlay", "open", "--target=" + rowCaller, "--size-percent=60"}
	if !reflect.DeepEqual(prepared.Args, want) {
		t.Fatalf("Args = %q, want %q (no --html, --js or --block)", prepared.Args, want)
	}
	if prepared.Page == nil || prepared.Page.Row != rowCaller || prepared.Page.Side != "" {
		t.Fatalf("Page = %+v, want the caller's row and no side", prepared.Page)
	}
	if prepared.Page == nil || !prepared.Page.Block || prepared.Dir == "" {
		t.Fatalf("Page = %+v, Dir = %q, want a blocking page in a temp directory", prepared.Page, prepared.Dir)
	}
	if prepared.Page.File != filepath.Join(prepared.Dir, "0.html") {
		t.Fatalf("Page.File = %q, want 0.html in %q", prepared.Page.File, prepared.Dir)
	}
	data, err := os.ReadFile(prepared.Page.File)
	if err != nil || string(data) != "<html><body>hi</body></html>" {
		t.Fatalf("page file = %q, %v, want the body", data, err)
	}
	info, err := os.Stat(prepared.Page.File)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("page file mode = %v, %v, want 0600", info, err)
	}
	dirInfo, err := os.Stat(prepared.Dir)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("temp directory mode = %v, %v, want 0700", dirInfo, err)
	}
	for _, arg := range prepared.Args {
		if strings.Contains(arg, "html") {
			t.Fatalf("Args %q mention html", prepared.Args)
		}
	}
}

func TestRewriteHTMLNonBlocking(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	prepared := f.prepare(t, f.rewriter(t), "session", "overlay", "open", "--html", "@0")
	if prepared.Page == nil || prepared.Page.Block || len(prepared.Cancel) != 0 {
		t.Fatalf("Page = %+v, Cancel = %q, want a non-blocking page and no cancel hook", prepared.Page, prepared.Cancel)
	}
}

func TestRewriteForwardsStdinOnlyWithTheFlag(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	rewriter := f.rewriter(t)
	decision := f.authorize(t, "session", "type", "--stdin")
	wantAllow(t, decision)
	prepared, err := rewriter.Rewrite(context.Background(), f.request(t, "echo hi\n"), decision.Call)
	if err != nil || string(prepared.Stdin) != "echo hi\n" {
		t.Fatalf("Stdin = %q, %v, want the text", prepared.Stdin, err)
	}
	decision = f.authorize(t, "session", "type", "ls")
	prepared, err = rewriter.Rewrite(context.Background(), f.request(t, "ignored"), decision.Call)
	if err != nil || prepared.Stdin != nil {
		t.Fatalf("Stdin = %q, %v, want none without --stdin", prepared.Stdin, err)
	}
}

func TestRewritePlainCallsPassThrough(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	prepared := f.prepare(t, f.rewriter(t), "tree", "--json")
	if want := []string{"tree", "--json"}; !reflect.DeepEqual(prepared.Args, want) {
		t.Fatalf("Args = %q, want %q", prepared.Args, want)
	}
}

func TestRewriteLeavesNoDirectoryOnFailure(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	rewriter := f.rewriter(t)
	decision := f.authorize(t, "session", "overlay", "open", "--html", "@0")
	wantAllow(t, decision)
	request := f.request(t, "")
	request.Files = nil // the call names a file the request does not carry
	if _, err := rewriter.Rewrite(context.Background(), request, decision.Call); err == nil {
		t.Fatal("Rewrite() error = nil, want a missing file refused")
	}
	entries, err := os.ReadDir(rewriter.Root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temp root = %v, %v, want it empty", entries, err)
	}
}

// TestRewriteQuotingSurvivesTheShell parses each rewritten command line with
// sh and checks every word arrives intact, for the values the plan names.
func TestRewriteQuotingSurvivesTheShell(t *testing.T) {
	t.Helper()
	nasty := []string{`it's`, `say "hi"`, `a;b`, `$(touch /tmp/x)`, "`id`", "line1\nline2", `back\slash`}
	for _, value := range nasty {
		t.Run(value, func(t *testing.T) {
			f := newFixture(t)
			rewriter := f.rewriter(t)
			decision := f.authorize(t, "session", "new", "--name=w9")
			request := f.request(t, "")
			decision.Call.Opts = append(decision.Call.Opts,
				Opt{Name: "cwd", Value: value}, Opt{Name: "command", Value: value, Role: "command"})
			prepared, err := rewriter.Rewrite(context.Background(), request, decision.Call)
			if err != nil {
				t.Fatalf("Rewrite() error = %v", err)
			}
			line := strings.TrimPrefix(prepared.Args[len(prepared.Args)-1], "--command=")
			out, err := exec.Command("sh", "-c", "for a in "+line+"; do printf '%s\\0' \"$a\"; done").Output()
			if err != nil {
				t.Fatalf("sh error = %v", err)
			}
			got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			want := []string{testAgr, "open", testHost, "w9", "--cwd", value, "--command", value}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("shell words = %q, want %q", got, want)
			}
		})
	}
}

func TestRewriteQuotingExactStringForAQuote(t *testing.T) {
	t.Helper()
	got := Join(testAgr, "run", testHost, "--cwd", `/d'ir`, "--", "echo", `a"b;c`)
	want := `'/opt/homebrew/bin/agr' 'run' 'myhost' '--cwd' '/d'\''ir' '--' 'echo' 'a"b;c'`
	if got != want {
		t.Fatalf("Join() = %s, want %s", got, want)
	}
}

func TestRewriteBindsErrorIsRefused(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	ctrl := gomock.NewController(t)
	binds := mocks.NewMockBindingLoader(ctrl)
	binds.EXPECT().Load().Return(nil, errors.New("disk")).AnyTimes()
	rewriter := &Rewriter{Agr: testAgr, Root: t.TempDir(), Binds: binds}
	decision := f.authorize(t, "session", "restore", "claude")
	if _, err := rewriter.Rewrite(context.Background(), f.request(t, ""), decision.Call); err == nil {
		t.Fatal("Rewrite() error = nil, want the binding read failure")
	}
}

func TestRewriteSessionNewConcurrentCallsGetDistinctNames(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	rewriter := f.rewriter(t)
	decision := f.authorize(t, "session", "new", "--name", "build")
	wantAllow(t, decision)
	const calls = 8
	names := make(chan string, calls)
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prepared, err := rewriter.Rewrite(context.Background(), f.request(t, ""), decision.Call)
			if err != nil {
				t.Errorf("Rewrite error = %v", err)
				return
			}
			names <- prepared.NewRow.Name
		}()
	}
	wg.Wait()
	close(names)
	seen := make(map[string]bool)
	for name := range names {
		if seen[name] {
			t.Fatalf("name %q selected twice", name)
		}
		seen[name] = true
	}
	// Releasing a reservation frees its name again.
	prepared, err := rewriter.Rewrite(context.Background(), f.request(t, ""), decision.Call)
	if err != nil {
		t.Fatal(err)
	}
	prepared.NewRow.Release()
	again, err := rewriter.Rewrite(context.Background(), f.request(t, ""), decision.Call)
	if err != nil {
		t.Fatal(err)
	}
	if again.NewRow.Name != prepared.NewRow.Name {
		t.Fatalf("released name %q not reused, got %q", prepared.NewRow.Name, again.NewRow.Name)
	}
}

// TestRewriteOverlayDropsFollowWithAWarning: page tools
// pass --follow by default; from a host it is left out, never applied.
func TestRewriteOverlayDropsFollowWithAWarning(t *testing.T) {
	t.Helper()
	for _, argv := range [][]string{
		{"session", "overlay", "open", "--html", "@0", "--follow", "--block"},
		{"session", "overlay", "open", "--follow", "top"},
	} {
		f := newFixture(t)
		prepared := f.prepare(t, f.rewriter(t), argv...)
		if slices.ContainsFunc(prepared.Args, func(arg string) bool { return strings.Contains(arg, "follow") }) {
			t.Fatalf("%q: Args = %q, want --follow left out", argv, prepared.Args)
		}
		want := []string{"--follow ignored from a host (it would move the user's selection)"}
		if !reflect.DeepEqual(prepared.Warnings, want) {
			t.Fatalf("%q: Warnings = %q, want %q", argv, prepared.Warnings, want)
		}
	}
}

// ownMixedRow binds the mixed row's right pane to the host too, so the host
// owns the whole split row.
func ownMixedRow(t *testing.T, f *fixture) {
	t.Helper()
	f.binds = append(f.binds, bind(t, rowMixed, mixedRight, "right", testHost, "w3-2"))
	row := f.row(t, rowMixed)
	row.SplitForegroundShell = ""
	row.SplitForeground = attachedFG(t, testHost, "w3-2")
}

func TestRewritePageSize(t *testing.T) {
	t.Helper()
	html := []string{"session", "overlay", "open", "--html", "@0", "--target", rowMixed, "--pane", "left"}
	tests := []struct {
		name     string
		owned    bool
		size     int
		extra    []string
		wantArgs []string
		wantWarn []string
		wantSide string
	}{
		{name: "no default keeps the pane", owned: true,
			wantArgs: []string{"--target=" + rowMixed, "--pane=left"}, wantSide: "left"},
		{name: "default covers an owned row", owned: true, size: 70,
			wantArgs: []string{"--target=" + rowMixed, "--size-percent=70"}},
		{name: "100 is full", owned: true, size: 100,
			wantArgs: []string{"--target=" + rowMixed, "--pane=left"}, wantSide: "left"},
		{name: "the caller's size wins", owned: true, size: 70, extra: []string{"--width", "600"},
			wantArgs: []string{"--target=" + rowMixed, "--width=600"}},
		{name: "a Mac pane keeps it pane-only", size: 70,
			wantArgs: []string{"--target=" + rowMixed, "--pane=left"}, wantSide: "left"},
		{name: "a size on a pane-only page is dropped", extra: []string{"--size-percent", "50"},
			wantArgs: []string{"--target=" + rowMixed, "--pane=left"}, wantSide: "left",
			wantWarn: []string{"size ignored: a pane overlay is always full, and this row has a pane the host does not own"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			if tt.owned {
				ownMixedRow(t, f)
			}
			rewriter := f.rewriter(t)
			rewriter.PageSize = func() int { return tt.size }
			prepared := f.prepare(t, rewriter, append(append([]string(nil), html...), tt.extra...)...)
			// the shim page's --html is gone: the daemon adds its --url
			if args := prepared.Args[3:]; !reflect.DeepEqual(args, tt.wantArgs) {
				t.Fatalf("Args = %q, want %q after the path", prepared.Args, tt.wantArgs)
			}
			if !reflect.DeepEqual(prepared.Warnings, tt.wantWarn) {
				t.Fatalf("Warnings = %q, want %q", prepared.Warnings, tt.wantWarn)
			}
			if prepared.Page == nil || prepared.Page.Side != tt.wantSide {
				t.Fatalf("Page = %+v, want side %q", prepared.Page, tt.wantSide)
			}
		})
	}
}

func TestRewriteWholeRowPageClosesTheRowOnCancel(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	ownMixedRow(t, f)
	rewriter := f.rewriter(t)
	rewriter.PageSize = func() int { return 60 }
	prepared := f.prepare(t, rewriter, "session", "overlay", "open", "--url", "https://example.com",
		"--block", "--target", rowMixed, "--pane", "left")
	want := []string{"session", "overlay", "close", "--target=" + rowMixed}
	if !reflect.DeepEqual(prepared.Cancel, want) {
		t.Fatalf("Cancel = %q, want %q", prepared.Cancel, want)
	}
	if slices.Contains(prepared.Args, "--pane=left") || !slices.Contains(prepared.Args, "--size-percent=60") {
		t.Fatalf("Args = %q, want the whole row at 60%%", prepared.Args)
	}
}
