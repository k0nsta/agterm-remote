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
	items := ItemsFor([]remote.Session{{Name: "api", Cmds: "claude", IdleSecs: 61}}, []bindings.Binding{{Row: "row", Name: "api"}}, nil)
	if len(items) != 1 || items[0].Subtitle != "claude · - · 1m" {
		t.Fatalf("ItemsFor() = %#v, want unknown row state", items)
	}
}
