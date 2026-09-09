// Package publish is the publish request path: mapping, merge, derivation and
// the write.
package publish

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// jsonLDKeyword reports whether a key is JSON-LD's rather than the publisher's.
// `@context` and `@type` are filter columns (C4), so the columns are the only
// place they are searchable. The `@` test covers the whole family.
func jsonLDKeyword(key string) bool {
	return strings.HasPrefix(key, "@")
}

// deriveSearchText is the one source of truth for what is searchable about a
// resource: its name, the text of its descriptor, and the VALUES in its
// attributes. Keys are stripped because they are a vocabulary, not content.
//
// Task 13 of implementation-plan.md carries the rest: why the output is not
// stored, and why a change to what this emits lands with a reindex. It must stay
// deterministic — the result is hashed into `embedding_source_hash`, which is the
// A5 re-embed decision.
func deriveSearchText(resource domain.Resource) string {
	var words []string

	if resource.Name != "" {
		words = append(words, resource.Name)
	}
	words = appendValues(words, resource.Descriptor())
	words = appendValues(words, resource.ResourceAttributes())

	return strings.Join(words, " ")
}

// appendValues walks one JSON document and appends every string value it holds.
//
// Unreadable bytes contribute nothing and are not an error: L1 validates the
// request, not the merge result, and failing here would leave the resource with
// an empty tsvector rather than one field short of a full one.
func appendValues(into []string, document json.RawMessage) []string {
	if len(document) == 0 {
		return into
	}

	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		return into
	}
	return appendLeaves(into, value)
}

// appendLeaves collects the string leaves of a decoded document.
//
// Objects are walked in SORTED key order. Go randomises map iteration per run,
// so an unsorted walk would pass every single-call test and churn
// embedding_source_hash on every republish in production. Only strings are
// collected — a number or a boolean carries no term a person searches for.
func appendLeaves(into []string, value any) []string {
	switch typed := value.(type) {
	case string:
		return append(into, typed)
	case []any:
		for _, element := range typed {
			into = appendLeaves(into, element)
		}
	case map[string]any:
		for _, key := range sortedKeys(typed) {
			if jsonLDKeyword(key) {
				continue
			}
			into = appendLeaves(into, typed[key])
		}
	}
	return into
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
