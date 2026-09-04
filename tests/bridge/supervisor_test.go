package bridge_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/bridge"
)

func TestExecRunnerStopsTheWholeSSHProcessGroup(t *testing.T) {
	t.Helper()
	shimDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatalf("resolve shim directory: %v", err)
	}
	marker := "agr-bridge-test-" + strconv.Itoa(os.Getpid())
	t.Setenv("PATH", shimDir)
	t.Setenv("AGR_BRIDGE_SHIM_MARKER", marker)
	t.Setenv("AGR_BRIDGE_SHIM_MODE", "")
	t.Setenv("AGR_BRIDGE_SHIM_COUNT_FILE", "")

	supervisor := bridge.New("host", "key", "/remote/bridge.sock", filepath.Join(t.TempDir(), "recv.sock"), &bridge.ExecRunner{})
	runDone := make(chan error, 1)
	go func() { runDone <- supervisor.Run(context.Background()) }()
	waitForProcess(t, marker, true)

	supervisor.Stop()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("supervisor did not stop")
	}
	waitForProcess(t, marker, false)
}

func TestSSHShimExit255RestartsWithGrowingGaps(t *testing.T) {
	t.Helper()
	shimDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatalf("resolve shim directory: %v", err)
	}
	countFile := filepath.Join(t.TempDir(), "starts")
	t.Setenv("PATH", shimDir)
	t.Setenv("AGR_BRIDGE_SHIM_MODE", "")
	t.Setenv("AGR_BRIDGE_SHIM_MARKER", "")
	t.Setenv("AGR_BRIDGE_SHIM_COUNT_FILE", countFile)

	supervisor := bridge.New("host", "key", "/remote/bridge.sock", filepath.Join(t.TempDir(), "recv.sock"), &bridge.ExecRunner{})
	runDone := make(chan error, 1)
	go func() { runDone <- supervisor.Run(context.Background()) }()
	waitForCount(t, countFile, 1)
	first := time.Now()
	waitForCount(t, countFile, 2)
	second := time.Now()
	waitForCount(t, countFile, 3)
	third := time.Now()
	if second.Sub(first) < 700*time.Millisecond {
		t.Fatalf("first reconnect gap = %s, want at least 700ms", second.Sub(first))
	}
	if third.Sub(second) <= second.Sub(first) {
		t.Fatalf("reconnect gaps = %s then %s, want growing gaps", second.Sub(first), third.Sub(second))
	}

	supervisor.Stop()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("supervisor did not stop")
	}
}

func waitForProcess(t *testing.T, marker string, present bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		found := processMatches(t, marker)
		if found == present {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process marker %q present = %v, want %v", marker, processMatches(t, marker), present)
}

func processMatches(t *testing.T, marker string) bool {
	t.Helper()
	command := exec.Command("/usr/bin/pgrep", "-f", marker)
	return command.Run() == nil
}

func waitForCount(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil {
			value, parseErr := strconv.Atoi(strings.TrimSpace(string(body)))
			if parseErr == nil && value >= want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("start count did not reach %d", want)
}
