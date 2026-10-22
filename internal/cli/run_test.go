package cli

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/remote"
)

func runDeps(tty *openTTYFake) RunDependencies {
	return RunDependencies{TTY: tty}
}

func TestRunSSHArgv(t *testing.T) {
	t.Helper()
	tty := &openTTYFake{}
	argv := []string{"make", "-j2", "it's", "a;b", "$(id)"}
	if err := Run(context.Background(), "user@example.com", "/srv/my app", argv, runDeps(tty)); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"ssh", "-t", "user@example.com", "--",
		`'sh' '-c' '` + runScript + `' 'sh' '/srv/my app' 'make' '-j2' 'it'\''s' 'a;b' '$(id)'`}
	if !reflect.DeepEqual(tty.argv, want) {
		t.Fatalf("argv =\n%q\nwant\n%q", tty.argv, want)
	}
}

func TestRunWithoutCwdPassesAnEmptyDirectory(t *testing.T) {
	t.Helper()
	tty := &openTTYFake{}
	if err := Run(context.Background(), "host", "", []string{"claude"}, runDeps(tty)); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"ssh", "-t", "host", "--", `'sh' '-c' '` + runScript + `' 'sh' '' 'claude'`}
	if !reflect.DeepEqual(tty.argv, want) {
		t.Fatalf("argv = %q, want %q", tty.argv, want)
	}
}

func TestRunUsesMoshWhenBothSidesSupportIt(t *testing.T) {
	t.Helper()
	tty := &openTTYFake{}
	deps := runDeps(tty)
	deps.HostInfo = func(string) (remote.HostInfo, error) { return remote.HostInfo{Mosh: true}, nil }
	deps.MoshPath = func() string { return "/usr/local/bin/mosh" }
	if err := Run(context.Background(), "host", "/srv", []string{"make", "dev"}, deps); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"mosh", "host", "--", "sh", "-c", runScript, "sh", "/srv", "make", "dev"}
	if !reflect.DeepEqual(tty.argv, want) {
		t.Fatalf("argv = %q, want %q", tty.argv, want)
	}
}

func TestRunScriptChangesDirectoryThenExecs(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	out, err := exec.Command("sh", "-c", runScript, "sh", dir, "pwd", "-P").Output()
	if err != nil {
		t.Fatalf("run script in %s: %v", dir, err)
	}
	if !strings.HasSuffix(strings.TrimSpace(string(out)), strings.TrimPrefix(dir, "/private")) {
		t.Fatalf("pwd = %q, want %q", out, dir)
	}
	if _, err := exec.Command("sh", "-c", runScript, "sh", dir+"/missing", "true").Output(); err == nil {
		t.Fatal("a missing directory must fail, not run the program elsewhere")
	}
	if err := exec.Command("sh", "-c", runScript, "sh", "", "true").Run(); err != nil {
		t.Fatalf("no directory: %v", err)
	}
}

func TestRunRefusals(t *testing.T) {
	t.Helper()
	tests := []struct {
		name string
		host string
		dir  string
		argv []string
		want string
	}{
		{name: "bad host", host: "-oProxyCommand=x", argv: []string{"a"}, want: "invalid remote host"},
		{name: "relative cwd", host: "host", dir: "rel", argv: []string{"a"}, want: "must be absolute"},
		{name: "no argv", host: "host", want: "no command"},
		{name: "empty program", host: "host", argv: []string{""}, want: "no command"},
	}
	for _, test := range tests {
		tty := &openTTYFake{}
		err := Run(context.Background(), test.host, test.dir, test.argv, runDeps(tty))
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", test.name, err, test.want)
		}
		if tty.calls != 0 {
			t.Errorf("%s: ssh ran", test.name)
		}
	}
	if err := Run(context.Background(), "host", "", []string{"a"}, RunDependencies{}); err == nil {
		t.Error("Run() without a TTY runner must fail")
	}
}

func TestRunRunPassesTheExitStatusThrough(t *testing.T) {
	t.Helper()
	var errw strings.Builder
	if got := RunRun(context.Background(), "host", "", []string{"a"}, runDeps(&openTTYFake{}), &errw); got != 0 {
		t.Fatalf("exit = %d, want 0", got)
	}

	exit := exec.Command("sh", "-c", "exit 7").Run()
	var exitErr *exec.ExitError
	if !errors.As(exit, &exitErr) {
		t.Fatalf("could not make an ExitError: %v", exit)
	}
	if got := RunRun(context.Background(), "host", "", []string{"a"}, runDeps(&openTTYFake{err: exit}), &errw); got != 7 {
		t.Fatalf("exit = %d, want the program's 7", got)
	}
	if errw.Len() != 0 {
		t.Fatalf("a passed-through status printed %q", errw.String())
	}

	if got := RunRun(context.Background(), "host", "", nil, runDeps(&openTTYFake{}), &errw); got != 1 || !strings.Contains(errw.String(), "agr: run:") {
		t.Fatalf("a usage failure = %d, %q; want 1 and a message", got, errw.String())
	}
}
