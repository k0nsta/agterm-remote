// Package bindings persists the mapping between agterm panes and remote
// multiplexer sessions. One row can hold two bindings, one per split pane.
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

// Binding identifies the agterm pane that represents one remote multiplexer
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
	// Grants are the local panes this binding's host may type into. They
	// follow a re-bind of the same host and name (see upsert).
	Grants []Grant `json:"grants,omitempty"`
}

// Grant kinds.
const (
	GrantOrchestrator = "orchestrator"
	GrantConsent      = "consent"
)

// Grant lets a host type into one local pane it does not own.
type Grant struct {
	Row    string `json:"row"`
	PaneID string `json:"pane_id"`
	Kind   string `json:"kind"`
}

// ErrNoBinding means a grant was addressed to a binding that no longer exists.
var ErrNoBinding = errors.New("no binding for host and name")

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
// so independent concurrent writers cannot lose one another's panes. Each
// binding replaces the entries upsert considers conflicting.
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

// Bind stores binding, replacing any existing binding for the same agterm pane
// or the same host and multiplexer session (see upsert).
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

// AddGrant adds grant to the binding of host and name, found under the store
// lock. It is an error, not a new entry, when that binding is gone. Adding a
// grant the binding already has changes nothing.
func (s *Store) AddGrant(host, name string, grant Grant) error {
	return s.editGrants(host, name, func(grants []Grant) []Grant {
		for _, have := range grants {
			if have == grant {
				return grants
			}
		}
		return append(grants, grant)
	})
}

// DropGrant removes grant from the binding of host and name. A grant the
// binding does not hold is not an error; a missing binding is.
func (s *Store) DropGrant(host, name string, grant Grant) error {
	return s.editGrants(host, name, func(grants []Grant) []Grant {
		kept := make([]Grant, 0, len(grants))
		for _, have := range grants {
			if have != grant {
				kept = append(kept, have)
			}
		}
		return kept
	})
}

func (s *Store) editGrants(host, name string, edit func([]Grant) []Grant) error {
	if s == nil {
		return errors.New("nil binding store")
	}
	return s.withLock(func() error {
		current, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		for i := range current {
			if current[i].Host != host || current[i].Name != name {
				continue
			}
			current[i].Grants = edit(append([]Grant(nil), current[i].Grants...))
			if len(current[i].Grants) == 0 {
				current[i].Grants = nil
			}
			return s.writeUnlocked(current)
		}
		return fmt.Errorf("%w: %s %s", ErrNoBinding, host, name)
	})
}

// UnbindRow removes every binding of row, both split panes included. It is
// idempotent.
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

// ByRow finds the first binding of an agterm row. A split row can hold two;
// callers acting on the whole row should use ForRow.
func (s *Store) ByRow(row string) (Binding, bool) {
	bindings := s.ForRow(row)
	if len(bindings) == 0 {
		return Binding{}, false
	}
	return bindings[0], true
}

// ForRow returns every binding of an agterm row in database order: one per
// bound pane. A corrupt or unreadable database returns nil.
func (s *Store) ForRow(row string) []Binding {
	bindings, err := s.Load()
	if err != nil {
		return nil
	}
	result := make([]Binding, 0)
	for _, binding := range bindings {
		if binding.Row == row {
			result = append(result, binding)
		}
	}
	return result
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

// upsert appends next after retiring every entry it supersedes:
//   - the same pane token (PaneID), which survives a promote;
//   - the same row and pane slot: nothing announces a split pane's exit and
//     agterm's tree does not list tokens, so a dead right pane is replaced by
//     whatever is opened next in that slot;
//   - the same host and session name, which is bound to one pane at a time.
//
// A replaced entry with the same host and name hands its Grants to next.
//
// When either side has no PaneID (agterm before pane tokens) the whole row is
// replaced, as it was before per-pane bindings.
//
// Known edge, accepted: promote the right pane to left, split again and open
// in the new right pane, and the promoted pane's binding is replaced because
// it still carries the old row and slot.
func upsert(current []Binding, next Binding) []Binding {
	filtered := make([]Binding, 0, len(current)+1)
	for _, binding := range current {
		if supersedes(next, binding) {
			// The same host and name is the same session attached again: it
			// keeps what its host was granted, unless the caller set grants.
			if binding.Host == next.Host && binding.Name == next.Name && next.Grants == nil {
				next.Grants = binding.Grants
			}
			continue
		}
		filtered = append(filtered, binding)
	}
	return append(filtered, next)
}

func supersedes(next, old Binding) bool {
	if old.Host == next.Host && old.Name == next.Name {
		return true
	}
	if old.Row != next.Row {
		return next.PaneID != "" && old.PaneID == next.PaneID
	}
	return next.PaneID == "" || old.PaneID == "" || old.PaneID == next.PaneID || old.Pane == next.Pane
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
