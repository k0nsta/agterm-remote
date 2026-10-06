package handled

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cache", "handled-panes.json")
	return New(path), path
}

func TestClaimReportsASecondClaimOfThePane(t *testing.T) {
	t.Helper()
	store, _ := newStore(t)
	if already, err := store.Claim("P1", []string{"P1"}); err != nil || already {
		t.Fatalf("first Claim() = (%v, %v), want (false, nil)", already, err)
	}
	if already, err := store.Claim("P1", []string{"P1"}); err != nil || !already {
		t.Fatalf("second Claim() = (%v, %v), want (true, nil)", already, err)
	}
}

func TestClaimPrunesPanesThatAreGoneOnlyAfterTheGrace(t *testing.T) {
	t.Helper()
	store, _ := newStore(t)
	clock := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	if _, err := store.Claim("old", []string{"old"}); err != nil {
		t.Fatal(err)
	}
	// A concurrent hook's snapshot predates "old": within the grace the
	// claim must survive, or the next re-show of "old" types again.
	clock = clock.Add(10 * time.Second)
	if _, err := store.Claim("new", []string{"new"}); err != nil {
		t.Fatal(err)
	}
	if already, _ := store.Claim("old", []string{"old", "new"}); !already {
		t.Fatal("a stale snapshot pruned a claim inside the grace")
	}
	clock = clock.Add(2 * pruneGrace)
	if _, err := store.Claim("new", []string{"new"}); err != nil {
		t.Fatal(err)
	}
	if already, err := store.Claim("old", []string{"old", "new"}); err != nil || already {
		t.Fatalf("Claim(old) after the grace = (%v, %v), want a fresh claim", already, err)
	}
}

func TestClaimKeepsALivePaneHoweverOld(t *testing.T) {
	t.Helper()
	store, _ := newStore(t)
	clock := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	if _, err := store.Claim("P", []string{"P"}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(24 * time.Hour)
	if already, _ := store.Claim("P", []string{"P"}); !already {
		t.Fatal("a pane still in the tree was pruned")
	}
}

func TestClaimWithUnknownTreeKeepsEntries(t *testing.T) {
	t.Helper()
	store, _ := newStore(t)
	if _, err := store.Claim("P1", []string{"P1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim("P2", nil); err != nil {
		t.Fatal(err)
	}
	if already, _ := store.Claim("P1", nil); !already {
		t.Fatal("a nil live list pruned an entry")
	}
}

func TestClaimTreatsACorruptFileAsEmpty(t *testing.T) {
	t.Helper()
	store, path := newStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if already, err := store.Claim("P1", nil); err != nil || already {
		t.Fatalf("Claim() on corrupt file = (%v, %v), want (false, nil)", already, err)
	}
}

func TestClaimRejectsEmptyPane(t *testing.T) {
	t.Helper()
	store, _ := newStore(t)
	if _, err := store.Claim("", nil); err == nil {
		t.Fatal("Claim(\"\") error = nil")
	}
}

func TestConcurrentClaimsOfOnePaneHaveOneWinner(t *testing.T) {
	t.Helper()
	store, _ := newStore(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	fresh := 0
	for range 16 {
		wg.Go(func() {
			already, err := store.Claim("P", nil)
			if err != nil {
				t.Errorf("Claim() error = %v", err)
				return
			}
			if !already {
				mu.Lock()
				fresh++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if fresh != 1 {
		t.Fatalf("fresh claims = %d, want exactly 1", fresh)
	}
}
