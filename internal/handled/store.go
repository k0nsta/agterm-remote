// Package handled remembers which agterm panes a hook has already acted on.
// agterm reports a split or scratch as "shown" both when it is created and
// when it is shown again, so without this record every re-show would type the
// remote command a second time into whatever the pane is running by then.
package handled

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Store is the handled-pane set at one JSON file, guarded by a lock file
// beside it: the split and scratch hooks are separate agterm hook lines, and
// agterm runs separate lines concurrently.
type Store struct {
	path string
	now  func() time.Time
}

// pruneGrace is how long a claim survives without its pane appearing in a
// tree snapshot. A hook reads the tree before it takes the lock, so a
// concurrent hook's fresh claim can be missing from that snapshot; only a
// claim older than any in-flight hook is safe to drop.
const pruneGrace = time.Minute

// New constructs a store at path.
func New(path string) *Store {
	return &Store{path: path, now: time.Now}
}

// Claim records paneID and reports whether it was already recorded. Claims
// for panes no longer in live, and older than pruneGrace, are dropped first,
// so the file stays as small as the open panes. A nil live keeps every claim
// (the tree was unreadable).
func (s *Store) Claim(paneID string, live []string) (bool, error) {
	if s == nil {
		return false, errors.New("nil handled-pane store")
	}
	if paneID == "" {
		return false, errors.New("empty pane id")
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	var claimed bool
	err := s.withLock(func() error {
		current, err := s.load()
		if err != nil {
			return err
		}
		at := now().UTC()
		if live != nil {
			prune(current, live, at)
		}
		if _, ok := current[paneID]; ok {
			claimed = true
			return s.write(current)
		}
		current[paneID] = at
		return s.write(current)
	})
	return claimed, err
}

func prune(current map[string]time.Time, live []string, now time.Time) {
	alive := make(map[string]bool, len(live))
	for _, id := range live {
		alive[id] = true
	}
	for id, at := range current {
		if !alive[id] && now.Sub(at) > pruneGrace {
			delete(current, id)
		}
	}
}

func (s *Store) withLock(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create handled-pane directory: %w", err)
	}
	lock, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open handled-pane lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock handled panes: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	return fn()
}

// load treats a corrupt file as empty: losing the record costs at most one
// repeated command in a pane that is re-shown, refusing to run costs the
// feature.
func (s *Store) load() (map[string]time.Time, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]time.Time{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read handled panes: %w", err)
	}
	var claims map[string]time.Time
	if json.Unmarshal(data, &claims) != nil || claims == nil {
		return map[string]time.Time{}, nil
	}
	return claims, nil
}

func (s *Store) write(claims map[string]time.Time) error {
	data, err := json.Marshal(claims)
	if err != nil {
		return fmt.Errorf("encode handled panes: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".handled-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary handled-pane file: %w", err)
	}
	name := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write handled panes: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close handled panes: %w", err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("replace handled panes: %w", err)
	}
	return nil
}
