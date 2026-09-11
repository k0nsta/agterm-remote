package remote

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
	"github.com/k0nsta/agterm-remote/internal/remotescript"
)

func TestInstallDefaultChoicesAndWriteOrder(t *testing.T) {
	t.Helper()
	dirs := testDirsForInstall(t)
	probe, err := os.ReadFile("testdata/probe.txt")
	if err != nil {
		t.Fatalf("read probe fixture: %v", err)
	}
	ssh := &runnerSSH{result: [][]byte{
		probe,
		nil,
		nil,
		nil,
		[]byte(`{"existing":true}`),
		nil,
	}}
	runner := NewRunnerWithVersion(ssh, dirs, "1.0.0")
	result, err := runner.InstallResult(context.Background(), "user@example.com", "")
	if err != nil {
		t.Fatalf("InstallResult() error = %v", err)
	}
	if want := (InstallResult{Host: "user@example.com", Version: "1.0.0", Mux: "zmx", Relay: "nc"}); !reflect.DeepEqual(result, want) {
		t.Fatalf("InstallResult() = %#v, want %#v", result, want)
	}
	calls := ssh.Calls(t)
	if len(calls) != 6 {
		t.Fatalf("SSH calls = %d, want probe, dirs, env, script, hooks read/write", len(calls))
	}
	if got, want := calls[0].argv, []string{"sh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("probe argv = %#v, want %#v", got, want)
	}
	if got, want := calls[1].argv, []string{"sh", "-c", `mkdir -p "$HOME/.cache/agr" "$HOME/.config/agr" "$HOME/.local/bin"`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("EnsureDirs argv = %#v, want %#v", got, want)
	}
	for index := 2; index < len(calls); index++ {
		if len(calls[index].stdin) == 0 && index != 4 {
			t.Fatalf("call %d has no stdin: %#v", index, calls[index])
		}
	}
	if got, want := calls[2].argv, []string{"sh", "-c", envWriteScript}; !reflect.DeepEqual(got, want) {
		t.Fatalf("env write argv = %#v, want %#v", got, want)
	}
	env := string(calls[2].stdin)
	for _, line := range []string{
		"export AGR_MUX=zmx\n",
		"export AGR_RELAY=nc\n",
		"export AGR_SOCK=$HOME/.cache/agr/bridge.sock\n",
		"export ZMX_DIR=/run/user/1000/zmx\n",
	} {
		if !strings.Contains(env, line) {
			t.Fatalf("env missing %q:\n%s", line, env)
		}
	}
	if got, want := calls[3].argv, []string{"sh", "-c", remoteScriptWrite}; !reflect.DeepEqual(got, want) {
		t.Fatalf("script write argv = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(calls[3].stdin, remotescript.Script("1.0.0")) {
		t.Fatal("script write did not send the versioned embedded script")
	}
	if got, want := calls[4].argv, []string{"sh", "-c", hooksReadScript}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hooks read argv = %#v, want %#v", got, want)
	}
	if got, want := calls[5].argv, []string{"sh", "-c", hooksWriteScript}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hooks write argv = %#v, want %#v", got, want)
	}
	info, err := LoadHostInfo(dirs, "user@example.com")
	if err != nil {
		t.Fatalf("LoadHostInfo() error = %v", err)
	}
	if info.Home != "/home/remote" || info.Mux != "zmx" || info.Relay != "nc" || info.AgrVersion != "1.0.0" || !info.Mosh || !info.ZmxLabels {
		t.Fatalf("saved HostInfo = %#v", info)
	}
}

func TestInstallMuxOverride(t *testing.T) {
	t.Helper()
	probe, err := os.ReadFile("testdata/probe.txt")
	if err != nil {
		t.Fatalf("read probe fixture: %v", err)
	}
	ssh := &runnerSSH{result: [][]byte{probe, nil, nil, nil, []byte(`{}`), nil}}
	result, err := NewRunnerWithVersion(ssh, testDirsForInstall(t), "1.0.0").InstallResult(context.Background(), "host", "tmux")
	if err != nil {
		t.Fatalf("InstallResult() error = %v", err)
	}
	if result.Mux != "tmux" || len(ssh.Calls(t)) != 6 {
		t.Fatalf("InstallResult() = %#v, calls = %d, want tmux and complete install", result, len(ssh.Calls(t)))
	}
}

func TestInstallChoiceFailuresDoNotWrite(t *testing.T) {
	t.Helper()
	tests := []struct {
		name     string
		probe    string
		override string
		want     string
	}{
		{name: "no mux", probe: "home\t/home/x\nnc_u\t1\n", want: "install zmx or tmux"},
		{name: "missing override", probe: "home\t/home/x\ntmux\t3.4\nnc_u\t1\n", override: "zmx", want: "zmx is not installed"},
		{name: "no relay", probe: "home\t/home/x\nzmx\t0.7.1\nzmx_dir\t/tmp/zmx\n", want: "install nc with -U"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Helper()
			ssh := &runnerSSH{result: [][]byte{[]byte(test.probe)}}
			_, err := NewRunner(ssh, testDirsForInstall(t)).InstallResult(context.Background(), "host", test.override)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("InstallResult() error = %v, want text %q", err, test.want)
			}
			if len(ssh.Calls(t)) != 1 {
				t.Fatalf("SSH calls = %d, want probe only", len(ssh.Calls(t)))
			}
		})
	}
}

func TestInstallStopsBeforeWritesWhenEnsureDirsFails(t *testing.T) {
	t.Helper()
	probe, err := os.ReadFile("testdata/probe.txt")
	if err != nil {
		t.Fatalf("read probe fixture: %v", err)
	}
	ssh := &runnerSSH{
		result: [][]byte{probe, nil},
		errors: []error{nil, errors.New("mkdir failed")},
	}
	dirs := testDirsForInstall(t)
	if _, err := NewRunner(ssh, dirs).InstallResult(context.Background(), "host", ""); err == nil || !strings.Contains(err.Error(), "mkdir failed") {
		t.Fatalf("InstallResult() error = %v, want EnsureDirs failure", err)
	}
	if got := len(ssh.Calls(t)); got != 2 {
		t.Fatalf("SSH calls = %d, want probe and EnsureDirs only", got)
	}
	if _, err := LoadHostInfo(dirs, "host"); err == nil {
		t.Fatal("InstallResult() wrote HostInfo after EnsureDirs failed")
	}
}

func TestInstallRefusesMalformedHooksBeforeHookWrite(t *testing.T) {
	t.Helper()
	probe, err := os.ReadFile("testdata/probe.txt")
	if err != nil {
		t.Fatalf("read probe fixture: %v", err)
	}
	ssh := &runnerSSH{result: [][]byte{probe, nil, nil, nil, []byte("{")}}
	if _, err := NewRunner(ssh, testDirsForInstall(t)).InstallResult(context.Background(), "host", ""); err == nil {
		t.Fatal("InstallResult() error = nil for malformed hooks")
	}
	if got := len(ssh.Calls(t)); got != 5 {
		t.Fatalf("SSH calls = %d, want no hooks write after malformed input", got)
	}
}

func TestInstallHelpers(t *testing.T) {
	t.Helper()
	if got, err := chooseRelay(ProbeResult{NCU: true, Python3: true, Socat: true}); err != nil || got != "nc" {
		t.Fatalf("chooseRelay() = %q, want nc", got)
	}
	if got, err := chooseRelay(ProbeResult{Python3: true, Socat: true}); err != nil || got != "python3" {
		t.Fatalf("chooseRelay() = %q, want python3", got)
	}
	if got, err := chooseRelay(ProbeResult{Socat: true}); err != nil || got != "socat" {
		t.Fatalf("chooseRelay() = %q, want socat", got)
	}
	if got := installEnv("tmux", "python3", "/tmp/zmx space"); !strings.Contains(got, "export ZMX_DIR='/tmp/zmx space'\n") {
		t.Fatalf("installEnv() did not quote unsafe path: %q", got)
	}
}

func testDirsForInstall(t *testing.T) (dirs paths.Dirs) {
	t.Helper()
	return pathstest.Dirs(t)
}
