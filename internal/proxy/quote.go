// Package proxy holds the Mac-side pieces of the agtermctl proxy.
package proxy

import "strings"

// Quote renders value as one single-quoted word of the shell line agterm runs
// on the Mac. Single quotes are the only fully literal quoting in sh, so an
// embedded ' is closed, escaped and reopened; nothing else in value (a
// double quote, ;, $(), a backtick, a newline) is special inside them.
func Quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// Join quotes every word and joins them with spaces: one command line in which
// each word survives one round of shell parsing intact.
func Join(words ...string) string {
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		quoted = append(quoted, Quote(word))
	}
	return strings.Join(quoted, " ")
}
