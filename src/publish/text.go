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
//
// `@context` and `@type` are filter COLUMNS under C4, matched exactly against a
// request's schemaContext. A term that is both a filter and a free-text token
// can be matched two ways that disagree, so the keywords are stripped here and
// the columns are the only place they are searchable. The `@` test covers the
// whole family — `@id`, `@graph`, `@value` — rather than the two names, because
// a keyword nobody listed is still not the publisher's prose.
func jsonLDKeyword(key string) bool {
	return strings.HasPrefix(key, "@")
}

// deriveSearchText is the one source of truth for what is searchable about a
// resource: its name, the text of its descriptor, and the VALUES in its
// attributes.
//
// Keys are stripped because they are a vocabulary, not content. Every resource
// of a type carries the same ones, so indexing "moisture" makes that word match
// every grain listing in the corpus instead of the ones that say something
// about moisture — a term with no discriminating power, in the index that ranks
// by exactly that.
//
// Its OUTPUT is deliberately not stored. `search_tsv` is built from it at
// insert and the Phase 2 embedding backfill calls it again over `name` and
// `document` rather than reading a stale copy — which is
// only sound because it is deterministic. It must therefore stay so: the result
// is hashed into `embedding_source_hash`, and that hash is the A5 re-embed
// decision, so a derivation that varied would re-embed a catalog that changed
// nothing.
//
// A change to what this function emits changes what matches. It lands with a
// reindex, never on its own.
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
// Unreadable bytes contribute nothing and are not an error. Nothing upstream
// guarantees a merged document parses — L1 validates the request, not the merge
// result — and failing here would leave the resource with an empty tsvector,
// undiscoverable by any lexical query, rather than merely under-indexed by one
// field.
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

// appendLeaves collects the string leaves of a decoded document, in a fixed
// order.
//
// Objects are walked in SORTED key order. Two byte-different spellings of the
// same document must derive identically — Go randomises map iteration per run,
// so an unsorted walk would pass every single-call test and churn
// embedding_source_hash on every republish in production.
//
// Only strings are collected. A number or a boolean carries no term a person
// searches for: "12" and "true" match nothing useful and would sit in the
// tsvector diluting the terms that do.
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
