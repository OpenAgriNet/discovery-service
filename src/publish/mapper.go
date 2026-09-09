package publish

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// clockLayouts are the spellings a daily bound may arrive in, tried in order.
//
// The offset forms carry their own zone and are exact. The bare forms carry
// none and are resolved in the deployment's default timezone, which is the only
// reading that makes "09:00:00" mean what the publisher meant by it.
var clockLayouts = []string{"15:04:05Z07:00", "15:04Z07:00"}

// bareClockLayouts are the same two without an offset.
var bareClockLayouts = []string{"15:04:05", "15:04"}

// clockReferenceDate is the day a bare clock time is resolved against.
//
// It matters. time.ParseInLocation("15:04:05", …) lands on year 0, where
// Asia/Kolkata's zoneinfo still holds LMT at +05:53:28 rather than the +05:30
// in force since 1906 — so a bare 09:00:00 would normalise to 03:06:32 UTC
// instead of 03:30:00, and nothing downstream would look wrong.
//
// A fixed date is also why a deployment in a DST zone gets one offset for the
// whole year rather than a window that shifts twice; see the plan's Deferred
// section.
var clockReferenceDate = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

// MapCatalog turns one wire catalog and its directive into the patch the merge
// applies, separating the faults that refuse the catalog from those that only
// qualify it.
//
// It returns a CatalogPatch rather than a Catalog (A8) because a
// defaults-filled struct cannot say whether the publisher sent a field or
// omitted it, and MERGE turns exactly that distinction into the difference
// between keeping a publisher's data and deleting it.
//
// zone is APP_DEFAULT_TIMEZONE, and it is a parameter rather than a config
// lookup so that a test can ask what 09:00:00 means in Kolkata without setting
// a process-wide environment variable.
// version is `context.version` from the publish envelope, and it is a
// parameter for the same reason zone is: the mapper is the last layer that can
// tell an absent version from a declared one, and the fallback belongs where
// that distinction still exists.
func MapCatalog(
	catalog beckn.Catalog, directive beckn.PublishDirective, network string, zone *time.Location,
	version string,
) (domain.CatalogPatch, []domain.Fault, []domain.Fault) {
	validity, fatal := mapValidity(catalog.Validity, "$['validity']", zone)

	resources, resourceFaults := mapResources(catalog.Resources)
	fatal = append(fatal, resourceFaults...)

	offers, offerFaults := mapOffers(catalog.Offers, zone)
	fatal = append(fatal, offerFaults...)

	// A9, both of them, resolved HERE and in both update modes — so no absence
	// reaches the merge and the merge needs no branch for one.
	active := catalog.IsActive == nil || *catalog.IsActive

	document, documentFault := catalogDocument(catalog, active)
	fatal = append(fatal, documentFault...)

	if len(fatal) > 0 {
		// Stores NOTHING. A catalog with a fatal fault is refused whole rather
		// than partially applied: a half-applied MERGE is not a state any later
		// publish can reason about, because the stored document no longer
		// corresponds to anything a publisher sent.
		return domain.CatalogPatch{}, fatal, nil
	}

	return domain.CatalogPatch{
		ID:        catalogID(catalog, directive),
		NetworkID: network,
		Document:  document,
		Validity:  validity,

		Active:    active,
		VisibleTo: visibleTo(directive, network),

		ProtocolVersion: protocolVersion(version),

		Resources: resources,
		Offers:    offers,
	}, nil, nil
}

// catalogDocument is what gets stored: the catalog verbatim, minus the two
// child arrays that own their own rows (A17), with `isActive` resolved.
//
// Resolving `isActive` into the document is the one place A9 and RFC 7396
// disagree and something has to settle it. A9 says an omitted `isActive` RESETS
// the catalog to live; 7396 says a member a patch does not mention is KEPT. Now
// that the member lives inside the document rather than beside it, leaving it
// to the merge would give scenario 26 the 7396 answer. Writing the resolved
// value in before the merge runs means the merge sees a patch that always
// mentions it, and the two rules stop competing.
//
// It also keeps the document and the `active` column agreeing by construction:
// both are this same bool, so no reader has to decide which one is true.
func catalogDocument(catalog beckn.Catalog, active bool) (json.RawMessage, []domain.Fault) {
	members, err := catalog.WithoutChildren()
	if err != nil {
		return nil, []domain.Fault{{
			Path:    "$",
			Code:    string(beckn.CodeSchemaInvalidFormat),
			Message: fmt.Sprintf("the catalog is not a JSON object: %v", err),
		}}
	}

	resolved, err := json.Marshal(active)
	if err != nil {
		return nil, []domain.Fault{{
			Path:    "$['isActive']",
			Code:    string(beckn.CodeSchemaInvalidFormat),
			Message: fmt.Sprintf("isActive is not encodable: %v", err),
		}}
	}
	members["isActive"] = resolved

	document, err := json.Marshal(members)
	if err != nil {
		return nil, []domain.Fault{{
			Path:    "$",
			Code:    string(beckn.CodeSchemaInvalidFormat),
			Message: fmt.Sprintf("the catalog is not re-encodable: %v", err),
		}}
	}
	return document, nil
}

// catalogID prefers the directive's id: it is what publishOne keyed the catalog
// on, and the body's own id is the one L1 validation already compared against
// it.
func catalogID(catalog beckn.Catalog, directive beckn.PublishDirective) string {
	if directive.CatalogID != "" {
		return directive.CatalogID
	}
	return catalog.ID
}

// visibleTo resolves C8: an omitted or empty visibleTo is the request's own
// network, not every network.
//
// The deviation from the spec's "visible to all eligible subscribers" is
// deliberate — publishing to every network by a typo is the worse of the two
// failures, and a publisher wanting network-wide reach can say so.
func visibleTo(directive beckn.PublishDirective, network string) []string {
	if len(directive.VisibleTo) == 0 {
		return []string{network}
	}
	return directive.VisibleTo
}

// mapResources carries each resource's document across verbatim and refuses a
// resource that has no id.
//
// Resources merge by id, so an empty id is not a key the merge can place: it
// would insert a row keyed on "" that the next publish silently patches instead
// of inserting beside.
func mapResources(resources []beckn.Resource) ([]domain.ResourcePatch, []domain.Fault) {
	if len(resources) == 0 {
		return nil, nil
	}

	out := make([]domain.ResourcePatch, 0, len(resources))
	var faults []domain.Fault

	for i, resource := range resources {
		if resource.ID == "" {
			faults = append(faults, domain.Fault{
				Path:    fmt.Sprintf("$['resources'][%d]['id']", i),
				Code:    string(beckn.CodeSchemaValidationFailed),
				Message: "a resource needs an id; resources merge by it",
			})
			continue
		}
		out = append(out, domain.ResourcePatch{
			ID:       resource.ID,
			Document: resourceDocument(resource),
		})
	}

	return out, faults
}

// resourceDocument is the resource as it arrived, or as its fields describe it
// when it was built in Go rather than decoded.
//
// The fallback goes through MarshalJSON rather than returning nil, because a
// nil document would merge as "absent" and a hand-built resource would then
// store nothing at all — which is how the conformance suite and every unit test
// that constructs a beckn.Resource would silently stop asserting anything.
func resourceDocument(resource beckn.Resource) json.RawMessage {
	if len(resource.Raw) > 0 {
		return resource.Raw
	}

	document, err := json.Marshal(resource)
	if err != nil {
		return nil
	}
	return document
}

// mapOffers carries the VERBATIM offer document across and resolves the one
// default an offer has.
func mapOffers(offers []beckn.Offer, zone *time.Location) ([]domain.OfferPatch, []domain.Fault) {
	if len(offers) == 0 {
		return nil, nil
	}

	out := make([]domain.OfferPatch, 0, len(offers))
	var faults []domain.Fault

	for i, offer := range offers {
		at := fmt.Sprintf("$['offers'][%d]", i)
		if offer.ID == "" {
			faults = append(faults, domain.Fault{
				Path:    at + "['id']",
				Code:    string(beckn.CodeSchemaValidationFailed),
				Message: "an offer needs an id; offers merge by it",
			})
			continue
		}

		validity, validityFaults := mapValidity(offer.Validity, at+"['validity']", zone)
		faults = append(faults, validityFaults...)

		out = append(out, domain.OfferPatch{
			ID:       offer.ID,
			Document: offer.Raw,
			// A9: an absent resourceIds is CATALOG-WIDE, which is the empty
			// slice — resolved here so no absence reaches the merge.
			ResourceIDs: resolvedResourceIDs(offer.ResourceIDs),
			Validity:    validity,
		})
	}

	return out, faults
}

// resolvedResourceIDs turns a nil into the empty slice the default names.
// Non-nil matters: nil and empty are the same set, and returning nil would put
// the absence the mapper just resolved back on the patch.
func resolvedResourceIDs(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

// mapValidity expands `validity` into four independent tri-states.
//
// Absent is nil — keep what is stored. An explicit null clears all four, which
// is the only reading of "validity": null that RFC 7396 admits. Each member is
// then independent, because a patch may clear an end date and keep a start one.
func mapValidity(raw json.RawMessage, at string, zone *time.Location) (*domain.TimePeriodPatch, []domain.Fault) {
	if len(raw) == 0 {
		return nil, nil
	}
	if isJSONNull(raw) {
		return &domain.TimePeriodPatch{
			StartDate: domain.Nullable[time.Time]{Set: true, Null: true},
			EndDate:   domain.Nullable[time.Time]{Set: true, Null: true},
			StartTime: domain.Nullable[domain.TimeOfDay]{Set: true, Null: true},
			EndTime:   domain.Nullable[domain.TimeOfDay]{Set: true, Null: true},
		}, nil
	}

	var period beckn.TimePeriod
	if err := json.Unmarshal(raw, &period); err != nil {
		return nil, []domain.Fault{{
			Path:    at,
			Code:    string(beckn.CodeSchemaInvalidFormat),
			Message: fmt.Sprintf("validity is not a time period: %v", err),
		}}
	}

	reader := &periodReader{at: at, zone: zone}
	patch := &domain.TimePeriodPatch{
		StartDate: reader.instant(period.StartDate, "startDate"),
		EndDate:   reader.instant(period.EndDate, "endDate"),
		StartTime: reader.clock(period.StartTime, "startTime"),
		EndTime:   reader.clock(period.EndTime, "endTime"),
	}

	return patch, append(reader.faults, dailyPairFault(patch, at)...)
}

// periodReader reads the four members of a validity, accumulating faults so the
// four reads stay four lines.
//
// All four are attempted rather than stopping at the first: a publisher fixing
// one bound at a time round-trips once per mistake.
type periodReader struct {
	at     string
	zone   *time.Location
	faults []domain.Fault
}

// dailyPairFault refuses half a daily window.
//
// The spec's anyOf requires both bounds, and guessing the missing one invents a
// window the publisher never stated — one that then silently decides whether
// every resource in the catalog is findable at 23:00.
func dailyPairFault(patch *domain.TimePeriodPatch, at string) []domain.Fault {
	start, end := isValue(patch.StartTime), isValue(patch.EndTime)
	if start == end {
		return nil
	}

	missing := "endTime"
	if end {
		missing = "startTime"
	}
	return []domain.Fault{{
		Path:    at,
		Code:    string(beckn.CodeSchemaValidationFailed),
		Message: "a daily window needs both bounds; " + missing + " is missing",
	}}
}

// isValue reports whether a member carries an actual bound, as opposed to being
// absent or an explicit delete.
func isValue[T any](member domain.Nullable[T]) bool { return member.Set && !member.Null }

// instant reads one RFC 3339 bound of the calendar half.
func (r *periodReader) instant(raw json.RawMessage, member string) domain.Nullable[time.Time] {
	text, state, ok := r.scalar(raw, member)
	if text == "" {
		return domain.Nullable[time.Time]{Set: ok && state.set, Null: state.null}
	}

	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		r.fault(member, fmt.Sprintf("%q is not an RFC 3339 instant", text))
		return domain.Nullable[time.Time]{}
	}
	return domain.Nullable[time.Time]{Value: at.UTC(), Set: true}
}

// clock reads one bound of the daily half and normalises it to UTC, so nothing
// downstream performs a timezone lookup per row.
func (r *periodReader) clock(raw json.RawMessage, member string) domain.Nullable[domain.TimeOfDay] {
	text, state, ok := r.scalar(raw, member)
	if text == "" {
		return domain.Nullable[domain.TimeOfDay]{Set: ok && state.set, Null: state.null}
	}

	at, parsed := parseClock(text, r.zone)
	if !parsed {
		r.fault(member, fmt.Sprintf("%q is not a time of day", text))
		return domain.Nullable[domain.TimeOfDay]{}
	}
	return domain.Nullable[domain.TimeOfDay]{Value: at, Set: true}
}

// memberState is the absent/null half of a tri-state, before any parsing.
type memberState struct {
	set  bool
	null bool
}

// scalar resolves the three states a raw member can be in, returning the string
// only when there is one to parse. The bool is false when the member was
// unreadable, which is already recorded as a fault.
func (r *periodReader) scalar(raw json.RawMessage, member string) (string, memberState, bool) {
	if len(raw) == 0 {
		return "", memberState{}, true
	}
	if isJSONNull(raw) {
		return "", memberState{set: true, null: true}, true
	}

	var text string
	if err := json.Unmarshal(raw, &text); err != nil || text == "" {
		r.fault(member, "expected a non-empty string")
		return "", memberState{}, false
	}
	return text, memberState{set: true}, true
}

// fault records one unreadable member, named by its own key.
func (r *periodReader) fault(member, message string) {
	r.faults = append(r.faults, domain.Fault{
		Path:    r.at + "['" + member + "']",
		Code:    string(beckn.CodeSchemaInvalidFormat),
		Message: message,
	})
}

// parseClock reads a daily bound in either spelling and returns it in UTC.
//
// An offset form is exact. A bare form is resolved in zone against a fixed
// reference date — see clockReferenceDate for why the date is not optional.
func parseClock(text string, zone *time.Location) (domain.TimeOfDay, bool) {
	for _, layout := range clockLayouts {
		if at, err := time.Parse(layout, text); err == nil {
			return timeOfDay(at.UTC()), true
		}
	}

	for _, layout := range bareClockLayouts {
		at, err := time.Parse(layout, text)
		if err != nil {
			continue
		}
		local := time.Date(
			clockReferenceDate.Year(), clockReferenceDate.Month(), clockReferenceDate.Day(),
			at.Hour(), at.Minute(), at.Second(), 0, zone)
		return timeOfDay(local.UTC()), true
	}

	return domain.TimeOfDay{}, false
}

// timeOfDay drops the date an instant carries but nobody supplied.
func timeOfDay(at time.Time) domain.TimeOfDay {
	return domain.TimeOfDay{Hour: at.Hour(), Minute: at.Minute(), Second: at.Second()}
}

// isJSONNull reports whether raw is the literal null, which under RFC 7396 is
// the instruction to delete rather than a value.
func isJSONNull(raw json.RawMessage) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
		case 'n':
			return string(raw[len(raw)-4:]) == "null"
		default:
			return false
		}
	}
	return false
}

// protocolVersion resolves an absent `context.version` to the version this
// build serves.
//
// Defaulted here rather than by the column's own DEFAULT, because the DEFAULT
// only fires on INSERT: a republish that dropped `version` would otherwise keep
// whatever the first publish declared, and a catalog would report a version no
// request in its history ever sent.
//
// C6's envelope rules make `version` required and pin it to `beckn.Version`, so
// nothing arriving over HTTP can currently reach the empty branch. It is here
// because this is where the resolution belongs the day that gate widens, and
// because MapCatalog is called directly by tests and by any future caller that
// does not come through the validator — a mapper that only works behind one
// particular gate is a mapper with an unwritten precondition.
func protocolVersion(declared string) string {
	if declared == "" {
		return beckn.Version
	}
	return declared
}
