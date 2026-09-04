package receiver

import (
	"reflect"
	"testing"
)

func TestDecode(t *testing.T) {
	t.Helper()
	tests := []struct {
		name string
		line string
		want Event
		ok   bool
	}{
		{
			name: "agr shape",
			line: `{"cmd":"session-status","session":"api","state":"active","args":["--blink","--unknown","--auto-reset"],"pane":"right","pane_id":"pane-1"}`,
			want: Event{Host: "host-a", Session: "api", State: "active", Pane: "right", PaneID: "pane-1", Blink: true, AutoReset: true},
			ok:   true,
		},
		{
			name: "cookbook shape",
			line: `{"cmd":"session-status","session_id":"row-1","state":"blocked","pane":"left","pane_id":"surface-1","args":[]}`,
			want: Event{Host: "host-a", SessionID: "row-1", State: "blocked", Pane: "left", PaneID: "surface-1"},
			ok:   true,
		},
		{
			name: "extra args are dropped",
			line: `{"cmd":"session-status","session":"api","state":"idle","args":["--blink","--ignored","--blink"]}`,
			want: Event{Host: "host-a", Session: "api", State: "idle", Blink: true},
			ok:   true,
		},
		{name: "missing state", line: `{"cmd":"session-status","session":"api"}`},
		{name: "bad state", line: `{"cmd":"session-status","session":"api","state":"running"}`},
		{name: "malformed json", line: `{"cmd":"session-status"`},
		{name: "unknown command", line: `{"cmd":"other","session":"api","state":"active"}`},
		{name: "bad agr token", line: `{"cmd":"session-status","session":"api/name","state":"active"}`},
		{name: "bad cookbook token", line: `{"cmd":"session-status","session_id":"-row","state":"active"}`},
		{name: "missing target", line: `{"cmd":"session-status","state":"active"}`},
		{name: "ambiguous target", line: `{"cmd":"session-status","session":"api","session_id":"row-1","state":"active"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			got, err := Decode("host-a", []byte(tt.line))
			if tt.ok {
				if err != nil {
					t.Fatalf("Decode() error = %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("Decode() = %#v, want %#v", got, tt.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("Decode() error = nil, want rejection for %s", tt.name)
			}
		})
	}
}
