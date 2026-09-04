package token

import (
	"regexp"
	"testing"
)

func TestValid(t *testing.T) {
	t.Helper()
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "empty", in: "", want: false},
		{name: "leading dash", in: "-x", want: false},
		{name: "leading dot", in: ".x", want: false},
		{name: "dot", in: ".", want: false},
		{name: "dot dot", in: "..", want: false},
		{name: "space", in: "a b", want: false},
		{name: "safe", in: "a-b_c.d", want: true},
		{name: "unicode", in: "café", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			if got := Valid(tt.in); got != tt.want {
				t.Fatalf("Valid(%q) = %t, want %t", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidState(t *testing.T) {
	t.Helper()
	for _, state := range []string{"idle", "active", "completed", "blocked"} {
		state := state
		t.Run("accept_"+state, func(t *testing.T) {
			t.Helper()
			if !ValidState(state) {
				t.Fatalf("ValidState(%q) = false, want true", state)
			}
		})
	}

	for _, state := range []string{"", "unknown", "ACTIVE", "active ", "idle\n"} {
		state := state
		t.Run("reject_"+state, func(t *testing.T) {
			t.Helper()
			if ValidState(state) {
				t.Fatalf("ValidState(%q) = true, want false", state)
			}
		})
	}
}

func TestValidHost(t *testing.T) {
	t.Helper()
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "user at host", in: "user@host", want: true},
		{name: "domain", in: "h1.example.com", want: true},
		{name: "hyphenated", in: "host-1", want: true},
		{name: "space", in: "a b", want: false},
		{name: "semicolon", in: "a;b", want: false},
		{name: "leading dash", in: "-h", want: false},
		{name: "quote", in: `user@host'`, want: false},
		{name: "unicode", in: "höst", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			if got := ValidHost(tt.in); got != tt.want {
				t.Fatalf("ValidHost(%q) = %t, want %t", tt.in, got, tt.want)
			}
		})
	}
}

func TestFileKey(t *testing.T) {
	t.Helper()
	filenameSafe := regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

	first := FileKey("user@h")
	if first != FileKey("user@h") {
		t.Fatalf("FileKey is not stable: %q != %q", first, FileKey("user@h"))
	}
	if first == FileKey("user_h") {
		t.Fatalf("FileKey collision: %q", first)
	}
	if !filenameSafe.MatchString(first) {
		t.Fatalf("FileKey(%q) = %q, want filename-safe output", "user@h", first)
	}
	if FileKey("user@h") != "user_h-"+first[len("user_h-"):] {
		t.Fatalf("FileKey did not retain safe characters: %q", first)
	}

	for _, host := range []string{"", "/tmp/host", "user;host", "höst"} {
		key := FileKey(host)
		if !filenameSafe.MatchString(key) {
			t.Fatalf("FileKey(%q) = %q, want filename-safe output", host, key)
		}
	}
}
