package proxy_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/proxy"
)

func TestQuoteExactStrings(t *testing.T) {
	t.Helper()
	cases := []struct{ name, in, want string }{
		{"plain", "abc", `'abc'`},
		{"empty", "", `''`},
		{"space", "a b", `'a b'`},
		{"single quote", "it's", `'it'\''s'`},
		{"only quote", "'", `''\'''`},
		{"double quote", `say "hi"`, `'say "hi"'`},
		{"semicolon", "a; rm -rf /", `'a; rm -rf /'`},
		{"command substitution", "$(id)", `'$(id)'`},
		{"variable", "$HOME", `'$HOME'`},
		{"backtick", "`id`", "'`id`'"},
		{"newline", "a\nb", "'a\nb'"},
		{"backslash", `a\b`, `'a\b'`},
	}
	for _, tc := range cases {
		if got := proxy.Quote(tc.in); got != tc.want {
			t.Errorf("%s: Quote(%q) = %s, want %s", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestJoinQuotesEveryWord(t *testing.T) {
	t.Helper()
	got := proxy.Join("/opt/homebrew/bin/agr", "open", "host", "--command", "echo 'a'; $(id)")
	want := `'/opt/homebrew/bin/agr' 'open' 'host' '--command' 'echo '\''a'\''; $(id)'`
	if got != want {
		t.Fatalf("Join() = %s, want %s", got, want)
	}
	if proxy.Join() != "" {
		t.Fatalf("Join() of nothing = %q", proxy.Join())
	}
}

// TestQuotedWordsSurviveASystemShell round-trips the hostile values through
// sh itself: printf prints each argument on its own line, so any word that
// split, expanded or ran something would show.
func TestQuotedWordsSurviveASystemShell(t *testing.T) {
	t.Helper()
	words := []string{"it's", `a "b"`, "x; echo pwned", "$(echo pwned)", "`echo pwned`", "a\nb", `\n`, "$HOME"}
	line := "printf '%s\\0' " + proxy.Join(words...)
	out, err := exec.Command("sh", "-c", line).Output()
	if err != nil {
		t.Fatalf("sh -c %q: %v", line, err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if len(got) != len(words) {
		t.Fatalf("got %d words %q, want %d", len(got), got, len(words))
	}
	for i := range words {
		if got[i] != words[i] {
			t.Errorf("word %d = %q, want %q", i, got[i], words[i])
		}
	}
}
