package scope

import (
	"fmt"
	"strings"
)

// ParseCondition parses a compact spec into a Condition. Grammar:
//
//	[!][field:]pattern
//
//	!         negate the match
//	field:    host|path|method|proto|body|any   (default: host)
//	pattern   strategy inferred from a prefix or content:
//	            ~re       regular expression   ("host:~^api\\.")
//	            =exact    full-string equality ("method:=GET")
//	            has * ?   glob                 ("host:*.github.com")
//	            else      substring            ("host:github")
//
// Examples:
//
//	"*.github.com"        host glob
//	"path:/v1/*"          path glob
//	"method:=POST"        exact method
//	"host:~^(api|cdn)\\." host regex
//	"!body:password"      NOT containing "password" in the body
func ParseCondition(spec string) (Condition, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Condition{}, fmt.Errorf("scope: empty condition")
	}
	c := Condition{Field: FieldHost}

	if strings.HasPrefix(spec, "!") {
		c.Negate = true
		spec = spec[1:]
	}
	if i := strings.IndexByte(spec, ':'); i >= 0 {
		if f, ok := fieldNames[strings.ToLower(spec[:i])]; ok {
			c.Field = f
			spec = spec[i+1:]
		}
	}

	switch {
	case strings.HasPrefix(spec, "~"):
		c.Strategy, c.Pattern = Regex, spec[1:]
	case strings.HasPrefix(spec, "="):
		c.Strategy, c.Pattern = Exact, spec[1:]
	case strings.ContainsAny(spec, "*?"):
		c.Strategy, c.Pattern = Glob, spec
	default:
		c.Strategy, c.Pattern = Substr, spec
	}
	if err := c.Compile(); err != nil {
		return c, fmt.Errorf("scope: bad pattern %q: %w", spec, err)
	}
	return c, nil
}

// Rule1 builds a single-condition rule from a spec.
func Rule1(action Action, spec string) (*Rule, error) {
	c, err := ParseCondition(spec)
	if err != nil {
		return nil, err
	}
	return &Rule{Name: action.String() + " " + spec, Action: action, Enabled: true, Conditions: []Condition{c}}, nil
}

// Build assembles a first-match-wins Set from include and exclude spec lists.
// Excludes are placed first so they take precedence over overlapping includes.
// defaultAll chooses the fallback when nothing matches: true ⇒ Include
// everything not excluded (denylist), false ⇒ Exclude everything not explicitly
// included (allowlist).
func Build(includes, excludes []string, defaultAll bool) (*Set, error) {
	s := &Set{Default: Exclude}
	if defaultAll {
		s.Default = Include
	}
	for _, spec := range excludes {
		r, err := Rule1(Exclude, spec)
		if err != nil {
			return nil, err
		}
		s.Rules = append(s.Rules, r)
	}
	for _, spec := range includes {
		r, err := Rule1(Include, spec)
		if err != nil {
			return nil, err
		}
		s.Rules = append(s.Rules, r)
	}
	return s, nil
}

// Describe renders a one-line summary of a Set for logs/status.
func (s *Set) Describe() string {
	parts := []string{"default=" + s.Default.String()}
	for _, r := range s.Rules {
		if r.Enabled {
			parts = append(parts, r.Name)
		}
	}
	return strings.Join(parts, ", ")
}
