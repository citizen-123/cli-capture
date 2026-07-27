// Package repeater provides variable substitution and request replaying — the
// primitive under Burp-style Repeater (tweak a request and resend) and Sniper
// (iterate an insertion point over a payload list) workflows.
//
// Variables are written {{name}} anywhere in a request's URL, header values, or
// body. A name may contain letters, digits, and the characters _ . - . Optional
// whitespace inside the braces is allowed: {{ name }}.
package repeater

import "regexp"

var varRe = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.\-]+)\s*\}\}`)

// Variables returns the distinct variable names referenced in s, in first-seen
// order.
func Variables(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range varRe.FindAllStringSubmatch(s, -1) {
		if name := m[1]; !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// Substitute replaces every {{name}} in s with vars[name]. Unknown variables
// are left as the literal {{name}} so unfilled slots stay visible rather than
// silently becoming empty.
func Substitute(s string, vars map[string]string) string {
	return varRe.ReplaceAllStringFunc(s, func(match string) string {
		name := varRe.FindStringSubmatch(match)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		return match
	})
}
