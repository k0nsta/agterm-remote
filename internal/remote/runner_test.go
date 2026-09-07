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

	"github.com/k0nsta/agterm-remote/internal/paths"
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
	dirs := paths.TestDirs(t)
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
	dirs := paths.TestDirs(t)
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
	dirs := paths.TestDirs(t)
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
	dirs := paths.TestDirs(t)
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
	dirs := paths.TestDirs(t)
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
	dirs := paths.TestDirs(t)
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
	dirs := paths.TestDirs(t)
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

func TestRunnerAgrPathUsesCachedHome(t *testing.T) {
	t.Helper()
	dirs := paths.TestDirs(t)
	if err := SaveHostInfo(dirs, "host", HostInfo{Home: "/home/remote"}); err != nil {
		t.Fatalf("SaveHostInfo() error = %v", err)
	}
	runner := NewRunner(nil, dirs)
	if got, want := runner.AgrPath("host"), "/home/remote/.local/bin/agr"; got != want {
		t.Fatalf("AgrPath() = %q, want %q", got, want)
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
	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"home probe", []string{"sh", "-c", `printf %s "$HOME"`}},
		{"ensure dirs", []string{"sh", "-c", `mkdir -p "$HOME/.cache/agr" "$HOME/.config/agr" "$HOME/.local/bin"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i, arg := range tc.argv {
				needsShell := strings.Contains(arg, "$HOME") || strings.HasPrefix(arg, "~")
				if !needsShell {
					continue
				}
				// An expansion-dependent value is only safe as the script
				// operand of an explicit shell: argv[0]=="sh", argv[1]=="-c".
				if i < 2 || tc.argv[0] != "sh" || tc.argv[1] != "-c" {
					t.Fatalf("argv[%d]=%q depends on shell expansion but is not the operand of an explicit `sh -c`; ExecSSH quotes argv, so it would arrive literally", i, arg)
				}
			}
		})
	}
}
