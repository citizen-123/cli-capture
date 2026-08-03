// Package shellquote makes strings safe to embed in a POSIX shell command line.
package shellquote

import "strings"

// Single wraps s in single quotes, which make every byte literal to a POSIX
// shell — no glob, no word-splitting, no command substitution. An embedded
// quote closes the run, escapes a literal one, and reopens.
func Single(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
