package domain

import "strings"

// keySeparator is NUL, the only separator that is safe here: ids are
// publisher-supplied text and any printable separator appears in real ones.
// PostgreSQL rejects NUL inside a TEXT value, so no stored id can contain it.
const keySeparator = "\x00"

// ResourceKey flattens the pair — catalog id and resource id — that identifies
// one resource.
//
// The retrieval ports carry flat ids because ranking and RRF are set operations
// over an opaque identity. The pair is only needed again at hydration.
func ResourceKey(catalogID, resourceID string) string {
	return catalogID + keySeparator + resourceID
}

// SplitResourceKey reads the pair back. ok is false for anything ResourceKey
// did not produce, which is reported rather than guessed at.
func SplitResourceKey(key string) (catalogID, resourceID string, ok bool) {
	catalogID, resourceID, ok = strings.Cut(key, keySeparator)
	if !ok || strings.Contains(resourceID, keySeparator) {
		return "", "", false
	}
	return catalogID, resourceID, true
}

// SplitResourceKeys flattens a page into the two parallel arrays every
// page-keyed query takes — parallel because PostgreSQL has no ragged array
// type. Keys that do not split are dropped.
func SplitResourceKeys(keys []string) (catalogIDs, resourceIDs []string) {
	catalogIDs = make([]string, 0, len(keys))
	resourceIDs = make([]string, 0, len(keys))
	for _, key := range keys {
		catalogID, resourceID, ok := SplitResourceKey(key)
		if !ok {
			continue
		}
		catalogIDs = append(catalogIDs, catalogID)
		resourceIDs = append(resourceIDs, resourceID)
	}
	return catalogIDs, resourceIDs
}
