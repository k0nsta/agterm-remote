package remote

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
	"github.com/k0nsta/agterm-remote/internal/token"
)

type runnerSSHCall struct {
	host  string
	stdin []byte
	argv  []string
}

type runnerSSH struct {
	mu     sync.Mutex
	calls  []runnerSSHCall
	body   []byte
	err    error
	result [][]byte
	errors []error
}

func (s *runnerSSH) Run(_ context.Context, host string, stdin []byte, argv ...string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, runnerSSHCall{host: host, stdin: append([]byte(nil), stdin...), argv: append([]string(nil), argv...)})
	index := len(s.calls) - 1
	if index < len(s.result) {
		var err error
		if index < len(s.errors) {
			err = s.errors[index]
		}
		return append([]byte(nil), s.result[index]...), err
	}
	return append([]byte(nil), s.body...), s.err
}

func (s *runnerSSH) Calls(t *testing.T) []runnerSSHCall {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]runnerSSHCall, len(s.calls))
	copy(result, s.calls)
	return result
}

func TestRunnerHomeCachesProbeAndUsesArgv(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	ssh := &runnerSSH{body: []byte("/home/remote\n")}
	runner := NewRunner(ssh, dirs)

	first, err := runner.Home(context.Background(), "user@example.com")
	if err != nil {
		t.Fatalf("first Home() error = %v", err)
	}
	second, err := runner.Home(context.Background(), "user@example.com")
	if err != nil {
		t.Fatalf("second Home() error = %v", err)
	}
	if first != "/home/remote" || second != first {
		t.Fatalf("Home() values = %q, %q, want /home/remote", first, second)
	}
	calls := ssh.Calls(t)
	if len(calls) != 1 {
		t.Fatalf("SSH calls = %d, want one probe", len(calls))
	}
	want := runnerSSHCall{host: "user@example.com", argv: []string{"sh", "-c", `printf %s "$HOME"`}}
	if !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("home probe call = %#v, want %#v", calls[0], want)
	}
}

func TestRunnerHomeDoesNotProbeCorruptCache(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	if err := SaveHostInfo(dirs, "host", HostInfo{Home: "/home/ok"}); err != nil {
		t.Fatalf("SaveHostInfo() error = %v", err)
	}
	infoPath := dirs.HostInfo(token.FileKey("host"))
	if err := os.WriteFile(infoPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("write corrupt host info: %v", err)
	}
	ssh := &runnerSSH{body: []byte("/home/probed")}
	_, err := NewRunner(ssh, dirs).Home(context.Background(), "host")
	if err == nil || len(ssh.Calls(t)) != 0 {
		t.Fatalf("Home() error = %v, calls = %d, want corrupt-cache error and no probe", err, len(ssh.Calls(t)))
	}
}

func TestRunnerEnsureDirsUsesConstantArgv(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	ssh := &runnerSSH{}
	if err := NewRunner(ssh, dirs).EnsureDirs(context.Background(), "host-1"); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}
	calls := ssh.Calls(t)
	if len(calls) != 1 {
		t.Fatalf("SSH calls = %d, want one", len(calls))
	}
	want := []string{"sh", "-c", `mkdir -p "$HOME/.cache/agr" "$HOME/.config/agr" "$HOME/.local/bin"`}
	if !reflect.DeepEqual(calls[0].argv, want) {
		t.Fatalf("EnsureDirs() argv = %#v, want %#v", calls[0].argv, want)
	}
}

func TestRunnerDataHeaderTable(t *testing.T) {
	t.Helper()
	tests := []struct {
		name    string
		body    string
		wantVer string
		want    string
		wantErr bool
	}{
		{name: "missing", body: "", wantErr: true},
		{name: "missing wrong prefix", body: "agr 1.0.0\nbody", wantErr: true},
		{name: "ok with body", body: "agr\t1.0.0\nbody\n", wantVer: "1.0.0", want: "body\n"},
		{name: "ok without trailing newline", body: "agr\t1.0.0", wantVer: "1.0.0", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Helper()
			version, payload, err := parseDataHeader([]byte(test.body))
			if (err != nil) != test.wantErr {
				t.Fatalf("parseDataHeader() error = %v, wantErr %v", err, test.wantErr)
			}
			if err == nil && (version != test.wantVer || string(payload) != test.want) {
				t.Fatalf("parseDataHeader() = (%q, %q), want (%q, %q)", version, payload, test.wantVer, test.want)
			}
		})
	}
}

func TestRunnerDataUnreachablePrecedesNotInstalled(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	if err := SaveHostInfo(dirs, "host", HostInfo{Home: "/home/remote"}); err != nil {
		t.Fatalf("SaveHostInfo() error = %v", err)
	}
	ssh := &runnerSSH{err: ErrUnreachable}
	_, err := NewRunnerWithVersion(ssh, dirs, "1.0.0").Data(context.Background(), "host", "sessions")
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Data() error = %v, want ErrUnreachable", err)
	}
	if errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Data() error = %v, must not be ErrNotInstalled", err)
	}
}

func TestRunnerDataMismatchWarnsAndReturnsPayload(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	if err := SaveHostInfo(dirs, "host", HostInfo{Home: "/home/remote"}); err != nil {
		t.Fatalf("SaveHostInfo() error = %v", err)
	}
	ssh := &runnerSSH{body: []byte("agr\t0.9.0\napi\t0\t-\t-\t-\n")}
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	body, err := NewRunnerWithVersion(ssh, dirs, "1.0.0").Data(context.Background(), "host", "sessions")
	if err != nil {
		t.Fatalf("Data() error = %v", err)
	}
	if string(body) != "api\t0\t-\t-\t-\n" {
		t.Fatalf("Data() body = %q, want session payload", body)
	}
	if !strings.Contains(logs.String(), "version mismatch") {
		t.Fatalf("log = %q, want version mismatch warning", logs.String())
	}
}

func TestRunnerDataPropagatesExitAfterValidHeader(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	if err := SaveHostInfo(dirs, "host", HostInfo{Home: "/home/remote"}); err != nil {
		t.Fatalf("SaveHostInfo() error = %v", err)
	}
	ssh := &runnerSSH{
		body: []byte("agr\t1.0.0\nrefused\n"),
		err:  &ExitError{Code: 1, Stderr: "agr: 'api' exists but is not agr-managed"},
	}
	body, err := NewRunnerWithVersion(ssh, dirs, "1.0.0").Data(context.Background(), "host", "reap", "api")
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Data() error = %v, want ExitError", err)
	}
	if !strings.Contains(err.Error(), "not agr-managed") || string(body) != "refused\n" {
		t.Fatalf("Data() = (%q, %v), want payload and refusal text", body, err)
	}
}

func TestRunnerReapValidatesNameAndSurfacesRemoteRefusal(t *testing.T) {
	t.Helper()
	dirs := pathstest.Dirs(t)
	if err := SaveHostInfo(dirs, "host", HostInfo{Home: "/home/remote"}); err != nil {
		t.Fatalf("SaveHostInfo() error = %v", err)
	}
	ssh := &runnerSSH{
		body: []byte("agr\t1.0.0\n"),
		err:  &ExitError{Code: 1, Stderr: "agr: session is not managed"},
	}
	runner := NewRunnerWithVersion(ssh, dirs, "1.0.0")
	if err := runner.Reap(context.Background(), "host", "bad name"); err == nil {
		t.Fatal("Reap() with invalid name error = nil")
	}
	if err := runner.Reap(context.Background(), "host", "api"); err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("Reap() error = %v, want remote refusal", err)
	}
	if len(ssh.Calls(t)) != 1 {
		t.Fatalf("SSH calls = %d, want invalid name not sent", len(ssh.Calls(t)))
	}
}

// TestNoExpansionDependentArgvReachesSSH guards the class rather than the site.
// Three separate fixes in this codebase have quoted argv correctly and then
// broken a caller that silently depended on the remote shell expanding it
// ($HOME probe, EnsureDirs, and AgrPath's fallback). ExecSSH quotes every argv
// element, so any value carrying $HOME or a leading ~ MUST be inside an
// explicit `sh -c` script, never a bare argument.
func TestNoExpansionDependentArgvReachesSSH(t *testing.T) {
	t.Helper()
	// Drive the REAL callers and inspect the argv they actually send. An
	// earlier version of this test walked literals written inside the test,
	// so it asserted against its own fixture and could not fail if production
	// reintroduced a bare "$HOME" argument — which is the whole defect class
	// it exists to guard.
	for _, tc := range []struct {
		name   string
		invoke func(*testing.T, *Runner) //nolint:thelper // invoked as the subtest body
	}{
		{"Home", func(t *testing.T, r *Runner) {
			t.Helper()
			if _, err := r.Home(context.Background(), "user@example.com"); err != nil {
				t.Fatalf("Home() error = %v", err)
			}
		}},
		{"EnsureDirs", func(t *testing.T, r *Runner) {
			t.Helper()
			if err := r.EnsureDirs(context.Background(), "user@example.com"); err != nil {
				t.Fatalf("EnsureDirs() error = %v", err)
			}
		}},
		{"ResolveAgrPath", func(t *testing.T, r *Runner) {
			t.Helper()
			if _, err := r.ResolveAgrPath(context.Background(), "user@example.com"); err != nil {
				t.Fatalf("ResolveAgrPath() error = %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ssh := &runnerSSH{body: []byte("/home/remote\n")}
			tc.invoke(t, NewRunner(ssh, pathstest.Dirs(t)))
			calls := ssh.Calls(t)
			if len(calls) == 0 {
				t.Fatal("caller made no SSH call, so the invariant was never exercised")
			}
			for _, call := range calls {
				assertExpansionIsExplicit(t, call.argv)
			}
		})
	}
}

// assertExpansionIsExplicit enforces the invariant on one real argv: ExecSSH
// quotes every element, so a value carrying $HOME or a leading ~ only expands
// when it is the script operand of an explicit `sh -c`.
func assertExpansionIsExplicit(t *testing.T, argv []string) {
	t.Helper()
	for i, arg := range argv {
		if !strings.Contains(arg, "$HOME") && !strings.HasPrefix(arg, "~") {
			continue
		}
		if i < 2 || argv[0] != "sh" || argv[1] != "-c" {
			t.Fatalf("argv %q element %d depends on shell expansion but is not the operand of an explicit `sh -c`; ExecSSH quotes argv, so it would arrive literally", argv, i)
		}
	}
}

// TestResolveAgrPathReturnsAbsolutePathAndRejectsBadHome covers the two
// behaviours that replaced AgrPath's expansion-dependent fallback. Neither was
// exercised before: ResolveAgrPath had no test at all, and the non-absolute
// guard was added without one.
func TestResolveAgrPathReturnsAbsolutePathAndRejectsBadHome(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name    string
		probe   string
		want    string
		wantErr string
	}{
		{name: "absolute home", probe: "/home/remote\n", want: "/home/remote/.local/bin/agr"},
		{name: "home with spaces", probe: "/srv/user data\n", want: "/srv/user data/.local/bin/agr"},
		// A relative or empty probe must never be cached: every remote path
		// for the host is built from it.
		{name: "relative home", probe: "relative/home\n", wantErr: "non-absolute"},
		{name: "empty home", probe: "\n", wantErr: "empty home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ssh := &runnerSSH{body: []byte(tc.probe)}
			runner := NewRunner(ssh, pathstest.Dirs(t))
			got, err := runner.ResolveAgrPath(context.Background(), "user@example.com")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ResolveAgrPath() error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveAgrPath() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("ResolveAgrPath() = %q, want %q", got, tc.want)
			}
			// The returned path must be usable as argv, i.e. carry no value
			// that needs a remote shell to expand.
			assertExpansionIsExplicit(t, []string{got})
		})
	}
}
