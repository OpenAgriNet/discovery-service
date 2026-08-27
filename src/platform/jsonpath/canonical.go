// Package jsonpath reduces the subset of JSONPath this service reads to one
// spelling.
//
// It exists for a single reason: geometry lookup is a byte comparison. The
// publish walker stores the path it found a geometry at in
// resource_geometries.target_path, and the discover mapper turns intent.targets
// into the array on the right of "target_path = ANY($1)". Nothing normalises
// between them at the database, so a caller writing $.catalogs[*].geo and a
// caller writing $['catalogs'][*]['geo'] must arrive at the same bytes here or
// one of them silently matches nothing.
//
// The subset is deliberately narrow. Everything JSONPath can express beyond
// naming a fixed location — recursive descent, filters, slices, unions — is
// refused rather than approximated, because an expression this service reads
// wrongly widens a spatial query instead of failing it.
package jsonpath

import "strings"

// Canonicalise rewrites a JSONPath into the single bracket-quoted spelling this
// service stores and compares, or returns the empty string if it cannot read the
// path.
//
// The empty return is the refusal, and every caller must treat it as one: the
// intent mapper answers 400, and the publish walker never produces a path it
// cannot read. Returning the input unchanged instead would put an unmatchable
// string in the ANY() array and turn a bad request into an empty result.
func Canonicalise(path string) string {
	rest := strings.TrimSpace(path)
	if !strings.HasPrefix(rest, "$") {
		return ""
	}
	rest = rest[1:]

	var out strings.Builder
	out.WriteByte('$')

	for rest != "" {
		var segment string
		var ok bool

		switch rest[0] {
		case '.':
			segment, rest, ok = dotSegment(rest)
		case '[':
			segment, rest, ok = bracketSegment(rest)
		default:
			return ""
		}
		if !ok {
			return ""
		}
		out.WriteString(segment)
	}

	return out.String()
}

// dotSegment reads one .name or .* segment and returns it in bracket form.
//
// A second dot is recursive descent, which names an unbounded set of locations
// rather than one, so it is refused here rather than flattened into something
// that looks like a single path.
func dotSegment(rest string) (segment, remainder string, ok bool) {
	if len(rest) > 1 && rest[1] == '.' {
		return "", "", false
	}

	end := 1
	for end < len(rest) && rest[end] != '.' && rest[end] != '[' {
		end++
	}
	name := rest[1:end]

	if name == "*" {
		return "[*]", rest[end:], true
	}
	if !isMemberName(name) {
		return "", "", false
	}
	return "['" + name + "']", rest[end:], true
}

// bracketSegment reads one [*], ['name'], ["name"] or [0] segment.
//
// Concrete indices are kept as written. They are what makes source_path unique
// among several geometries found under one wildcard, so folding them here would
// collide every location in an array onto its first element.
func bracketSegment(rest string) (segment, remainder string, ok bool) {
	if strings.HasPrefix(rest, "[*]") {
		return "[*]", rest[3:], true
	}
	if len(rest) > 1 && (rest[1] == '\'' || rest[1] == '"') {
		return quotedSegment(rest)
	}

	end := 1
	for end < len(rest) && rest[end] != ']' {
		end++
	}
	if end == len(rest) {
		return "", "", false
	}
	digits := rest[1:end]
	if !isIndex(digits) {
		return "", "", false
	}
	return "[" + digits + "]", rest[end+1:], true
}

// quotedSegment reads ['name'] or ["name"], normalising both to single quotes.
//
// A backslash inside the name is refused rather than unescaped: this subset has
// no escape grammar, and a half-understood one would let two different names
// canonicalise to the same bytes.
func quotedSegment(rest string) (segment, remainder string, ok bool) {
	quote := rest[1]

	end := strings.IndexByte(rest[2:], quote)
	if end < 0 {
		return "", "", false
	}
	end += 2

	name := rest[2:end]
	if end+1 >= len(rest) || rest[end+1] != ']' {
		return "", "", false
	}
	if !isMemberName(name) || strings.ContainsRune(name, '\\') {
		return "", "", false
	}
	return "['" + name + "']", rest[end+2:], true
}

// isMemberName reports whether name is one this subset will carry.
//
// The '@' is there for JSON-LD: "@context" and "@type" are ordinary members of a
// published catalog, and a name check that rejected them would make the
// attributes block unaddressable.
func isMemberName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isMemberByte(name[i]) {
			return false
		}
	}
	return true
}

// isMemberByte reports whether c may appear in a member name.
//
// Split out of isMemberName so that rootMember, which has to find where a name
// ENDS rather than check one it already has, reads the same character class.
// Two copies of this class would be two chances for a name to be addressable
// in one file and not the other.
func isMemberByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '_', c == '-', c == '@':
		return true
	}
	return false
}

// isIndex reports whether digits is a non-negative integer in its only spelling.
//
// "01" is refused because it addresses the same element as "0" while comparing
// unequal to it, which is exactly the kind of near-miss this package exists to
// keep out of the database.
func isIndex(digits string) bool {
	if digits == "" {
		return false
	}
	if len(digits) > 1 && digits[0] == '0' {
		return false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}
	return true
}
