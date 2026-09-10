package jsonpath

import (
	"fmt"
	"strings"
)

// Accept reports whether an attribute-filter expression may be handed to
// PostgreSQL, or returns the reason it may not.
//
// A gate, not a rewriter. Since A18 the filter runs against one column —
// `resources.filter_doc`, a composite already rooted at `$.catalogs` — so an
// accepted expression is cast with `@filter::jsonpath` and evaluated VERBATIM.
// Nothing is stripped, rebased or repaired: a repairer is a parser, which is the
// thing this package refuses to own.
//
// What makes the gate necessary rather than tidy is that three wrong shapes fail
// SILENTLY. `@?` takes a PATH expression, and given a PREDICATE — the same intent
// written without the `?` — it matches EVERY row and reports no error, because it
// asks only whether the expression yielded an item and a comparison always yields
// one, `false` included. A wrong root then matches nothing, and a two-root
// expression matches everything. All three are refused here so they become a 400.
//
// The error is a plain one; callers map it to SCH_INVALID_JSONPATH.
func Accept(expression string) error {
	rest := strings.TrimSpace(expression)

	// PostgreSQL's explicit path modes change how missing members and arrays
	// behave, not what the expression names, so they are carried through untouched
	// rather than being a fourth thing to validate.
	for _, mode := range []string{"strict ", "lax "} {
		if strings.HasPrefix(rest, mode) {
			rest = strings.TrimSpace(rest[len(mode):])
			break
		}
	}

	root, ok := rootMember(rest)
	if !ok || root != filterRoot {
		return fmt.Errorf("the expression must be rooted at $.%s, which is what %s holds; %q is not",
			filterRoot, filterColumn, expression)
	}

	return acceptForm(rest)
}

const (
	// filterRoot is the only member an accepted expression may name off `$`.
	filterRoot = "catalogs"

	// filterColumn is named in the refusal because "wrong root" is the least
	// guessable of the three: the caller wrote a path valid against the response
	// document that simply does not exist in the column the filter runs against.
	filterColumn = "resources.filter_doc"
)

// acceptForm walks the expression once, checking the two things a caller cannot
// learn from PostgreSQL's own answer: that a `?` filter is reached before any
// comparison, and that there is only one root.
//
// Bracket matching over bytes, deliberately not a parse. It has to know where
// strings and nesting are — a `?` in a name and a `&&` between two roots are the
// same bytes as the ones that matter — and nothing else. The cast is still the
// last word on syntax; this walk only moves the three silent failures in front
// of it.
func acceptForm(expression string) error {
	walk := scan{filterAt: -1, compare: -1}

	for i := 0; i < len(expression); i++ {
		next, err := walk.step(expression, i)
		if err != nil {
			return err
		}
		i = next
	}

	return walk.done()
}

// scan is where the walk has got to: how deep it is, and where it first saw the
// two things whose ORDER is the whole question.
//
// filterAt and compare start at -1, because byte 0 is a position an expression can
// genuinely have one at and a zero value would read as "seen at the start" — the
// state that decides the last check in done.
type scan struct {
	parens   int
	brackets int
	filterAt int
	compare  int
}

// step reads the byte at i and returns the index to continue from, which is
// past the byte only when a string was consumed whole.
func (s *scan) step(expression string, i int) (int, error) {
	switch c := expression[i]; c {
	case '"', '\'':
		end := closingQuote(expression, i)
		if end < 0 {
			return 0, fmt.Errorf("the string opened at byte %d is never closed", i)
		}
		return end, nil

	case '(':
		s.parens++
	case ')':
		if s.parens--; s.parens < 0 {
			return 0, fmt.Errorf("the %q at byte %d closes nothing", c, i)
		}
	case '[':
		s.brackets++
	case ']':
		if s.brackets--; s.brackets < 0 {
			return 0, fmt.Errorf("the %q at byte %d closes nothing", c, i)
		}

	case '?':
		return i, s.filter(expression, i)
	case '&', '|':
		return s.conjunction(expression, i)

	case '=', '<', '>':
		// Covers ==, !=, <>, <= and >= by their common byte. Only the FIRST
		// one matters, and only relative to the first `?`.
		if s.compare < 0 {
			s.compare = i
		}
	}
	return i, nil
}

// filter records a `? (...)` and refuses the RFC 9535 spelling of one.
//
// `[?(@.x == "y")]` is what every JSONPath tutorial teaches and what PostgreSQL
// does not speak (C10). The dialects differ by one bracket, and left to PostgreSQL
// the subscript form parses, selects nothing, and `@?` reports no error — so it is
// named here instead.
func (s *scan) filter(expression string, i int) error {
	if s.brackets > 0 {
		return fmt.Errorf("the filter at byte %d is inside a subscript: that is RFC 9535, and this service runs PostgreSQL SQL/JSON path, where a filter is written ? (...) after the subscript", i)
	}
	if next := skipSpace(expression, i+1); next >= len(expression) || expression[next] != '(' {
		return fmt.Errorf("the filter at byte %d has no parenthesised predicate", i)
	}
	if s.filterAt < 0 {
		s.filterAt = i
	}
	return nil
}

// conjunction refuses a second root: `$.catalogs[*] ? (...) && $.other == 1`.
//
// Legal jsonpath, and it parses as a PREDICATE, so under `@?` it behaves like the
// missing-`?` case and returns the corpus. Inside parentheses the same operator is
// the ordinary conjunction a predicate is built from, so depth is the distinction.
func (s *scan) conjunction(expression string, i int) (int, error) {
	if i+1 >= len(expression) || expression[i+1] != expression[i] {
		return i, nil
	}
	if s.parens == 0 {
		return 0, fmt.Errorf("%q at byte %d joins two roots: an expression may name one root, and `@?` answers true for every row of a two-root expression", expression[i:i+2], i)
	}
	return i + 1, nil
}

// done is what the walk knows only once it has finished.
func (s *scan) done() error {
	if s.parens > 0 {
		return fmt.Errorf("%d unclosed %q", s.parens, "(")
	}
	if s.brackets > 0 {
		return fmt.Errorf("%d unclosed %q", s.brackets, "[")
	}

	if s.filterAt < 0 {
		return fmt.Errorf("the expression has no ? (...) filter, so it selects rather than tests: `@?` answers true for every row it is given one of these, and the caller receives the whole corpus looking like a filtered page")
	}
	if s.compare >= 0 && s.compare < s.filterAt {
		return fmt.Errorf("the comparison at byte %d comes before the filter at byte %d: the predicate belongs inside ? (...), and outside it `@?` answers true for every row", s.compare, s.filterAt)
	}
	return nil
}

// rootMember reads the one member an expression names off `$`.
//
// It stops at the first byte that cannot be part of a name, which is what reads
// `$.catalogs ? (...)` — legal, and what lax-mode auto-unwrapping invites — the
// same way as `$.catalogs[*] ? (...)`.
func rootMember(expression string) (string, bool) {
	if !strings.HasPrefix(expression, "$") {
		return "", false
	}
	rest := expression[1:]

	switch {
	case strings.HasPrefix(rest, ".."):
		// Recursive descent names an unbounded set of locations, and under a
		// composite holding every level of the document at once it is the one
		// construct that could make a resource match on its catalog's others.
		return "", false

	case strings.HasPrefix(rest, "."):
		end := 1
		for end < len(rest) && isMemberByte(rest[end]) {
			end++
		}
		name := rest[1:end]
		return name, isMemberName(name)

	case strings.HasPrefix(rest, "["):
		segment, _, ok := bracketSegment(rest)
		if !ok || !strings.HasPrefix(segment, "['") {
			return "", false
		}
		// bracketSegment normalises to ['name'], so the name is what sits
		// between the quotes.
		return segment[2 : len(segment)-2], true
	}

	return "", false
}

// closingQuote returns the index of the quote closing the one at open, or -1.
//
// A backslash escapes the next byte, so `"a \" b"` closes at the second quote and
// not the first. Single quotes are tracked alongside double ones so that one left
// unterminated is a refusal here rather than a scan that runs off and mis-reads
// what follows.
func closingQuote(s string, open int) int {
	for i := open + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case s[open]:
			return i
		}
	}
	return -1
}

// skipSpace returns the index of the first non-space byte at or after i.
func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// HasIndexableEquality reports whether GIN can extract anything from an
// accepted expression.
//
// `jsonb_path_ops` extracts clauses of the form accessor-chain `==` constant and
// nothing else. Inequality, `like_regex` and `starts with` are answered correctly
// by reading every gated row — fine beside a text or spatial predicate that has
// already narrowed the corpus, a full scan of the catalogue when alone. MapIntent
// uses this to tell the two cases apart.
//
// It OVER-approximates deliberately: `@.a == "x" || @.b > 1` answers true here and
// PostgreSQL will in fact scan it, since a disjunction is index-servable only when
// EVERY arm is. Over-approximating costs one slow query; under-approximating would
// cost a 400 on a legitimate filter.
func HasIndexableEquality(expression string) bool {
	for i := 0; i < len(expression); i++ {
		switch expression[i] {
		case '"', '\'':
			end := closingQuote(expression, i)
			if end < 0 {
				// Unterminated: Accept has already refused this, and reading
				// on would find comparisons inside what is really a string.
				return false
			}
			i = end
		case '=':
			// `==` and only `==`: `!=`, `<=` and `>=` each carry one `=`, so the
			// pair is what separates the extractable clause from the three that scan.
			if i+1 < len(expression) && expression[i+1] == '=' {
				return true
			}
		}
	}
	return false
}
