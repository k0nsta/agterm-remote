// Package bindings persists the mapping between agterm rows and remote
// multiplexer sessions.
package bindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/k0nsta/agterm-remote/internal/paths"
)

// Binding identifies the agterm row that represents one remote multiplexer
// session. Pane is kept in addition to PaneID because agterm's refusal reply
// identifies the owning pane by role.
type Binding struct {
	Row     string    `json:"row"`
	PaneID  string    `json:"pane_id"`
	Pane    string    `json:"pane"`
	Host    string    `json:"host"`
	Name    string    `json:"name"`
	Mux     string    `json:"mux"`
	BoundAt time.Time `json:"bound_at"`
}

// Store is the persistent binding database at dirs.Bindings().
type Store struct {
	dirs paths.Dirs
}

// New constructs a binding store rooted at dirs.
func New(dirs paths.Dirs) *Store {
	return &Store{dirs: dirs}
}

// Load reads all bindings. A missing database is the normal first-run state
// and returns an empty slice. Invalid JSON is returned to the caller rather
// than being treated as an empty database.
func (s *Store) Load() ([]Binding, error) {
	if s == nil {
		return nil, errors.New("nil binding store")
	}
	var result []Binding
	err := s.withLock(func() error {
		var err error
		result, err = s.loadUnlocked()
		return err
	})
	return result, err
}

// Save atomically upserts the supplied bindings while holding the database
// lock. Distinct bindings already written by another process are retained,
// so independent concurrent writers cannot lose one another's rows. Bind is
// the stricter single-binding operation and removes conflicting row/session
// entries before writing.
func (s *Store) Save(next []Binding) error {
	if s == nil {
		return errors.New("nil binding store")
	}
	return s.withLock(func() error {
		current, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		for _, binding := range next {
			current = upsert(current, binding)
		}
		return s.writeUnlocked(current)
	})
}

// Bind stores b, replacing any existing binding for the same agterm row or
// the same host and multiplexer session.
func (s *Store) Bind(binding Binding) error {
	if s == nil {
		return errors.New("nil binding store")
	}
	return s.withLock(func() error {
		current, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		return s.writeUnlocked(upsert(current, binding))
	})
}

// UnbindRow removes the binding for row. It is idempotent.
func (s *Store) UnbindRow(row string) error {
	if s == nil {
		return errors.New("nil binding store")
	}
	return s.withLock(func() error {
		current, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		filtered := current[:0]
		for _, binding := range current {
			if binding.Row != row {
				filtered = append(filtered, binding)
			}
		}
		return s.writeUnlocked(filtered)
	})
}

// ByHostName finds the binding for a remote host and multiplexer session.
func (s *Store) ByHostName(host, name string) (Binding, bool) {
	bindings, err := s.Load()
	if err != nil {
		return Binding{}, false
	}
	for _, binding := range bindings {
		if binding.Host == host && binding.Name == name {
			return binding, true
		}
	}
	return Binding{}, false
}

// ByRow finds the binding for an agterm row.
func (s *Store) ByRow(row string) (Binding, bool) {
	bindings, err := s.Load()
	if err != nil {
		return Binding{}, false
	}
	for _, binding := range bindings {
		if binding.Row == row {
			return binding, true
		}
	}
	return Binding{}, false
}

// ForHost returns bindings belonging to host in database order. A corrupt or
// unreadable database returns nil; callers that need the error should use
// Load directly.
func (s *Store) ForHost(host string) []Binding {
	bindings, err := s.Load()
	if err != nil {
		return nil
	}
	result := make([]Binding, 0)
	for _, binding := range bindings {
		if binding.Host == host {
			result = append(result, binding)
		}
	}
	return result
}

// Dangling returns this host's bindings whose agterm rows are absent from
// live. The input is treated as a set and result order follows the database.
func (s *Store) Dangling(host string, live []string) []Binding {
	bindings, err := s.Load()
	if err != nil {
		return nil
	}
	liveSet := make(map[string]struct{}, len(live))
	for _, row := range live {
		liveSet[row] = struct{}{}
	}
	result := make([]Binding, 0)
	for _, binding := range bindings {
		if binding.Host != host {
			continue
		}
		if _, ok := liveSet[binding.Row]; !ok {
			result = append(result, binding)
		}
	}
	return result
}

// Reconcile removes bindings whose rows are not in liveRows and returns the
// number removed. It is an explicit destructive operation; unlike a listing
// read, it is persisted under the same transaction lock as Bind.
func (s *Store) Reconcile(liveRows []string) (removed int, err error) {
	if s == nil {
		return 0, errors.New("nil binding store")
	}
	err = s.withLock(func() error {
		current, loadErr := s.loadUnlocked()
		if loadErr != nil {
			return loadErr
		}
		liveSet := make(map[string]struct{}, len(liveRows))
		for _, row := range liveRows {
			liveSet[row] = struct{}{}
		}
		filtered := current[:0]
		for _, binding := range current {
			if _, ok := liveSet[binding.Row]; ok {
				filtered = append(filtered, binding)
			} else {
				removed++
			}
		}
		return s.writeUnlocked(filtered)
	})
	return removed, err
}

func upsert(current []Binding, next Binding) []Binding {
	filtered := make([]Binding, 0, len(current)+1)
	for _, binding := range current {
		if binding.Row == next.Row || (binding.Host == next.Host && binding.Name == next.Name) {
			continue
		}
		filtered = append(filtered, binding)
	}
	return append(filtered, next)
}

func (s *Store) withLock(fn func() error) error {
	if err := os.MkdirAll(s.dirs.Cache, 0o700); err != nil {
		return fmt.Errorf("create agr cache: %w", err)
	}
	// The binding file is replaced after each write. A separate stable inode is
	// therefore required: flocking the file itself would stop protecting the
	// next writer immediately after os.Rename.
	lockPath := s.dirs.Bindings() + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open binding lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock bindings: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	return fn()
}

func (s *Store) loadUnlocked() ([]Binding, error) {
	data, err := os.ReadFile(s.dirs.Bindings())
	if errors.Is(err, os.ErrNotExist) {
		return []Binding{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read bindings: %w", err)
	}
	var result []Binding
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode bindings: %w", err)
	}
	if result == nil {
		result = []Binding{}
	}
	return result, nil
}

func (s *Store) writeUnlocked(bindings []Binding) error {
	if err := os.MkdirAll(filepath.Dir(s.dirs.Bindings()), 0o700); err != nil {
		return fmt.Errorf("create bindings directory: %w", err)
	}
	data, err := json.MarshalIndent(bindings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode bindings: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.dirs.Bindings()), ".bindings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary bindings file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("set temporary bindings permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temporary bindings file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary bindings file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary bindings file: %w", err)
	}
	if err := os.Rename(tmpName, s.dirs.Bindings()); err != nil {
		return fmt.Errorf("replace bindings: %w", err)
	}
	return nil
}
