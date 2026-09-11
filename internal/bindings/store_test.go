package bindings

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return New(pathstest.Dirs(t))
}

func binding(t *testing.T, row, host, name string) Binding {
	t.Helper()
	return Binding{
		Row:     row,
		PaneID:  "pane-" + row,
		Pane:    "left",
		Host:    host,
		Name:    name,
		Mux:     "tmux",
		BoundAt: time.Unix(1700000000, 123).UTC(),
	}
}

func TestStoreRoundTrip(t *testing.T) {
	t.Helper()
	store := testStore(t)
	want := []Binding{binding(t, "row-a", "host-a", "api"), binding(t, "row-b", "host-b", "web")}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestStoreRebindReplacesRowAndHostName(t *testing.T) {
	t.Helper()
	store := testStore(t)
	first := binding(t, "row-a", "host-a", "api")
	second := binding(t, "row-a", "host-b", "web")
	third := binding(t, "row-c", "host-b", "web")
	for _, item := range []Binding{first, second, third} {
		if err := store.Bind(item); err != nil {
			t.Fatalf("Bind(%#v) error = %v", item, err)
		}
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []Binding{third}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestStoreMissingFileIsEmpty(t *testing.T) {
	t.Helper()
	store := testStore(t)
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("Load() = %#v, want a non-nil empty slice", got)
	}
}

func TestStoreCorruptFileReturnsError(t *testing.T) {
	t.Helper()
	store := testStore(t)
	if err := os.MkdirAll(store.dirs.Cache, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(store.dirs.Bindings(), []byte("not json"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load() error = nil, want corrupt JSON error")
	}
}

func TestStoreLookupsAndUnbind(t *testing.T) {
	t.Helper()
	store := testStore(t)
	first := binding(t, "row-a", "host-a", "api")
	second := binding(t, "row-b", "host-a", "web")
	third := binding(t, "row-c", "host-b", "api")
	for _, item := range []Binding{first, second, third} {
		if err := store.Bind(item); err != nil {
			t.Fatalf("Bind(%#v) error = %v", item, err)
		}
	}

	if got, ok := store.ByRow("row-a"); !ok || !reflect.DeepEqual(got, first) {
		t.Fatalf("ByRow(row-a) = %#v, %t; want %#v, true", got, ok, first)
	}
	if got, ok := store.ByHostName("host-a", "web"); !ok || !reflect.DeepEqual(got, second) {
		t.Fatalf("ByHostName(host-a, web) = %#v, %t; want %#v, true", got, ok, second)
	}
	if got := store.ForHost("host-a"); !reflect.DeepEqual(got, []Binding{first, second}) {
		t.Fatalf("ForHost(host-a) = %#v, want %#v", got, []Binding{first, second})
	}

	if err := store.UnbindRow("row-a"); err != nil {
		t.Fatalf("UnbindRow() error = %v", err)
	}
	if _, ok := store.ByRow("row-a"); ok {
		t.Fatal("ByRow(row-a) found an unbound row")
	}
	if err := store.UnbindRow("missing"); err != nil {
		t.Fatalf("idempotent UnbindRow() error = %v", err)
	}
}

func TestConcurrentSavesRetainBothBindings(t *testing.T) {
	t.Helper()
	store := testStore(t)
	items := []Binding{binding(t, "row-a", "host-a", "api"), binding(t, "row-b", "host-b", "web")}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, len(items))
	for _, item := range items {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- store.Save([]Binding{item})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Save() error = %v", err)
		}
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got) != len(items) {
		t.Fatalf("Load() after concurrent Save = %#v, want both bindings", got)
	}
	for _, want := range items {
		if !containsBinding(t, got, want) {
			t.Errorf("concurrent Save lost %#v; got %#v", want, got)
		}
	}
}

func containsBinding(t *testing.T, items []Binding, want Binding) bool {
	t.Helper()
	for _, item := range items {
		if reflect.DeepEqual(item, want) {
			return true
		}
	}
	return false
}

func TestBindingJSONIncludesPane(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(binding(t, "row", "host", "name"))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	text := string(data)
	for _, field := range []string{`"pane_id"`, `"pane"`, `"bound_at"`} {
		if !strings.Contains(text, field) {
			t.Errorf("JSON %q does not contain %s", text, field)
		}
	}
}
