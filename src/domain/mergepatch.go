package domain

import (
	"encoding/json"
	"slices"
	"time"
)

// MergePatch applies an RFC 7396 JSON Merge Patch and returns the result.
//
// The document half of A8's two-level rule: absent keeps, explicit null
// deletes, arrays replace wholesale. The collection half — matching `resources`
// and `offers` by id rather than array position — is MergeCatalog.
//
// Pure, and neither input is modified: every object the merge touches is
// rebuilt, because the target is a document the audit trail holds and a
// concurrent reader may still be looking at.
//
// Unreadable JSON changes nothing on the patch side, and reads as "not an
// object" on the target side — the RFC's own branch for a scalar. Neither is
// reachable from a validated request.
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

// mergeValue is the RFC's algorithm over decoded values. A patch that is not an
// object replaces the target outright, which is how arrays, scalars and an
// explicit null all get their behaviour without a branch of their own.
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
			// Absent from the target too: RFC 7396 makes this a no-op rather than
			// an insert of null, and delete on a missing key already is one.
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
// The collection half of A8: MergePatch for the documents, identity-keyed merge
// for Resources and Offers.
//
// `touched` is every resource the patch named, plus — for every offer the patch
// named — the union of that offer's ResourceIDs before and after the merge. The
// union is what makes a RELOCATION correct: an offer moving from r1 to r2 must
// still visit r1 to delete the geometry row left behind there. Sorted and
// deduplicated, because a duplicate id is a resource embedded twice.
func MergeCatalog(stored Catalog, patch CatalogPatch) (Catalog, []string) {
	merged := stored

	// Identity comes from the PATCH: on a first publish `stored` is the zero
	// Catalog, so taking it from there would store an empty id and, through the
	// two merges below, resources whose CatalogID is "".
	merged.ID = patch.ID

	// NetworkID is not stored, but is carried here because EnsureVisibleTo runs
	// next and is its only reader — dropping it would default an empty audience
	// to [""], a network nobody is on.
	merged.NetworkID = patch.NetworkID

	merged.Document = patchDocument(stored.Document, patch.Document)

	// Unconditional, both (A9): the mapper has already resolved the declared
	// default, so silence means "sent with its default", not "keep what is
	// stored".
	merged.Active = patch.Active
	merged.VisibleTo = patch.VisibleTo

	// Unconditional too, and the reason the column has no merge semantics: a
	// catalog reports the version of the request that last wrote it.
	merged.ProtocolVersion = patch.ProtocolVersion

	validity := window{stored.ValidFrom, stored.ValidTo, stored.ValidTimeFrom, stored.ValidTimeTo}.patched(patch.Validity)
	merged.ValidFrom, merged.ValidTo = validity.From, validity.To
	merged.ValidTimeFrom, merged.ValidTimeTo = validity.TimeFrom, validity.TimeTo

	resources, touchedByResources := mergeResources(merged.ID, stored.Resources, patch.Resources)
	offers, touchedByOffers := mergeOffers(merged.ID, stored.Offers, patch.Offers)

	merged.Resources, merged.Offers = resources, offers
	return merged, uniqueSorted(append(touchedByResources, touchedByOffers...))
}

// patchDocument applies a document patch, treating a nil patch as absent. An
// explicit JSON null needs no branch: MergePatch already returns null for it.
func patchDocument(stored, patch json.RawMessage) json.RawMessage {
	if patch == nil {
		return stored
	}
	return MergePatch(stored, patch)
}

// window is the validity quartet Catalog and Offer both carry, gathered so the
// tri-state rule is written once rather than once per owner.
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
// zero time — what an unset column reads back as.
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
// — 00:00:00 is a real bound and cannot double as the absence.
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
// id nothing stores is an insert; there is no delete, because under MERGE a null
// deletes a key and never a row.
func mergeResources(catalogID string, stored []Resource, patches []ResourcePatch) ([]Resource, []string) {
	merged := slices.Clone(stored)
	at := indexByID(merged, func(resource Resource) string { return resource.ID })

	touched := make([]string, 0, len(patches))
	for _, patch := range patches {
		touched = append(touched, patch.ID)

		position, held := at[patch.ID]
		if !held {
			// Record the insert's position before appending: a payload may name
			// the same NEW id twice, and two rows under one id would put the two
			// backends into disagreement — Postgres refuses the second on its
			// unique index.
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
// merged: it has a declared default of [] (A9), already resolved by the mapper.
func patchedOffer(offer Offer, patch OfferPatch) Offer {
	offer.Document = patchDocument(offer.Document, patch.Document)
	offer.ResourceIDs = patch.ResourceIDs

	validity := window{offer.ValidFrom, offer.ValidTo, offer.ValidTimeFrom, offer.ValidTimeTo}.patched(patch.Validity)
	offer.ValidFrom, offer.ValidTo = validity.From, validity.To
	offer.ValidTimeFrom, offer.ValidTimeTo = validity.TimeFrom, validity.TimeTo
	return offer
}

// indexByID maps each element's id to its position, so a merge is one pass over
// the patches rather than a scan per patch.
func indexByID[T any](items []T, id func(T) string) map[string]int {
	at := make(map[string]int, len(items))
	for position, item := range items {
		at[id(item)] = position
	}
	return at
}

// uniqueSorted is what `touched` is reported as: sorted for a stable iteration
// order, compacted because a duplicate id is a resource re-embedded twice.
func uniqueSorted(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// TouchedSet answers "was this resource in the patch" without a scan.
//
// `touched` crosses DeriveFunc as a slice and stays one, because the geometry
// clear and PropagateGate send it whole as a PostgreSQL array. Every other
// consumer asks only for membership, once per resource — quadratic against a
// slice, and a full-mode publish touches every resource.
type TouchedSet map[string]struct{}

// NewTouchedSet indexes a touched list for membership. NewTouchedSet(nil) has
// no members, which is the right answer for a patch that touched nothing.
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
