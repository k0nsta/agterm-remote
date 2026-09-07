package cli

import (
	"encoding/json"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

func TestItemsForSubtitleGolden(t *testing.T) {
	t.Helper()
	items := ItemsFor(
		"home",
		[]remote.Session{
			{Name: "api", Cmds: "claude", State: "active", IdleSecs: 42},
			{Name: "infra", Cmds: "-", IdleSecs: -1},
		},
		[]bindings.Binding{{Row: "row-api", Host: "home", Name: "api"}, {Row: "row-old", Host: "home", Name: "infra"}},
		[]string{"row-api"},
	)
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal picker items: %v", err)
	}
	want := `[{"id":"api","label":"api","subtitle":"claude · bound · 42s"},{"id":"infra","label":"infra","subtitle":"- · stale · -"}]`
	if string(data) != want {
		t.Fatalf("ItemsFor() = %s, want %s", data, want)
	}
}

func TestItemsForUnknownTreeUsesDash(t *testing.T) {
	t.Helper()
	items := ItemsFor("homelab", []remote.Session{{Name: "api", Cmds: "claude", IdleSecs: 61}}, []bindings.Binding{{Row: "row", Name: "api", Host: "homelab"}}, nil)
	if len(items) != 1 || items[0].Subtitle != "claude · - · 1m" {
		t.Fatalf("ItemsFor() = %#v, want unknown row state", items)
	}
}

// TestItemsForIgnoresOtherHostsBindings pins the host half of the lookup: the
// same session name routinely exists on several hosts, so matching on name
// alone reported another host's row state against this host's session.
func TestItemsForIgnoresOtherHostsBindings(t *testing.T) {
	t.Helper()
	items := ItemsFor(
		"home",
		[]remote.Session{{Name: "api", Cmds: "claude", IdleSecs: 5}},
		// A live binding for the same name on a DIFFERENT host.
		[]bindings.Binding{{Row: "row-api", Host: "elsewhere", Name: "api"}},
		[]string{"row-api"},
	)
	if len(items) != 1 {
		t.Fatalf("ItemsFor() returned %d items, want 1", len(items))
	}
	if got, want := items[0].Subtitle, "claude · - · 5s"; got != want {
		t.Fatalf("subtitle = %q, want %q — another host's binding leaked in", got, want)
	}
}
