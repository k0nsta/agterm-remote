package remote

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/token"
)

const (
	defaultVersion = "dev"
	remoteAgrPath  = ".local/bin/agr"
)

// ErrNotInstalled identifies a host that did not return agr's data header.
var ErrNotInstalled = errors.New("remote agr is not installed")

// Runner invokes the installed agr script and caches the remote home needed
// for absolute paths in reverse forwards and interactive commands.
type Runner struct {
	ssh     SSH
	dirs    paths.Dirs
	version string

	homeMu sync.Mutex
}

// NewRunner constructs a runner using the supplied SSH seam. A nil seam uses
// the concrete argv-only ExecSSH adapter.
func NewRunner(ssh SSH, dirs paths.Dirs) *Runner {
	return NewRunnerWithVersion(ssh, dirs, defaultVersion)
}

// NewRunnerWithVersion is NewRunner with the local build version used for the
// remote data-header mismatch warning.
func NewRunnerWithVersion(ssh SSH, dirs paths.Dirs, version string) *Runner {
	if ssh == nil {
		ssh = NewExecSSH()
	}
	if dirs.Cache == "" {
		dirs = paths.New()
	}
	if version == "" {
		version = defaultVersion
	}
	return &Runner{ssh: ssh, dirs: dirs, version: version}
}

// Home returns the cached remote home, probing it once when no host-info file
// exists. The lock covers the read/probe/write sequence so concurrent callers
// cannot issue duplicate probes or race an atomic cache update.
func (r *Runner) Home(ctx context.Context, host string) (string, error) {
	if err := validateHost(host); err != nil {
		return "", err
	}
	if ctx == nil {
		return "", errors.New("nil remote context")
	}

	r.homeMu.Lock()
	defer r.homeMu.Unlock()
	info, err := LoadHostInfo(r.dirs, host)
	if err == nil {
		if info.Home == "" {
			return "", fmt.Errorf("host info for %q has an empty home", host)
		}
		return info.Home, nil
	}
	if !hostInfoMissing(err) {
		return "", err
	}

	// Expansion has to be asked for explicitly. ExecSSH quotes every argv
	// element so a remote path can never be re-parsed as shell syntax, which
	// means a bare "$HOME" argument now arrives literally; the probe therefore
	// runs its own shell, where the expansion is intentional rather than a
	// side effect of how ssh happens to join arguments.
	body, err := r.sshClient().Run(ctx, host, nil, "sh", "-c", `printf %s "$HOME"`)
	if err != nil {
		return "", err
	}
	home := strings.TrimSpace(string(body))
	if home == "" {
		return "", fmt.Errorf("remote host %q returned an empty home", host)
	}
	// A probe that returns anything but an absolute path would otherwise be
	// cached and then used to build every remote path for this host.
	if !strings.HasPrefix(home, "/") {
		return "", fmt.Errorf("remote host %q returned a non-absolute home %q", host, home)
	}
	if err := SaveHostInfo(r.dirs, host, HostInfo{Home: home, ProbedAt: time.Now()}); err != nil {
		return "", err
	}
	return home, nil
}

// EnsureDirs creates the remote directories required by the reverse socket and
// installation. It runs an explicit shell because the paths are $HOME-relative
// and ExecSSH quotes argv: a bare "~/.cache/agr" argument would be created as
// a directory literally named "~".
func (r *Runner) EnsureDirs(ctx context.Context, host string) error {
	if err := validateHost(host); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("nil remote context")
	}
	_, err := r.sshClient().Run(ctx, host, nil, "sh", "-c",
		`mkdir -p "$HOME/.cache/agr" "$HOME/.config/agr" "$HOME/.local/bin"`)
	return err
}

// ResolveAgrPath returns an ABSOLUTE remote agr path, probing $HOME when host
// info is not cached. The result is safe to place in argv: an unexpanded
// "$HOME/..." would reach the remote literally, because ssh's argv is quoted
// (that is what stops injection) and mosh execs argv with no remote shell.
func (r *Runner) ResolveAgrPath(ctx context.Context, host string) (string, error) {
	home, err := r.Home(ctx, host)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, remoteAgrPath), nil
}

// Data invokes a data-producing remote verb, verifies agr's handshake header,
// and returns only the body after that header. An unreachable host is checked
// before header parsing so offline hosts never receive an installation hint.
func (r *Runner) Data(ctx context.Context, host, verb string, args ...string) ([]byte, error) {
	if err := validateHost(host); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, errors.New("nil remote context")
	}
	if verb == "" {
		return nil, errors.New("empty remote verb")
	}
	home, err := r.Home(ctx, host)
	if err != nil {
		return nil, err
	}
	remoteArgv := make([]string, 0, 1+len(args))
	remoteArgv = append(remoteArgv, filepath.Join(home, remoteAgrPath), verb)
	remoteArgv = append(remoteArgv, args...)
	body, runErr := r.sshClient().Run(ctx, host, nil, remoteArgv...)
	if errors.Is(runErr, ErrUnreachable) {
		return body, runErr
	}
	version, payload, headerErr := parseDataHeader(body)
	if headerErr != nil {
		return nil, fmt.Errorf("%w: remote agr on %q is missing or outdated; run: agr install %s", ErrNotInstalled, host, host)
	}
	if version != r.version {
		log.Printf("warning: remote agr version mismatch (local %s, %s %s) — run: agr install %s", r.version, host, version, host)
	}
	if runErr != nil {
		return payload, runErr
	}
	return payload, nil
}

// Sessions lists agr-owned remote sessions.
func (r *Runner) Sessions(ctx context.Context, host string) ([]Session, error) {
	body, err := r.Data(ctx, host, "sessions")
	if err != nil {
		return nil, err
	}
	return ParseSessions(body)
}

// Reap removes one agr-owned remote session. The remote refusal error is
// intentionally returned unchanged so callers can show its stderr text.
func (r *Runner) Reap(ctx context.Context, host, name string) error {
	if !token.Valid(name) {
		return fmt.Errorf("invalid remote session name %q", name)
	}
	_, err := r.Data(ctx, host, "reap", name)
	return err
}

func (r *Runner) sshClient() SSH {
	if r.ssh == nil {
		return NewExecSSH()
	}
	return r.ssh
}

func validateHost(host string) error {
	if !token.ValidHost(host) {
		return fmt.Errorf("invalid remote host %q", host)
	}
	return nil
}

func parseDataHeader(body []byte) (version string, payload []byte, err error) {
	lineEnd := strings.IndexByte(string(body), '\n')
	if lineEnd < 0 {
		line := string(body)
		if !strings.HasPrefix(line, "agr\t") || len(line) == len("agr\t") {
			return "", nil, errors.New("missing agr data header")
		}
		return strings.TrimPrefix(line, "agr\t"), []byte{}, nil
	}
	header := string(body[:lineEnd])
	if !strings.HasPrefix(header, "agr\t") || len(header) == len("agr\t") {
		return "", nil, errors.New("missing agr data header")
	}
	return strings.TrimPrefix(header, "agr\t"), body[lineEnd+1:], nil
}

var _ interface {
	Home(context.Context, string) (string, error)
	Sessions(context.Context, string) ([]Session, error)
	EnsureDirs(context.Context, string) error
} = (*Runner)(nil)
