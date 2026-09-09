package domain

import "strings"

// A resource is identified by a PAIR — its catalog and its own id — and the
// retrieval ports carry a flat []string. These two functions are the one place
// the pair is flattened and the one place it is read back.
//
// The retrieval ports are flat because a Retriever ranks and RRF fuses: both
// are set operations over an opaque identity, and giving them a struct would
// make every map key in the fusion a two-field composite for no gain. The pair
// is only needed again at hydration, where it becomes the two parallel arrays
// the offer join is built from.

// keySeparator is NUL, and it is the only separator that is safe here.
//
// Catalog and resource ids are publisher-supplied text. Any printable
// separator — ':', '/', '|' — appears in real ids, and an id containing the
// separator would split into the wrong pair and hydrate a DIFFERENT resource,
// silently. PostgreSQL rejects NUL inside a TEXT value outright, so no stored
// id can contain one; the same property makes it unusable in JSON, which is
// where these ids arrive from.
const keySeparator = "\x00"

// ResourceKey flattens the pair that identifies one resource.
func ResourceKey(catalogID, resourceID string) string {
	return catalogID + keySeparator + resourceID
}

// SplitResourceKey reads the pair back.
//
// The second return is false for anything ResourceKey did not produce. A
// malformed key is a bug in a retriever rather than bad input, but hydrating
// (key, "") for it would answer with a resource nobody asked for, so it is
// reported rather than guessed at.
func SplitResourceKey(key string) (catalogID, resourceID string, ok bool) {
	catalogID, resourceID, ok = strings.Cut(key, keySeparator)
	if !ok || strings.Contains(resourceID, keySeparator) {
		return "", "", false
	}
	return catalogID, resourceID, true
}

// SplitResourceKeys flattens a page into the two parallel arrays every
// page-keyed query takes.
//
// Parallel 1-D arrays rather than one array of pairs, because PostgreSQL has no
// ragged array type — the same constraint that keeps `unnest` out of the
// publish batch. Keys that do not split are dropped rather than guessed at.
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
