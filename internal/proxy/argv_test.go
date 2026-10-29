package proxy

import (
	"reflect"
	"strings"
	"testing"
)

func mustLeaf(t *testing.T, name string) *leafSpec {
	t.Helper()
	for i := range allowlist {
		if allowlist[i].name() == name {
			return &allowlist[i]
		}
	}
	t.Fatalf("no leaf %q", name)
	return nil
}

// sameStrings compares two lists, treating nil and empty as equal.
func sameStrings(t *testing.T, got, want []string) bool {
	t.Helper()
	return len(got) == len(want) && (len(got) == 0 || reflect.DeepEqual(got, want))
}

func optNames(t *testing.T, args parsedArgs) []string {
	t.Helper()
	names := make([]string, 0, len(args.opts))
	for _, opt := range args.opts {
		names = append(names, opt.spec.name+"="+opt.value)
	}
	return names
}

func TestParseArgsGrammar(t *testing.T) {
	tests := []struct {
		name     string
		leaf     string
		args     []string
		wantOpts []string
		wantPos  []string
		wantErr  string
	}{
		{name: "space form", leaf: "session overlay open", args: []string{"--size-percent", "70", "--js"},
			wantOpts: []string{"size-percent=70", "js="}},
		{name: "equals form", leaf: "session overlay open", args: []string{"--size-percent=70", "--html=@0"},
			wantOpts: []string{"size-percent=70", "html=@0"}},
		{name: "equals value may start with a dash", leaf: "session overlay submit", args: []string{"--value=-1"},
			wantOpts: []string{"value=-1"}},
		{name: "options after positionals", leaf: "session overlay open", args: []string{"htop", "--block", "--size-percent", "50"},
			wantOpts: []string{"block=", "size-percent=50"}, wantPos: []string{"htop"}},
		{name: "double dash ends options", leaf: "session overlay open", args: []string{"--block", "--", "ls", "--all"},
			wantOpts: []string{"block="}, wantPos: []string{"ls", "--all"}},
		{name: "repeat of a repeatable option", leaf: "session close", args: []string{"--target", "a", "--target=b"},
			wantOpts: []string{"target=a", "target=b"}},
		{name: "window and socket are stripped", leaf: "tree", args: []string{"--window", "w1", "--socket=/x", "--json"},
			wantOpts: []string{"json="}},
		{name: "lone dash is a positional", leaf: "session rename", args: []string{"-"}, wantPos: []string{"-"}},
		{name: "repeat of a single option", leaf: "session overlay open", args: []string{"--js", "--js"}, wantErr: "more than once"},
		{name: "unknown option", leaf: "session type", args: []string{"--select"}, wantErr: "--select is not allowed"},
		{name: "named refused option", leaf: "session overlay open", args: []string{"--persistent"}, wantErr: "keeps a browser login"},
		{name: "short option", leaf: "session type", args: []string{"-s"}, wantErr: "short option"},
		{name: "value missing", leaf: "session overlay open", args: []string{"--size-percent"}, wantErr: "needs a value"},
		{name: "value looks like an option", leaf: "session overlay submit", args: []string{"--value", "--js"}, wantErr: "looks like an option"},
		{name: "flag with a value", leaf: "session overlay open", args: []string{"--js=1"}, wantErr: "takes no value"},
		{name: "enum value", leaf: "session overlay close", args: []string{"--pane", "middle"}, wantErr: "not allowed"},
		{name: "NUL in a positional", leaf: "session rename", args: []string{"a\x00b"}, wantErr: "NUL"},
		{name: "NUL in a value", leaf: "session overlay submit", args: []string{"--value=a\x00b"}, wantErr: "NUL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(mustLeaf(t, tt.leaf), tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseArgs() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArgs() error = %v", err)
			}
			if gotOpts := optNames(t, got); !sameStrings(t, gotOpts, tt.wantOpts) {
				t.Fatalf("opts = %v, want %v", gotOpts, tt.wantOpts)
			}
			if !sameStrings(t, got.pos, tt.wantPos) {
				t.Fatalf("pos = %v, want %v", got.pos, tt.wantPos)
			}
		})
	}
}

func TestParseArgsHelpIgnoresEverythingElse(t *testing.T) {
	got, err := parseArgs(mustLeaf(t, "session type"), []string{"--help", "--target", "x", "text"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if !got.help || len(got.opts) != 0 || len(got.pos) != 0 {
		t.Fatalf("parseArgs() = %#v, want only help", got)
	}
}

func TestMatchLeaf(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		wantLeaf string
		wantRest []string
		wantErr  string
	}{
		{name: "longest path wins", argv: []string{"session", "hud", "update", "hi"}, wantLeaf: "session hud update", wantRest: []string{"hi"}},
		{name: "hud default open", argv: []string{"session", "hud", "hello"}, wantLeaf: "session hud", wantRest: []string{"hello"}},
		{name: "top level", argv: []string{"tree", "--json"}, wantLeaf: "tree", wantRest: []string{"--json"}},
		{name: "empty", argv: nil, wantErr: "no command"},
		{name: "refused top level", argv: []string{"pick", "open"}, wantErr: "`pick` is not available"},
		{name: "refused session leaf", argv: []string{"session", "scratch", "on"}, wantErr: "`session scratch` is not available"},
		{name: "refused nested", argv: []string{"session", "split", "close"}, wantErr: "`session split close` is not available"},
		{name: "unknown", argv: []string{"session", "frobnicate"}, wantErr: "not in the allowlist"},
		{name: "options before the path are not a path", argv: []string{"--json", "tree"}, wantErr: "not in the allowlist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaf, rest, err := matchLeaf(tt.argv)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("matchLeaf() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("matchLeaf() error = %v", err)
			}
			if leaf.name() != tt.wantLeaf || !reflect.DeepEqual(rest, tt.wantRest) {
				t.Fatalf("matchLeaf() = %q %v, want %q %v", leaf.name(), rest, tt.wantLeaf, tt.wantRest)
			}
		})
	}
}
