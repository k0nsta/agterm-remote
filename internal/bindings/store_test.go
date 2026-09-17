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

func paneBinding(t *testing.T, row, pane, paneID, host, name string) Binding {
	t.Helper()
	item := binding(t, row, host, name)
	item.Pane = pane
	item.PaneID = paneID
	return item
}

func TestUpsertRetiresSupersededPanes(t *testing.T) {
	t.Helper()
	left := paneBinding(t, "row-1", "left", "tok-left", "homelab", "a")
	right := paneBinding(t, "row-1", "right", "tok-right", "homelab", "b")
	tests := []struct {
		name    string
		current []Binding
		next    Binding
		want    []Binding
	}{
		{
			name:    "two panes in one row coexist",
			current: []Binding{left},
			next:    right,
			want:    []Binding{left, right},
		},
		{
			name:    "same pane token replaces across rows after a promote",
			current: []Binding{left, right},
			next:    paneBinding(t, "row-2", "left", "tok-right", "homelab", "c"),
			want:    []Binding{left, paneBinding(t, "row-2", "left", "tok-right", "homelab", "c")},
		},
		{
			name:    "same row and slot replaces a dead pane's successor",
			current: []Binding{left, right},
			next:    paneBinding(t, "row-1", "right", "tok-new", "homelab", "c"),
			want:    []Binding{left, paneBinding(t, "row-1", "right", "tok-new", "homelab", "c")},
		},
		{
			name:    "same host and name moves the session to the new pane",
			current: []Binding{left, paneBinding(t, "row-2", "left", "tok-other", "homelab", "b")},
			next:    right,
			want:    []Binding{left, right},
		},
		{
			name:    "same name on another host is a different session",
			current: []Binding{left},
			next:    paneBinding(t, "row-2", "left", "tok-other", "elsewhere", "a"),
			want:    []Binding{left, paneBinding(t, "row-2", "left", "tok-other", "elsewhere", "a")},
		},
		{
			name:    "empty token replaces the whole row",
			current: []Binding{left, right, paneBinding(t, "row-2", "left", "tok-other", "homelab", "c")},
			next:    paneBinding(t, "row-1", "right", "", "homelab", "d"),
			want:    []Binding{paneBinding(t, "row-2", "left", "tok-other", "homelab", "c"), paneBinding(t, "row-1", "right", "", "homelab", "d")},
		},
		{
			name:    "a tokenless entry is replaced by any open in its row",
			current: []Binding{paneBinding(t, "row-1", "left", "", "homelab", "a")},
			next:    right,
			want:    []Binding{right},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := upsert(append([]Binding(nil), tt.current...), tt.next)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("upsert() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestStoreSplitRowKeepsBothPanes(t *testing.T) {
	t.Helper()
	store := testStore(t)
	left := paneBinding(t, "row-1", "left", "tok-left", "homelab", "a")
	right := paneBinding(t, "row-1", "right", "tok-right", "homelab", "b")
	for _, item := range []Binding{left, right} {
		if err := store.Bind(item); err != nil {
			t.Fatalf("Bind(%#v) error = %v", item, err)
		}
	}
	if got := store.ForRow("row-1"); !reflect.DeepEqual(got, []Binding{left, right}) {
		t.Fatalf("ForRow(row-1) = %#v, want both panes", got)
	}
	for _, want := range []Binding{left, right} {
		if got, ok := store.ByHostName(want.Host, want.Name); !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("ByHostName(%s, %s) = %#v, %t; want %#v", want.Host, want.Name, got, ok, want)
		}
	}
	if err := store.UnbindRow("row-1"); err != nil {
		t.Fatalf("UnbindRow() error = %v", err)
	}
	if got := store.ForRow("row-1"); len(got) != 0 {
		t.Fatalf("ForRow(row-1) after UnbindRow = %#v, want none", got)
	}
}
