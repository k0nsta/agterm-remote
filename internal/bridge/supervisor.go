package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	connectionPromotionDelay = 5 * time.Second
	processStopGrace         = 2 * time.Second
	processKillWait          = 1 * time.Second
)

// State is the externally visible bridge state.
type State string

const (
	StateDown State = "down"
	StateUp   State = "up"
	// StateConnecting is a bridge whose SSH process is running but has not yet
	// proven itself — by a first status event or by staying up for
	// connectionPromotionDelay. Before it existed, doctor reported a freshly
	// started, perfectly healthy bridge as "down" for those first seconds.
	StateConnecting State = "connecting"
)

// Supervisor owns one reverse SSH tunnel. A supervisor is intended to be
// run once; Stop makes its Run method return and closes Changes.
type Supervisor struct {
	host       string
	hostKey    string
	remoteSock string
	localSock  string
	sshPath    string
	runner     ProcessRunner

	initOnce  sync.Once
	mu        sync.RWMutex
	state     State
	process   Process
	changes   chan State
	alive     chan struct{}
	stop      chan struct{}
	stopOnce  sync.Once
	closeOnce sync.Once
}

// New constructs a supervisor and resolves ssh for this supervisor only. The
// per-instance lookup is deliberate: tests and callers may change PATH for a
// newly created bridge without affecting an existing one.
func New(host, hostKey, remoteSock, localSock string, runner ProcessRunner) *Supervisor {
	if runner == nil {
		runner = &ExecRunner{}
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		sshPath = "/usr/bin/ssh"
	}
	return &Supervisor{
		host:       host,
		hostKey:    hostKey,
		remoteSock: remoteSock,
		localSock:  localSock,
		sshPath:    sshPath,
		runner:     runner,
	}
}

// SSHPath reports the executable selected for this supervisor. It is useful
// for diagnostics and makes the per-instance path choice observable without
// exposing the rest of the supervisor's implementation state.
func (s *Supervisor) SSHPath() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sshPath
}

func (s *Supervisor) init() {
	s.initOnce.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.state == "" {
			s.state = StateDown
		}
		if s.changes == nil {
			s.changes = make(chan State, 16)
		}
		if s.alive == nil {
			s.alive = make(chan struct{}, 1)
		}
		if s.stop == nil {
			s.stop = make(chan struct{})
		}
	})
}

// State reports the latest bridge state.
func (s *Supervisor) State() State {
	if s == nil {
		return StateDown
	}
	s.init()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Changes returns a channel receiving state transitions. It is closed when
// Run returns.
func (s *Supervisor) Changes() <-chan State {
	if s == nil {
		closed := make(chan State)
		close(closed)
		return closed
	}
	s.init()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.changes
}

// MarkAlive promotes a running bridge immediately, instead of waiting for
// the five-second connection timer. Notifications received before a process
// starts are ignored because they cannot prove that the new tunnel is alive.
func (s *Supervisor) MarkAlive() {
	if s == nil {
		return
	}
	s.init()
	s.mu.RLock()
	alive, running := s.alive, s.process != nil
	s.mu.RUnlock()
	if !running {
		return
	}
	select {
	case alive <- struct{}{}:
	default:
	}
}

// Stop asks Run to stop the current process and return. Run performs the
// actual Process.Stop call so there is one owner of process shutdown.
func (s *Supervisor) Stop() {
	if s == nil {
		return
	}
	s.init()
	s.stopOnce.Do(func() { close(s.stop) })
}

// Run supervises the tunnel until ctx is canceled or Stop is called.
func (s *Supervisor) Run(ctx context.Context) error {
	if s == nil {
		return errors.New("nil bridge supervisor")
	}
	if ctx == nil {
		return errors.New("nil bridge context")
	}
	s.init()
	defer s.finish()

	var previous time.Duration
	for {
		if s.stopping(ctx) {
			return nil
		}

		process, err := s.start(ctx)
		if err != nil {
			if s.stopping(ctx) {
				return nil
			}
			log.Printf("warning: start SSH bridge for %s: %v", s.host, err)
			previous = Next(previous, 0)
			if !s.wait(ctx, previous) {
				return nil
			}
			continue
		}

		started := time.Now()
		s.setState(StateConnecting)
		waitCh := make(chan error, 1)
		go func() { waitCh <- process.Wait() }()

		timer := time.NewTimer(connectionPromotionDelay)
		promoted := false
		processExited := false
		for !processExited {
			select {
			case <-ctx.Done():
				timer.Stop()
				s.stopProcess(process, waitCh)
				return nil
			case <-s.stop:
				timer.Stop()
				s.stopProcess(process, waitCh)
				return nil
			case <-s.alive:
				if !promoted {
					promoted = true
					s.setState(StateUp)
				}
			case <-timer.C:
				if !promoted {
					promoted = true
					s.setState(StateUp)
				}
			case <-waitCh:
				processExited = true
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		s.clearProcess(process)
		s.setState(StateDown)
		previous = Next(previous, time.Since(started))
		if !s.wait(ctx, previous) {
			return nil
		}
	}
}

func (s *Supervisor) start(ctx context.Context) (Process, error) {
	s.mu.RLock()
	runner := s.runner
	sshPath := s.sshPath
	s.mu.RUnlock()
	if runner == nil {
		runner = &ExecRunner{}
	}
	if sshPath == "" {
		if found, err := exec.LookPath("ssh"); err == nil {
			sshPath = found
		} else {
			sshPath = "/usr/bin/ssh"
		}
	}
	argv := []string{
		sshPath,
		"-N",
		"-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
		"-R", s.remoteSock + ":" + s.localSock,
		s.host,
	}
	process, err := runner.Start(ctx, argv, s.logPath())
	if err == nil {
		s.mu.Lock()
		s.process = process
		s.mu.Unlock()
	}
	return process, err
}

func (s *Supervisor) logPath() string {
	if s.localSock == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.localSock), "bridge-"+s.hostKey+".log")
}

func (s *Supervisor) clearProcess(process Process) {
	s.mu.Lock()
	s.process = nil
	s.mu.Unlock()
}

func (s *Supervisor) stopProcess(process Process, waitCh <-chan error) {
	if err := process.Stop(processStopGrace); err != nil {
		log.Printf("warning: stop SSH bridge for %s: %v", s.host, err)
	}
	select {
	case <-waitCh:
	case <-time.After(processKillWait):
		log.Printf("warning: SSH bridge for %s did not report exit", s.host)
	}
	s.clearProcess(process)
	s.setState(StateDown)
}

func (s *Supervisor) wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.stop:
		return false
	case <-timer.C:
		return true
	}
}

func (s *Supervisor) stopping(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *Supervisor) setState(state State) {
	s.mu.Lock()
	if s.state == state {
		s.mu.Unlock()
		return
	}
	s.state = state
	changes := s.changes
	s.mu.Unlock()
	select {
	case changes <- state:
	default:
		log.Printf("warning: dropping bridge state change for %s: %s", s.host, state)
	}
}

func (s *Supervisor) finish() {
	s.closeOnce.Do(func() {
		s.mu.RLock()
		changes := s.changes
		s.mu.RUnlock()
		close(changes)
	})
}

// ExecRunner starts commands in their own process group and writes both
// stdout and stderr to the supplied bridge log. Stop terminates the complete
// group, including descendants spawned by a shell-based test shim or ssh.
type ExecRunner struct{}

func (r *ExecRunner) Start(ctx context.Context, argv []string, logPath string) (Process, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("empty process argv")
	}
	if ctx == nil {
		return nil, errors.New("nil process context")
	}

	command := exec.Command(argv[0], argv[1:]...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output := io.Discard
	var logFile *os.File
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
			return nil, fmt.Errorf("create bridge log directory: %w", err)
		}
		file, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open bridge log: %w", err)
		}
		logFile = file
		output = file
	}
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return nil, fmt.Errorf("start %q: %w", argv[0], err)
	}

	process := &execProcess{command: command, pgid: command.Process.Pid, done: make(chan struct{}), logFile: logFile}
	go process.wait()
	go func() {
		select {
		case <-ctx.Done():
			_ = process.Stop(processStopGrace)
		case <-process.done:
		}
	}()
	return process, nil
}

var _ ProcessRunner = (*ExecRunner)(nil)

type execProcess struct {
	command *exec.Cmd
	pgid    int
	done    chan struct{}
	logFile *os.File

	mu       sync.RWMutex
	waitErr  error
	stopOnce sync.Once
	stopErr  error
}

func (p *execProcess) wait() {
	err := p.command.Wait()
	p.mu.Lock()
	p.waitErr = err
	p.mu.Unlock()
	if p.logFile != nil {
		_ = p.logFile.Close()
	}
	close(p.done)
}

func (p *execProcess) Wait() error {
	<-p.done
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.waitErr
}

func (p *execProcess) Stop(grace time.Duration) error {
	p.stopOnce.Do(func() {
		if processDone(p.done) {
			return
		}
		if err := signalGroup(p.pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			p.stopErr = fmt.Errorf("send SIGTERM to process group: %w", err)
			return
		}
		if !waitDone(p.done, grace) {
			if err := signalGroup(p.pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				p.stopErr = fmt.Errorf("send SIGKILL to process group: %w", err)
				return
			}
			if !waitDone(p.done, processKillWait) {
				p.stopErr = errors.New("process did not exit after SIGKILL")
			}
		}
	})
	return p.stopErr
}

func signalGroup(pgid int, signal syscall.Signal) error {
	if pgid <= 0 {
		return errors.New("invalid process group id")
	}
	return syscall.Kill(-pgid, signal)
}

func processDone(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func waitDone(done <-chan struct{}, duration time.Duration) bool {
	if duration <= 0 {
		return processDone(done)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return processDone(done)
	}
}
