package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// ScopeGate is the six columns every resource carries a copy of.
//
// A type rather than six loose variables because the whole hazard here is
// FORGETTING one: `Resource` has no validity of its own, so a column a
// propagate leaves out keeps yesterday's value forever and no later publish can
// correct it. Six fields on a struct that both backends read and write is the
// difference between omitting a column and failing to compile.
type ScopeGate struct {
	VisibleTo     []string
	Active        bool
	ValidFrom     time.Time
	ValidTo       time.Time
	ValidTimeFrom *TimeOfDay
	ValidTimeTo   *TimeOfDay
}

// Gate reads the scope gate off a catalog.
//
// Always off the MERGE RESULT, never off the patch: a republish that carried no
// `validity` must propagate the validity the catalog already had rather than
// NULL it. The type cannot enforce that — it is a rule about the argument — but
// there is exactly one call site per backend and both pass `merged`.
func (c Catalog) Gate() ScopeGate {
	return ScopeGate{
		VisibleTo:     c.VisibleTo,
		Active:        c.Active,
		ValidFrom:     c.ValidFrom,
		ValidTo:       c.ValidTo,
		ValidTimeFrom: c.ValidTimeFrom,
		ValidTimeTo:   c.ValidTimeTo,
	}
}

// ApplyTo copies the gate onto one resource.
func (g ScopeGate) ApplyTo(resource *Resource) {
	resource.VisibleTo = slices.Clone(g.VisibleTo)
	resource.Active = g.Active
	resource.ValidFrom = g.ValidFrom
	resource.ValidTo = g.ValidTo
	resource.ValidTimeFrom = g.ValidTimeFrom
	resource.ValidTimeTo = g.ValidTimeTo
}

// Matches reports whether a resource already carries this exact gate.
//
// It is what the Postgres propagate's `IS DISTINCT FROM` says in SQL, written
// here so the memory backend reaches the same end state by the same rule rather
// than by a coincidence of both being correct. The rule is that every resource
// ENDS the transaction carrying the catalog's gate, not that every resource is
// rewritten.
func (g ScopeGate) Matches(resource Resource) bool {
	return slices.Equal(g.VisibleTo, resource.VisibleTo) &&
		g.Active == resource.Active &&
		g.ValidFrom.Equal(resource.ValidFrom) &&
		g.ValidTo.Equal(resource.ValidTo) &&
		sameTimeOfDay(g.ValidTimeFrom, resource.ValidTimeFrom) &&
		sameTimeOfDay(g.ValidTimeTo, resource.ValidTimeTo)
}

// sameTimeOfDay compares two optional clock times, nil included.
func sameTimeOfDay(left, right *TimeOfDay) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// EnsureVisibleTo is the fail-safe an empty audience gets.
//
// The mapper already applied this default (A9), in both modes, so it should
// never fire. It stays because a catalog visible to no network is findable by
// nobody while reporting success, and that is not a failure worth reaching
// production to discover. The DEFAULT '{}' on the column is the same fail-safe
// one layer down: a state the writer must never store, not a valid one.
func (c *Catalog) EnsureVisibleTo() {
	if len(c.VisibleTo) == 0 {
		c.VisibleTo = []string{c.NetworkID}
	}
}

// DanglingOfferReference is one offer naming resources its catalog does not
// hold, and what the writer must do about it.
//
// A value the repository turns into a Fault rather than a Fault itself, because
// the code is a `beckn.ErrorCode` constant and this package may not import
// `beckn` — purity_test.go fails the build if it tries. Two backends building
// the same fault from the same constant is one string; this package holding its
// own copy of that string would be two.
type DanglingOfferReference struct {
	// Index is the offer's position among the catalog's offers, for the fault
	// path. Relative to the catalog, not to the request: UpsertCatalog does not
	// know which catalog of a multi-catalog publish it is writing.
	Index int

	OfferID string

	// Missing is the ids that name nothing, in the order the publisher sent
	// them.
	Missing []string

	// Dropped is set when pruning left the offer with NO ids at all, which
	// means it was not written. Empty means CATALOG-WIDE, so writing a pruned
	// offer would promote a one-resource offer to the provider's entire
	// inventory — the same trap the FULL prune avoids, reached from the other
	// side.
	Dropped bool
}

// PruneOfferReferences checks every offer against the merged catalog, drops the
// ids that name nothing, and reports what it found.
//
// It mutates: surviving offers keep only the ids that exist, and an offer
// pruned to empty is removed from the catalog outright. Checked against the
// MERGED catalog, so an offer may legitimately name a resource an earlier
// publish stored.
//
// Every offer on the merged catalog is checked, not only the ones the patch
// named. The two are the same set in practice — under MERGE a stored offer was
// already checked when it was written, and under FULL the merge result holds
// only the patch's offers — and "every offer ends this transaction referencing
// resources that exist" is the invariant worth holding, rather than "every
// offer we happened to look at".
//
// This is the ONLY one of the three defences that can catch a reference to a
// resource that never existed — the delete-then-prune pair on a FULL republish
// cannot distinguish a first-publish typo from a correct array. `resource_ids`
// carries no foreign key, because PostgreSQL cannot declare one into an array.
func PruneOfferReferences(merged *Catalog) []DanglingOfferReference {
	held := make(map[string]bool, len(merged.Resources))
	for _, resource := range merged.Resources {
		held[resource.ID] = true
	}

	// A fresh slice rather than merged.Offers[:0]: the merge result may share
	// its backing array with the STORED catalog it was built from, and
	// filtering in place would rewrite the store's own offers.
	var found []DanglingOfferReference
	kept := make([]Offer, 0, len(merged.Offers))

	for index, offer := range merged.Offers {
		// An empty ResourceIDs is CATALOG-WIDE and names nothing to check. It
		// must not fall into the pruning below, which would read it as an
		// offer that lost every id and delete it.
		if len(offer.ResourceIDs) == 0 {
			kept = append(kept, offer)
			continue
		}

		var missing, survives []string
		for _, id := range offer.ResourceIDs {
			if held[id] {
				survives = append(survives, id)
				continue
			}
			missing = append(missing, id)
		}

		if len(missing) == 0 {
			kept = append(kept, offer)
			continue
		}

		found = append(found, DanglingOfferReference{
			Index:   index,
			OfferID: offer.ID,
			Missing: missing,
			Dropped: len(survives) == 0,
		})

		if len(survives) == 0 {
			continue
		}
		offer.ResourceIDs = survives
		kept = append(kept, offer)
	}

	merged.Offers = kept
	return found
}

// Faults turns what the prune found into the PARTIAL faults a publish reports.
//
// The code arrives as a PARAMETER because it is a `beckn.ErrorCode` and this
// package may not import `beckn` — purity_test.go fails the build if it tries.
// Injecting it keeps ONE spelling of the path and the message here, which is
// the drift that actually matters; the code itself is already pinned to the
// schema enum by the test in `beckn`.
//
// The path is CATALOG-RELATIVE. UpsertCatalog writes one catalog and does not
// know which of a multi-catalog publish it is, so the request-relative prefix
// is the publish layer's to add.
func Faults(found []DanglingOfferReference, code string) []Fault {
	faults := make([]Fault, 0, len(found))
	for _, reference := range found {
		faults = append(faults, Fault{
			Path: fmt.Sprintf("$.offers[%d].resourceIds", reference.Index),
			Code: code,
			Message: fmt.Sprintf(
				"offer %q references %s this catalog does not have: %s",
				reference.OfferID, resourceWord(len(reference.Missing)),
				strings.Join(reference.Missing, ", "),
			) + droppedNote(reference.Dropped),
		})
	}
	return faults
}

func resourceWord(count int) string {
	if count == 1 {
		return "a resource"
	}
	return "resources"
}

// droppedNote says so when the offer did not survive the prune, because
// "referenced a missing resource" and "was not stored at all" are different
// things to a publisher reading a PARTIAL and deciding whether to act.
func droppedNote(dropped bool) string {
	if !dropped {
		return ""
	}
	return "; every id it named was missing, so the offer was not stored"
}
