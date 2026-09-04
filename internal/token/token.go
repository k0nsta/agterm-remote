// Package token validates values that are passed between agr's local and
// remote components or used in its cache paths.
package token

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Valid reports whether s is a safe agr token. Tokens use an ASCII
// alphanumeric, dot, underscore, and hyphen alphabet, but cannot begin with a
// path or option marker.
func Valid(s string) bool {
	if s == "" || s[0] == '-' || s[0] == '.' {
		return false
	}

	for i := 0; i < len(s); i++ {
		if !validChar(s[i]) {
			return false
		}
	}
	return true
}

// ValidState reports whether s is one of the states understood by agterm.
func ValidState(s string) bool {
	switch s {
	case "idle", "active", "completed", "blocked":
		return true
	default:
		return false
	}
}

// ValidHost reports whether s can be passed as an SSH destination without
// being interpreted as shell syntax. The caller still passes the value as a
// distinct argv element; this validation also protects remote command and
// cache-path boundaries that use the same input.
func ValidHost(s string) bool {
	if s == "" || s[0] == '-' {
		return false
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e || isShellMeta(c) {
			return false
		}
	}
	return true
}

// FileKey returns a stable, filename-safe key for host. Characters in the
// token alphabet are retained for readability; all other bytes are replaced
// with underscores. A hash suffix keeps hosts with the same sanitized form
// distinct.
func FileKey(host string) string {
	var b strings.Builder
	b.Grow(len(host) + 1 + 12)
	for i := 0; i < len(host); i++ {
		if validChar(host[i]) {
			b.WriteByte(host[i])
		} else {
			b.WriteByte('_')
		}
	}

	sum := sha256.Sum256([]byte(host))
	b.WriteByte('-')
	b.WriteString(hex.EncodeToString(sum[:6]))
	return b.String()
}

func validChar(c byte) bool {
	return c >= 'A' && c <= 'Z' ||
		c >= 'a' && c <= 'z' ||
		c >= '0' && c <= '9' ||
		c == '_' || c == '.' || c == '-'
}

func isShellMeta(c byte) bool {
	switch c {
	case '\\', '\'', '"', '`', '$', ';', '&', '|', '(', ')', '<', '>', '*', '?', '[', ']', '{', '}', '!', '#', '~':
		return true
	default:
		return false
	}
}
