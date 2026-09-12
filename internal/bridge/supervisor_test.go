package bridge_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/bridge"
	"github.com/k0nsta/agterm-remote/internal/bridge/mocks"
	"go.uber.org/mock/gomock"
)

func TestSupervisorUsesExactReverseSSHArgvAndPromotesOnEvent(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	process := mocks.NewMockProcess(ctrl)
	exited := make(chan struct{})
	process.EXPECT().Wait().DoAndReturn(func() error {
		<-exited
		return nil
	})
	process.EXPECT().Stop(gomock.Any()).DoAndReturn(func(time.Duration) error {
		close(exited)
		return nil
	})

	runner := mocks.NewMockProcessRunner(ctrl)
	supervisor := bridge.New("user@example.com", "host-key", "/remote/agr.sock", "/local/recv.sock", runner)
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		sshPath = "/usr/bin/ssh"
	}
	wantArgv := []string{
		sshPath,
		"-N",
		"-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
		"-R", "/remote/agr.sock:/local/recv.sock",
		"user@example.com",
	}
	started := make(chan struct{})
	runner.EXPECT().Start(gomock.Any(), gomock.Any(), filepath.Join("/local", "bridge-host-key.log")).DoAndReturn(func(_ context.Context, argv []string, _ string) (bridge.Process, error) {
		if !reflect.DeepEqual(argv, wantArgv) {
			t.Errorf("ssh argv = %#v, want %#v", argv, wantArgv)
		}
		close(started)
		return process, nil
	})

	runDone := make(chan error, 1)
	go func() { runDone <- supervisor.Run(context.Background()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not start the process")
	}
	select {
	case state := <-supervisor.Changes():
		if state != bridge.StateConnecting {
			t.Fatalf("state after start = %q, want %q", state, bridge.StateConnecting)
		}
	case <-time.After(time.Second):
		t.Fatal("starting the process did not report connecting")
	}
	supervisor.MarkAlive()
	select {
	case state := <-supervisor.Changes():
		if state != bridge.StateUp {
			t.Fatalf("state = %q, want %q", state, bridge.StateUp)
		}
	case <-time.After(time.Second):
		t.Fatal("MarkAlive did not promote the supervisor")
	}
	if got := supervisor.State(); got != bridge.StateUp {
		t.Fatalf("State() = %q, want %q", got, bridge.StateUp)
	}

	supervisor.Stop()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop")
	}
	if got := supervisor.State(); got != bridge.StateDown {
		t.Fatalf("final State() = %q, want %q", got, bridge.StateDown)
	}
}

func TestSupervisorRestartsAfterProcessExit(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	first := mocks.NewMockProcess(ctrl)
	firstDone := make(chan struct{})
	close(firstDone)
	first.EXPECT().Wait().DoAndReturn(func() error {
		<-firstDone
		return nil
	})

	second := mocks.NewMockProcess(ctrl)
	secondDone := make(chan struct{})
	second.EXPECT().Wait().DoAndReturn(func() error {
		<-secondDone
		return nil
	})
	second.EXPECT().Stop(gomock.Any()).DoAndReturn(func(time.Duration) error {
		close(secondDone)
		return nil
	})

	runner := mocks.NewMockProcessRunner(ctrl)
	supervisor := bridge.New("host", "key", "remote", "/tmp/local.sock", runner)
	startedSecond := make(chan struct{})
	runner.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).Return(first, nil)
	runner.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, []string, string) (bridge.Process, error) {
		close(startedSecond)
		return second, nil
	})

	runDone := make(chan error, 1)
	go func() { runDone <- supervisor.Run(context.Background()) }()
	select {
	case <-startedSecond:
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor did not restart after process exit")
	}
	supervisor.Stop()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after restart")
	}
}

func TestSupervisorStopCallsProcessStopAndClosesChanges(t *testing.T) {
	t.Helper()
	ctrl := gomock.NewController(t)
	process := mocks.NewMockProcess(ctrl)
	done := make(chan struct{})
	process.EXPECT().Wait().DoAndReturn(func() error {
		<-done
		return nil
	})
	process.EXPECT().Stop(gomock.Any()).DoAndReturn(func(time.Duration) error {
		close(done)
		return nil
	})
	runner := mocks.NewMockProcessRunner(ctrl)
	supervisor := bridge.New("host", "key", "remote", "/tmp/local.sock", runner)
	started := make(chan struct{})
	runner.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, []string, string) (bridge.Process, error) {
		close(started)
		return process, nil
	})

	runDone := make(chan error, 1)
	go func() { runDone <- supervisor.Run(context.Background()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not start the process")
	}
	supervisor.Stop()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not finish")
	}
	// A bridge stopped before promotion reports exactly connecting → down and
	// then closes its change stream; nothing else may leak out.
	for _, want := range []bridge.State{bridge.StateConnecting, bridge.StateDown} {
		select {
		case got, ok := <-supervisor.Changes():
			if !ok || got != want {
				t.Fatalf("Changes() = %q (open=%v), want %q", got, ok, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("Changes() did not report %q", want)
		}
	}
	select {
	case got, ok := <-supervisor.Changes():
		if ok {
			t.Fatalf("Changes() still had an unexpected transition: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Changes() was not closed")
	}
}

func TestSupervisorResolvesSSHPerInstance(t *testing.T) {
	t.Helper()
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	firstPath := filepath.Join(firstDir, "ssh")
	secondPath := filepath.Join(secondDir, "ssh")
	writeExecutable(t, firstPath)
	writeExecutable(t, secondPath)
	t.Setenv("PATH", firstDir)
	first := bridge.New("host", "one", "remote", "local", nil)
	t.Setenv("PATH", secondDir)
	second := bridge.New("host", "two", "remote", "local", nil)
	if got := first.SSHPath(); got != firstPath {
		t.Fatalf("first ssh path = %q, want %q", got, firstPath)
	}
	if got := second.SSHPath(); got != secondPath {
		t.Fatalf("second ssh path = %q, want %q", got, secondPath)
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write ssh shim: %v", err)
	}
}
