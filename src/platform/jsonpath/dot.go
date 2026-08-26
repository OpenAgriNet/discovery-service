package jsonpath

import "strings"

// Dot renders a canonical bracket path in the dot form the wire uses.
//
// It is the inverse rendering of the grammar Canonicalise parses, and it exists
// because the two audiences want different spellings of the same path. A stored
// target_path is compared byte-for-byte against a caller's `targets`, so it must
// be canonical; an `error.details.path` is read by a human looking for the value
// they sent, and C7's own example is dot form — `$.message.publishDirectives[1]`.
//
// Only a member name that is a bare identifier is unbracketed. `@type` and
// `resource-id` stay in brackets, because `$.a.@type` is not a path any JSONPath
// implementation would take back — a renderer that produced it would hand the
// publisher a string their own tooling refuses.
//
// Returns the empty string for a path it cannot read, exactly as Canonicalise
// does, so a caller has one refusal to handle rather than two.
func Dot(path string) string {
	rest := strings.TrimSpace(path)
	if !strings.HasPrefix(rest, "$") {
		return ""
	}
	rest = rest[1:]

	var out strings.Builder
	out.WriteByte('$')

	for rest != "" {
		segment, remainder, ok := dotRender(rest)
		if !ok {
			return ""
		}
		out.WriteString(segment)
		rest = remainder
	}
	return out.String()
}

// dotRender consumes one canonical bracket segment and renders it.
func dotRender(rest string) (segment, remainder string, ok bool) {
	if len(rest) < 3 || rest[0] != '[' {
		return "", "", false
	}

	if rest[1] == '\'' {
		return dotMember(rest)
	}

	end := strings.IndexByte(rest, ']')
	if end < 0 {
		return "", "", false
	}
	inner := rest[1:end]
	if inner != "*" && !isIndex(inner) {
		return "", "", false
	}
	return rest[:end+1], rest[end+1:], true
}

// dotMember renders a quoted member. The closing quote is found before the
// closing bracket, not the other way round: Canonicalise admits `-` and `@` in a
// name, and scanning to the first `]` would mis-split a name that contained one.
func dotMember(rest string) (segment, remainder string, ok bool) {
	end := strings.IndexByte(rest[2:], '\'')
	if end < 0 {
		return "", "", false
	}
	end += 2

	if end+1 >= len(rest) || rest[end+1] != ']' {
		return "", "", false
	}
	name := rest[2:end]
	if !isMemberName(name) {
		return "", "", false
	}

	if isIdentifier(name) {
		return "." + name, rest[end+2:], true
	}
	return rest[:end+2], rest[end+2:], true
}

// isIdentifier reports whether a member name can be written after a dot.
//
// Narrower than isMemberName on purpose: that one describes what this service
// will READ, and this one describes what it is willing to EMIT. A leading digit,
// a `-` or a `@` is legal in a bracketed name and unreadable in a dotted one.
func isIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
