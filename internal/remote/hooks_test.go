package remote

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMergeHooksTable(t *testing.T) {
	t.Helper()
	tests := []struct {
		name        string
		existing    string
		wantChanged bool
		checks      []string
	}{
		{
			name:        "missing settings",
			existing:    "{}",
			wantChanged: true,
			checks: []string{
				`"UserPromptSubmit"`,
				`$HOME/.local/bin/agr status active --blink`,
				`$HOME/.local/bin/agr status blocked`,
			},
		},
		{
			name:        "foreign keys kept",
			existing:    `{"enabled":true,"hooks":{"Other":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`,
			wantChanged: true,
			checks:      []string{`"enabled": true`, `"Other"`, `echo keep`},
		},
		{
			name:        "one bucket already wired",
			existing:    `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"$HOME/.local/bin/agr status active --blink"}]}]}}`,
			wantChanged: true,
			checks:      []string{`"PostToolUse"`, `"Stop"`},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Helper()
			got, changed, err := MergeHooks([]byte(test.existing))
			if err != nil {
				t.Fatalf("MergeHooks() error = %v", err)
			}
			if changed != test.wantChanged {
				t.Fatalf("MergeHooks() changed = %v, want %v", changed, test.wantChanged)
			}
			for _, check := range test.checks {
				if !bytes.Contains(got, []byte(check)) {
					t.Fatalf("MergeHooks() output missing %q:\n%s", check, got)
				}
			}
			if !json.Valid(got) || !strings.Contains(string(got), "\n  ") {
				t.Fatalf("MergeHooks() output is not indented JSON:\n%s", got)
			}
		})
	}
}

func TestMergeHooksReplacesLegacyCommands(t *testing.T) {
	t.Helper()
	legacy := `{
  "hooks": {
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "$HOME/.local/bin/agr active --blink"}]}],
    "PostToolUse": [{"hooks": [{"type": "command", "command": "/tmp/agr active --blink"}]}],
    "Notification": [{"matcher": "permission_prompt", "hooks": [{"type": "command", "command": "$HOME/.local/bin/agr blocked"}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "$HOME/.local/bin/agr completed --auto-reset"}]}]
  }
}`
	got, changed, err := MergeHooks([]byte(legacy))
	if err != nil {
		t.Fatalf("MergeHooks() error = %v", err)
	}
	if !changed {
		t.Fatal("MergeHooks() changed = false, want legacy commands replaced")
	}
	if bytes.Contains(got, []byte("/agr active")) || bytes.Contains(got, []byte("/agr blocked")) || bytes.Contains(got, []byte("/agr completed")) {
		t.Fatalf("MergeHooks() left a legacy command:\n%s", got)
	}
	if bytes.Count(got, []byte("$HOME/.local/bin/agr status active --blink")) != 2 {
		t.Fatalf("MergeHooks() active command count = %d, want 2", bytes.Count(got, []byte("$HOME/.local/bin/agr status active --blink")))
	}
}

func TestMergeHooksIsIdempotent(t *testing.T) {
	t.Helper()
	first, changed, err := MergeHooks([]byte("{}"))
	if err != nil || !changed {
		t.Fatalf("first MergeHooks() = changed %v, error %v", changed, err)
	}
	second, changed, err := MergeHooks(first)
	if err != nil {
		t.Fatalf("second MergeHooks() error = %v", err)
	}
	if changed {
		t.Fatal("second MergeHooks() changed = true, want false")
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("idempotent output differs:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestMergeHooksRejectsMalformedSettings(t *testing.T) {
	t.Helper()
	for _, existing := range []string{"{", `{"hooks":{"Stop":{}}}`, "[]"} {
		if _, changed, err := MergeHooks([]byte(existing)); err == nil || changed {
			t.Fatalf("MergeHooks(%q) = changed %v, error %v; want refusal", existing, changed, err)
		}
	}
}

func TestMergeHooksEmptyInput(t *testing.T) {
	t.Helper()
	merged, changed, err := MergeHooks(nil)
	if err != nil || !changed || !json.Valid(merged) {
		t.Fatalf("MergeHooks(nil) = changed %v, error %v, JSON %q", changed, err, merged)
	}
}
