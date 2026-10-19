// Package license checks SPDX license expressions against an allow/deny policy.
package license

import (
	"fmt"
	"strings"
	"unicode"
)

// Policy lists license ids (or prefixes ending in "*") that are accepted or refused.
// With an empty Allow, everything that is not denied is accepted.
type Policy struct {
	Allow, Deny []string
}

// Empty reports whether the policy accepts everything.
func (p Policy) Empty() bool { return len(p.Allow) == 0 && len(p.Deny) == 0 }

// Verdict is the outcome of checking one license expression.
type Verdict string

const (
	OK      Verdict = "ok"
	Denied  Verdict = "denied"
	Unknown Verdict = "unknown" // empty or not a valid SPDX expression
	// NotAllowed means an allow list exists and the license is not on it.
	NotAllowed Verdict = "not allowed"
)

// Check evaluates an SPDX expression ("MIT", "Apache-2.0 OR MIT", "GPL-2.0-only WITH Classpath-exception-2.0")
// against the policy. In "A OR B" one acceptable side is enough; in "A AND B" both must be.
// An exception after WITH is ignored: the license before it decides.
func (p Policy) Check(expr string) Verdict {
	toks, err := tokenize(expr)
	if err != nil || len(toks) == 0 {
		return Unknown
	}
	ps := &parser{toks: toks, policy: p}
	v, err := ps.or()
	if err != nil || ps.pos != len(toks) {
		return Unknown
	}
	return v
}

type parser struct {
	toks   []string
	pos    int
	policy Policy
}

// or returns OK when one alternative is acceptable, otherwise the verdict of the first one.
func (s *parser) or() (Verdict, error) {
	best, err := s.and()
	if err != nil {
		return Unknown, err
	}
	for s.peekIs("OR") {
		s.pos++
		v, err := s.and()
		if err != nil {
			return Unknown, err
		}
		if v == OK {
			best = OK
		}
	}
	return best, nil
}

// and returns OK only when every part is acceptable, otherwise the first failing verdict.
func (s *parser) and() (Verdict, error) {
	res, err := s.term()
	if err != nil {
		return Unknown, err
	}
	for s.peekIs("AND") {
		s.pos++
		v, err := s.term()
		if err != nil {
			return Unknown, err
		}
		if res == OK {
			res = v
		}
	}
	return res, nil
}

func (s *parser) term() (Verdict, error) {
	if s.pos >= len(s.toks) {
		return Unknown, fmt.Errorf("unexpected end")
	}
	t := s.toks[s.pos]
	s.pos++
	switch {
	case t == "(":
		v, err := s.or()
		if err != nil {
			return Unknown, err
		}
		if s.pos >= len(s.toks) || s.toks[s.pos] != ")" {
			return Unknown, fmt.Errorf("missing )")
		}
		s.pos++
		return v, nil
	case t == ")" || isOp(t):
		return Unknown, fmt.Errorf("unexpected %q", t)
	}
	if s.peekIs("WITH") {
		s.pos++
		if s.pos >= len(s.toks) || isOp(s.toks[s.pos]) || s.toks[s.pos] == "(" || s.toks[s.pos] == ")" {
			return Unknown, fmt.Errorf("WITH needs an exception")
		}
		s.pos++
	}
	return s.policy.one(t), nil
}

func (s *parser) peekIs(op string) bool {
	return s.pos < len(s.toks) && strings.EqualFold(s.toks[s.pos], op)
}

func isOp(t string) bool {
	switch strings.ToUpper(t) {
	case "AND", "OR", "WITH":
		return true
	}
	return false
}

// one judges a single license id.
func (p Policy) one(id string) Verdict {
	if matches(p.Deny, id) {
		return Denied
	}
	if len(p.Allow) > 0 && !matches(p.Allow, id) {
		return NotAllowed
	}
	return OK
}

// matches compares ids ignoring case and the GPL-style suffixes "+", "-only" and "-or-later",
// so denying "GPL-3.0" also refuses "GPL-3.0-only", "GPL-3.0-or-later" and "GPL-3.0+".
// A pattern ending in "*" matches every id with that prefix.
func matches(list []string, id string) bool {
	for _, pat := range list {
		pat = strings.TrimSpace(pat)
		if strings.HasSuffix(pat, "*") {
			if strings.HasPrefix(strings.ToLower(id), strings.ToLower(strings.TrimSuffix(pat, "*"))) {
				return true
			}
			continue
		}
		if normalize(pat) == normalize(id) {
			return true
		}
	}
	return false
}

func normalize(id string) string {
	id = strings.ToLower(id)
	id = strings.TrimSuffix(id, "+")
	id = strings.TrimSuffix(id, "-or-later")
	id = strings.TrimSuffix(id, "-only")
	return id
}

func tokenize(expr string) ([]string, error) {
	var toks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for _, r := range expr {
		switch {
		case r == '(' || r == ')':
			flush()
			toks = append(toks, string(r))
		case unicode.IsSpace(r):
			flush()
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '.' || r == '+' || r == ':':
			cur.WriteRune(r)
		default:
			return nil, fmt.Errorf("unexpected character %q", r)
		}
	}
	flush()
	return toks, nil
}
