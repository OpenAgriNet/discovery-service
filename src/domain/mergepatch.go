package domain

import (
	"encoding/json"
	"slices"
	"time"
)

// MergePatch applies an RFC 7396 JSON Merge Patch and returns the result.
//
// This is the document half of A8's two-level rule: an absent key keeps its
// stored value, an explicit null deletes it, and an array is replaced wholesale
// rather than element-merged. The collection half — matching `resources` and
// `offers` by id instead of by array position — is MergeCatalog's job, and it
// exists precisely because this rule would otherwise discard a publisher's
// whole catalog on a one-resource patch.
//
// A pure function on two values: no context, no clock, no storage. That is what
// lets the exhaustive table in mergepatch_test.go be a unit test rather than a
// database fixture.
//
// Neither input is modified. Both are decoded into fresh values and every
// object the merge touches is rebuilt, because the target here is a document
// the audit trail holds and a concurrent reader may still be looking at.
//
// A patch that is not readable JSON changes nothing. A target that is not
// readable JSON reads as "not an object", which is the RFC's own branch for a
// scalar: the patch replaces it. Neither is reachable from a validated request
// — both sides have been through L1 or came out of the store — so failing here
// would report a corruption this function cannot describe better than the
// caller can.
func MergePatch(target, patch json.RawMessage) json.RawMessage {
	var patchValue any
	if err := json.Unmarshal(patch, &patchValue); err != nil {
		return target
	}

	var targetValue any
	if err := json.Unmarshal(target, &targetValue); err != nil {
		targetValue = nil
	}

	merged, err := json.Marshal(mergeValue(targetValue, patchValue))
	if err != nil {
		return target
	}
	return merged
}

// mergeValue is the RFC's algorithm over decoded values.
//
// A patch that is not an object replaces the target outright — which is how
// arrays, scalars and an explicit null all get their behaviour without a branch
// of their own.
func mergeValue(target, patch any) any {
	patchObject, isObject := patch.(map[string]any)
	if !isObject {
		return patch
	}

	targetObject, isObject := target.(map[string]any)
	if !isObject {
		targetObject = map[string]any{}
	}

	merged := make(map[string]any, len(targetObject)+len(patchObject))
	for name, value := range targetObject {
		merged[name] = value
	}

	for name, value := range patchObject {
		if value == nil {
			// Absent from the target too: RFC 7396 makes this a no-op rather
			// than an insert of null, and delete on a missing key already is
			// one.
			delete(merged, name)
			continue
		}
		merged[name] = mergeValue(merged[name], value)
	}
	return merged
}

// MergeCatalog applies a patch to a stored catalog and reports which resources
// have to be re-derived.
//
// This is the collection half of A8's two-level rule: MergePatch for the
// documents, identity-keyed merge for Resources and Offers. Pure on values —
// no context, no storage, no clock — which is what makes the merge table a
// unit test rather than a database fixture.
//
// `touched` is every resource the patch named, PLUS, for every offer the patch
// named, the UNION of that offer's ResourceIDs before and after the merge. The
// union rather than the merged ids alone is what makes a RELOCATION correct:
// an offer's geometry is written in its resources' loop iteration, so an offer
// moving from r1 to r2 must still visit r1 to delete the row that geometry left
// behind. The stored ids are the only record of where the geometry currently
// is, and deleting a row requires visiting its owner.
//
// The returned ids are sorted and deduplicated: the repository iterates them to
// decide what to re-embed, and a duplicate is a resource embedded twice.
func MergeCatalog(stored Catalog, patch CatalogPatch) (Catalog, []string) {
	merged := stored

	// Identity comes from the PATCH, not from what was stored. On a first
	// publish `stored` is the zero Catalog, so taking these from it would store
	// a catalog with an empty id and — through the two merge calls below —
	// resources and offers whose CatalogID is "". The repository looks the
	// stored catalog up BY patch.ID, so the two can never legitimately differ.
	merged.ID = patch.ID

	// NetworkID is not stored (nothing reads it back). It is carried here
	// because EnsureVisibleTo is the next thing that runs and it is the only
	// reader — a merge result that dropped it would default an empty audience
	// to [""], a network nobody is on, rather than to the publisher's own.
	merged.NetworkID = patch.NetworkID

	merged.Document = patchDocument(stored.Document, patch.Document)

	// Unconditional, both of them (A9). By here the mapper has already resolved
	// the declared default, so silence means "sent with its default" rather
	// than "keep what is stored" — the half of A9 scenario 26 exists to pin.
	merged.Active = patch.Active
	merged.VisibleTo = patch.VisibleTo

	// Unconditional for the same reason, and it is the reason the column has no
	// merge semantics at all: a catalog reports the version of the request that
	// last wrote it, not of the request that first created it. The mapper has
	// already resolved an absent context.version to this build's own, so
	// carrying `stored` forward here would make a republish claim a version its
	// own envelope did not declare.
	merged.ProtocolVersion = patch.ProtocolVersion

	validity := window{stored.ValidFrom, stored.ValidTo, stored.ValidTimeFrom, stored.ValidTimeTo}.patched(patch.Validity)
	merged.ValidFrom, merged.ValidTo = validity.From, validity.To
	merged.ValidTimeFrom, merged.ValidTimeTo = validity.TimeFrom, validity.TimeTo

	resources, touchedByResources := mergeResources(merged.ID, stored.Resources, patch.Resources)
	offers, touchedByOffers := mergeOffers(merged.ID, stored.Offers, patch.Offers)

	merged.Resources, merged.Offers = resources, offers
	return merged, uniqueSorted(append(touchedByResources, touchedByOffers...))
}

// patchDocument applies a document patch, treating a nil patch as absent.
//
// An explicit JSON null needs no branch of its own: RFC 7396 replaces the
// target with any non-object patch, so MergePatch already returns null for it.
func patchDocument(stored, patch json.RawMessage) json.RawMessage {
	if patch == nil {
		return stored
	}
	return MergePatch(stored, patch)
}

// window is the validity quartet Catalog and Offer both carry, gathered so the
// tri-state rule is written once rather than once per owner. The fields stay
// flat on those types because the plan names them there and the storage layer
// maps them one-to-one onto four columns.
type window struct {
	From     time.Time
	To       time.Time
	TimeFrom *TimeOfDay
	TimeTo   *TimeOfDay
}

// patched resolves all four columns independently. A nil patch is an absent
// `validity` and keeps every one of them.
func (w window) patched(patch *TimePeriodPatch) window {
	if patch == nil {
		return w
	}
	return window{
		From:     patchedDate(w.From, patch.StartDate),
		To:       patchedDate(w.To, patch.EndDate),
		TimeFrom: patchedTimeOfDay(w.TimeFrom, patch.StartTime),
		TimeTo:   patchedTimeOfDay(w.TimeTo, patch.EndTime),
	}
}

// patchedDate reads the tri-state onto a calendar bound, where cleared is the
// zero time — the same value an unset column reads back as.
func patchedDate(stored time.Time, patch Nullable[time.Time]) time.Time {
	if !patch.Set {
		return stored
	}
	if patch.Null {
		return time.Time{}
	}
	return patch.Value
}

// patchedTimeOfDay reads the tri-state onto a daily bound, where cleared is nil
// — because 00:00:00 is a real bound and cannot double as the absence.
func patchedTimeOfDay(stored *TimeOfDay, patch Nullable[TimeOfDay]) *TimeOfDay {
	if !patch.Set {
		return stored
	}
	if patch.Null {
		return nil
	}
	value := patch.Value
	return &value
}

// mergeResources merges by id (A8), never by array position. A patch naming an
// id nothing stores is an insert; there is no delete, because under MERGE a
// null deletes a key and never a row.
func mergeResources(catalogID string, stored []Resource, patches []ResourcePatch) ([]Resource, []string) {
	merged := slices.Clone(stored)
	at := indexByID(merged, func(resource Resource) string { return resource.ID })

	touched := make([]string, 0, len(patches))
	for _, patch := range patches {
		touched = append(touched, patch.ID)

		position, held := at[patch.ID]
		if !held {
			// Record the insert's position before appending: a payload may name
			// the same NEW id twice, and "resources match by id" (A8) cannot
			// mean two rows under one id. Postgres would refuse the second on
			// its unique index, so a merge that produced it would put the two
			// backends into disagreement — the one thing this function exists
			// on both sides of.
			at[patch.ID] = len(merged)
			merged = append(merged, Resource{
				ID:        patch.ID,
				CatalogID: catalogID,
				Document:  patchDocument(nil, patch.Document),
			})
			continue
		}
		merged[position].Document = patchDocument(merged[position].Document, patch.Document)
	}
	return merged, touched
}

// mergeOffers merges by id and reports the resources each patched offer touches
// — the union of its ids before and after, which is what a relocation needs.
func mergeOffers(catalogID string, stored []Offer, patches []OfferPatch) ([]Offer, []string) {
	merged := slices.Clone(stored)
	at := indexByID(merged, func(offer Offer) string { return offer.ID })

	touched := make([]string, 0, len(patches))
	for _, patch := range patches {
		touched = append(touched, patch.ResourceIDs...)

		position, held := at[patch.ID]
		if !held {
			at[patch.ID] = len(merged)
			merged = append(merged, patchedOffer(Offer{ID: patch.ID, CatalogID: catalogID}, patch))
			continue
		}
		touched = append(touched, merged[position].ResourceIDs...)
		merged[position] = patchedOffer(merged[position], patch)
	}
	return merged, touched
}

// patchedOffer applies one offer patch. ResourceIDs is assigned rather than
// merged: it has a declared default of [] (A9), so the mapper has already
// resolved it and there is no absence left to represent.
func patchedOffer(offer Offer, patch OfferPatch) Offer {
	offer.Document = patchDocument(offer.Document, patch.Document)
	offer.ResourceIDs = patch.ResourceIDs

	validity := window{offer.ValidFrom, offer.ValidTo, offer.ValidTimeFrom, offer.ValidTimeTo}.patched(patch.Validity)
	offer.ValidFrom, offer.ValidTo = validity.From, validity.To
	offer.ValidTimeFrom, offer.ValidTimeTo = validity.TimeFrom, validity.TimeTo
	return offer
}

// indexByID maps each element's id to its position, so a merge is one pass over
// the patches rather than a scan of the stored slice per patch.
func indexByID[T any](items []T, id func(T) string) map[string]int {
	at := make(map[string]int, len(items))
	for position, item := range items {
		at[id(item)] = position
	}
	return at
}

// uniqueSorted is what `touched` is reported as. Sorted for a stable iteration
// order in the repository, compacted because a duplicate id is a resource
// re-embedded twice.
func uniqueSorted(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// TouchedSet answers "was this resource in the patch" without a scan.
//
// `touched` crosses DeriveFunc and the repository as a SLICE, and stays one:
// two of its consumers send it whole as a PostgreSQL array — the geometry clear
// and PropagateGate — and both depend on it being a list. Every other consumer
// asks only for membership, once per resource in the merged catalog, which
// against a slice is quadratic in the size of a catalog. A full-mode publish
// touches every resource, so a 500-resource catalog costs ~250k string
// comparisons per pass, and derive, coverGeometries and writeResources each
// make a pass.
//
// A named type rather than a bare map so the call sites read as the question
// they are asking, and so the empty case cannot be confused with a nil catalog:
// NewTouchedSet(nil).Has(id) is false, which is the right answer for a patch
// that touched nothing.
type TouchedSet map[string]struct{}

// NewTouchedSet indexes a touched list for membership.
func NewTouchedSet(touched []string) TouchedSet {
	members := make(TouchedSet, len(touched))
	for _, resourceID := range touched {
		members[resourceID] = struct{}{}
	}
	return members
}

// Has reports whether the patch touched this resource.
func (s TouchedSet) Has(resourceID string) bool {
	_, ok := s[resourceID]
	return ok
}
