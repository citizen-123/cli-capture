// Package scope is a small, self-contained matching engine used to answer
// yes/no scoping questions about traffic — "should this flow be intercepted?"
// and "should this TLS host be MITM'd?" — from the same rules.
//
// The model is deliberately orthogonal so every posture is expressible as data:
//
//	Condition — one field (host/path/method/…) matched by one strategy
//	            (substring/glob/regex/exact), optionally negated.
//	Rule      — an AND of Conditions plus an action (Include or Exclude).
//	Set       — an ordered list of Rules plus a Default action.
//
//	allowlist  → Set{Default: Exclude, Rules: [Include …]}
//	denylist   → Set{Default: Include, Rules: [Exclude …]}
//	Burp-style → Set{Default: Exclude, Rules: [Exclude …, Include …]} (ordered)
//
// It imports nothing from the rest of the app: callers translate their own type
// (a capture.Flow, a CONNECT host) into a neutral Target.
package scope

import (
	"regexp"
	"strings"
)

// Field selects which part of a Target a Condition inspects.
type Field int

const (
	FieldHost Field = iota
	FieldPath
	FieldMethod
	FieldProtocol
	FieldBody
	FieldAny // all fields joined; a catch-all text search
)

var fieldNames = map[string]Field{
	"host": FieldHost, "path": FieldPath, "method": FieldMethod,
	"proto": FieldProtocol, "protocol": FieldProtocol,
	"body": FieldBody, "any": FieldAny,
}

// Strategy selects how a Condition's pattern is matched against a field.
type Strategy int

const (
	Substr Strategy = iota // pattern is a substring of the field
	Glob                   // shell-style * and ? , anchored to the whole field
	Regex                  // RE2 regular expression
	Exact                  // full-string equality
)

// Target is the neutral view a Set matches against.
type Target struct {
	Host     string
	Method   string
	Path     string
	Protocol string
	Body     []byte
}

func (t Target) field(f Field) string {
	switch f {
	case FieldHost:
		return t.Host
	case FieldPath:
		return t.Path
	case FieldMethod:
		return t.Method
	case FieldProtocol:
		return t.Protocol
	case FieldBody:
		return string(t.Body)
	case FieldAny:
		return strings.Join([]string{t.Host, t.Method, t.Path, t.Protocol, string(t.Body)}, " ")
	}
	return ""
}

// Condition matches one field with one strategy.
type Condition struct {
	Field    Field
	Strategy Strategy
	Pattern  string
	Negate   bool

	re *regexp.Regexp // compiled for Glob/Regex
}

// Compile prepares Glob/Regex conditions. Substr/Exact need no compilation.
func (c *Condition) Compile() error {
	switch c.Strategy {
	case Regex:
		re, err := regexp.Compile(c.Pattern)
		if err != nil {
			return err
		}
		c.re = re
	case Glob:
		re, err := regexp.Compile(globToRegexp(c.Pattern))
		if err != nil {
			return err
		}
		c.re = re
	}
	return nil
}

// Match reports whether t satisfies this condition (honoring Negate).
func (c *Condition) Match(t Target) bool {
	v := t.field(c.Field)
	var m bool
	switch c.Strategy {
	case Substr:
		m = strings.Contains(v, c.Pattern)
	case Exact:
		m = v == c.Pattern
	case Glob, Regex:
		m = c.re != nil && c.re.MatchString(v)
	}
	if c.Negate {
		return !m
	}
	return m
}

// Action is the verdict a Rule (or a Set's default) contributes.
type Action int

const (
	Include Action = iota // in scope (intercept / MITM)
	Exclude               // out of scope (pass through untouched)
)

func (a Action) String() string {
	if a == Include {
		return "include"
	}
	return "exclude"
}

// Rule is an AND of Conditions with an action. A Rule with no Conditions is a
// catch-all that matches every Target — useful as an explicit default rule.
type Rule struct {
	Name       string
	Action     Action
	Enabled    bool
	Conditions []Condition
}

// Match reports whether every condition holds (and the rule is enabled).
func (r *Rule) Match(t Target) bool {
	if !r.Enabled {
		return false
	}
	for i := range r.Conditions {
		if !r.Conditions[i].Match(t) {
			return false
		}
	}
	return true // zero conditions ⇒ matches everything
}

// Set is an ordered rule list with a default verdict.
type Set struct {
	Rules   []*Rule
	Default Action
	// LastMatchWins evaluates all rules and takes the last match; the default
	// (false) is first-match-wins, which is cheaper and more predictable.
	LastMatchWins bool
}

// Eval returns the winning Action for t.
func (s *Set) Eval(t Target) Action {
	action := s.Default
	for _, r := range s.Rules {
		if r.Match(t) {
			action = r.Action
			if !s.LastMatchWins {
				return action
			}
		}
	}
	return action
}

// InScope is Eval(t) == Include.
func (s *Set) InScope(t Target) bool { return s.Eval(t) == Include }

// Compile prepares every rule's conditions; call once after building a Set.
func (s *Set) Compile() error {
	for _, r := range s.Rules {
		for i := range r.Conditions {
			if err := r.Conditions[i].Compile(); err != nil {
				return err
			}
		}
	}
	return nil
}

// globToRegexp converts shell-style globs to an anchored RE2 pattern. Unlike
// filepath.Match, '*' also spans '/', which is what users expect for hosts and
// path prefixes here.
func globToRegexp(g string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range g {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return b.String()
}
