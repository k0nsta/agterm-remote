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
		nil,
		[]byte(`{"existing":true}`),
		nil,
	}}
	runner := NewRunnerWithVersion(ssh, dirs, "1.0.0")
	result, err := runner.InstallResult(context.Background(), "user@example.com", "")
	if err != nil {
		t.Fatalf("InstallResult() error = %v", err)
	}
	if want := (InstallResult{Host: "user@example.com", Version: "1.0.0", Mux: "zmx", Relay: "nc", Proxy: "agtermctl proxy: updated"}); !reflect.DeepEqual(result, want) {
		t.Fatalf("InstallResult() = %#v, want %#v", result, want)
	}
	calls := ssh.Calls(t)
	if len(calls) != 7 {
		t.Fatalf("SSH calls = %d, want probe, dirs, env, script, proxy, hooks read/write", len(calls))
	}
	if got, want := calls[0].argv, []string{"sh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("probe argv = %#v, want %#v", got, want)
	}
	if got, want := calls[1].argv, []string{"sh", "-c", `mkdir -p "$HOME/.cache/agr" "$HOME/.config/agr" "$HOME/.local/bin"`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("EnsureDirs argv = %#v, want %#v", got, want)
	}
	for index := 2; index < len(calls); index++ {
		if len(calls[index].stdin) == 0 && index != 5 {
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
	if got, want := calls[4].argv, []string{"sh", "-c", proxyWriteScript}; !reflect.DeepEqual(got, want) {
		t.Fatalf("proxy write argv = %#v, want %#v", got, want)
	}
	if got, want := string(calls[4].stdin), "#!/bin/sh\n# agr-proxy-wrapper: written by agr install; replaced on update\nexec \"$HOME/.local/bin/agr\" ctl \"$@\"\n"; got != want {
		t.Fatalf("proxy wrapper = %q, want %q", got, want)
	}
	if got, want := calls[5].argv, []string{"sh", "-c", hooksReadScript}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hooks read argv = %#v, want %#v", got, want)
	}
	if got, want := calls[6].argv, []string{"sh", "-c", hooksWriteScript}; !reflect.DeepEqual(got, want) {
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
	ssh := &runnerSSH{result: [][]byte{probe, nil, nil, nil, nil, []byte(`{}`), nil}}
	result, err := NewRunnerWithVersion(ssh, testDirsForInstall(t), "1.0.0").InstallResult(context.Background(), "host", "tmux")
	if err != nil {
		t.Fatalf("InstallResult() error = %v", err)
	}
	if result.Mux != "tmux" || len(ssh.Calls(t)) != 7 {
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
	ssh := &runnerSSH{result: [][]byte{probe, nil, nil, nil, nil, []byte("{")}}
	if _, err := NewRunner(ssh, testDirsForInstall(t)).InstallResult(context.Background(), "host", ""); err == nil {
		t.Fatal("InstallResult() error = nil for malformed hooks")
	}
	if got := len(ssh.Calls(t)); got != 6 {
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

type terminfoFake struct {
	entry []byte
	err   error
	calls []string
}

func (f *terminfoFake) Infocmp(_ context.Context, term string) ([]byte, error) {
	f.calls = append(f.calls, term)
	return f.entry, f.err
}

// TestInstallPushesMissingTerminfo pins the first real-host lesson: the Mac
// sends TERM=xterm-ghostty and a host without that entry runs tmux blind.
// Install now carries the local entry over when the probe says it is missing
// and tic is available; every other outcome is reported, never fatal.
func TestInstallPushesMissingTerminfo(t *testing.T) {
	t.Helper()
	base, err := os.ReadFile("testdata/probe.txt")
	if err != nil {
		t.Fatalf("read probe fixture: %v", err)
	}
	tests := []struct {
		name      string
		probe     string
		infocmp   *terminfoFake
		wantCalls int
		wantNote  string
		wantTic   bool
	}{
		{"missing with tic: pushed", "terminfo\t0\ntic\t1\n", &terminfoFake{entry: []byte("xterm-ghostty|entry,\n")}, 8, "terminfo xterm-ghostty: installed", true},
		{"present: nothing pushed", "terminfo\t1\ntic\t1\n", &terminfoFake{entry: []byte("unused")}, 7, "terminfo xterm-ghostty: present", false},
		{"missing without tic: reported", "terminfo\t0\ntic\t0\n", &terminfoFake{entry: []byte("unused")}, 7, "tic is not installed", false},
		{"local infocmp fails: reported", "terminfo\t0\ntic\t1\n", &terminfoFake{err: errors.New("no entry")}, 7, "could not read the local entry", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			dirs := testDirsForInstall(t)
			ssh := &runnerSSH{result: [][]byte{append(append([]byte(nil), base...), []byte(tc.probe)...), nil, nil, nil, nil, []byte(`{}`), nil, nil}}
			runner := NewRunnerWithVersion(ssh, dirs, "1.0.0")
			runner.SetTerm("xterm-ghostty")
			runner.terminfo = tc.infocmp
			result, err := runner.InstallResult(context.Background(), "user@example.com", "")
			if err != nil {
				t.Fatalf("InstallResult() error = %v", err)
			}
			if !strings.Contains(result.Terminfo, tc.wantNote) {
				t.Fatalf("InstallResult().Terminfo = %q, want it to contain %q", result.Terminfo, tc.wantNote)
			}
			calls := ssh.Calls(t)
			if len(calls) != tc.wantCalls {
				t.Fatalf("SSH calls = %d, want %d", len(calls), tc.wantCalls)
			}
			if tc.wantTic {
				last := calls[len(calls)-1]
				if !reflect.DeepEqual(last.argv, []string{"tic", "-x", "-"}) || string(last.stdin) != string(tc.infocmp.entry) {
					t.Fatalf("terminfo push = argv %#v stdin %q, want tic -x - fed the local entry", last.argv, last.stdin)
				}
			}
		})
	}
}

// probeWith swaps the agtermctl and PATH lines of the fixture, which describes
// a host that already has the agr proxy on its PATH.
func probeWith(t *testing.T, agtermctl, localBin string) string {
	t.Helper()
	body, err := os.ReadFile("testdata/probe.txt")
	if err != nil {
		t.Fatalf("read probe fixture: %v", err)
	}
	text := strings.Replace(string(body), "agtermctl\tproxy\n", "agtermctl\t"+agtermctl+"\n", 1)
	return strings.Replace(text, "local_bin_path\t1\n", "local_bin_path\t"+localBin+"\n", 1)
}

// TestInstallProxyWrapper pins the three cases: a host without an agtermctl
// gets the wrapper, an earlier agr wrapper is updated, and a foreign agtermctl
// (a Mac running agterm) is left alone and the install says so.
func TestInstallProxyWrapper(t *testing.T) {
	t.Helper()
	tests := []struct {
		name      string
		agtermctl string
		localBin  string
		wantWrite bool
		wantNote  string
	}{
		{"none: written", "MISSING", "1", true, "agtermctl proxy: installed"},
		{"marked: updated", "proxy", "1", true, "agtermctl proxy: updated"},
		{"foreign: skipped", "other", "1", false, "agtermctl proxy: skipped"},
		{"none, not on PATH: written with a warning", "MISSING", "0", true, "installed (~/.local/bin is not on a login shell PATH there)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			results := [][]byte{[]byte(probeWith(t, tc.agtermctl, tc.localBin)), nil, nil, nil}
			wantCalls := 6
			if tc.wantWrite {
				results = append(results, nil)
				wantCalls = 7
			}
			results = append(results, []byte(`{}`), nil)
			ssh := &runnerSSH{result: results}
			result, err := NewRunnerWithVersion(ssh, testDirsForInstall(t), "1.0.0").InstallResult(context.Background(), "host", "")
			if err != nil {
				t.Fatalf("InstallResult() error = %v", err)
			}
			if !strings.Contains(result.Proxy, tc.wantNote) {
				t.Fatalf("InstallResult().Proxy = %q, want it to contain %q", result.Proxy, tc.wantNote)
			}
			calls := ssh.Calls(t)
			if len(calls) != wantCalls {
				t.Fatalf("SSH calls = %d, want %d", len(calls), wantCalls)
			}
			wrote := false
			for _, call := range calls {
				if reflect.DeepEqual(call.argv, []string{"sh", "-c", proxyWriteScript}) {
					wrote = true
				}
			}
			if wrote != tc.wantWrite {
				t.Fatalf("wrapper written = %v, want %v", wrote, tc.wantWrite)
			}
		})
	}
}

func TestProxyWriteScriptKeepsUnmarkedFileAndSetsMode(t *testing.T) {
	t.Helper()
	if !strings.Contains(proxyWriteScript, "chmod 755") || !strings.Contains(proxyWriteScript, "grep -q '^"+proxyMarker+"'") {
		t.Fatalf("proxyWriteScript = %q, want mode 755 and a marker check", proxyWriteScript)
	}
	if !strings.Contains(probeScript, "grep -q '^"+proxyMarker+"'") {
		t.Fatal("probe script does not look for the proxy marker line")
	}
	if !strings.HasPrefix(proxyWrapper, "#!/bin/sh\n"+proxyMarker) {
		t.Fatalf("proxyWrapper = %q, want the marker on its second line", proxyWrapper)
	}
}
