package discover

import (
	"context"
	"slices"
	"strings"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// The discover path's contribution to the span: the three events of 23d,
// recorded through fact rather than onto a span directly (A23), because
// tests/architecture/boundary_test.go refuses the SDK import here. Every name
// below comes from the registry.

// observeIntent records the SHAPE of the question, at intake.
//
// The shape and never the content: textSearch, filters.expression and every
// coordinate are on the never-emitted list, so what goes out is which KINDS of
// criterion were sent, which grammar and which operators.
//
// Called BEFORE MapIntent, which is what makes it able to report a grammar or an
// operator this service refuses — the one question intake is uniquely placed to
// answer, where retrieval_info already says what ran.
func observeIntent(ctx context.Context, envelope beckn.Context, intent beckn.Intent) {
	fact.ObserveStrings(ctx, fact.IntentKinds, intentKinds(intent))

	// Omitted, not written empty, when the caller sent no filter: an empty
	// filter_type says somebody asked for the empty grammar.
	if intent.Filters != nil {
		fact.ObserveString(ctx, fact.IntentFilterType, intent.Filters.Type)
	}

	// Verbatim and with duplicates kept — two S_DWITHIN constraints are two
	// constraints, and collapsing them would report a cheaper query than ran.
	if len(intent.Spatial) > 0 {
		ops := make([]string, 0, len(intent.Spatial))
		for _, constraint := range intent.Spatial {
			ops = append(ops, constraint.Op)
		}
		fact.ObserveStrings(ctx, fact.IntentSpatialOps, ops)
	}

	fact.ObserveBool(ctx, fact.IntentScoped, envelope.NetworkID != "")
	observeSchemaPredicate(ctx, envelope)
}

// intentKinds is which of the four criteria the caller sent, in the order
// beckn.Intent declares them so two identical intents produce one value.
//
// The field AS SENT, untrimmed: MapIntent trims a whitespace-only textSearch
// away, so request_info saying what arrived and retrieval.modes_run saying what
// ran is precisely the diagnosis. Trimming here would hide it.
//
// An intent with none returns an empty list rather than nil, which the record
// keeps: it is what distinguishes case 17's 400 from a request that never
// reached this controller.
func intentKinds(intent beckn.Intent) []string {
	kinds := make([]string, 0, 4)

	for _, kind := range []struct {
		name    string
		present bool
	}{
		{"textSearch", intent.TextSearch != ""},
		{"filters", intent.Filters != nil},
		{"spatial", len(intent.Spatial) > 0},
		{"mediaSearch", len(intent.MediaSearch) > 0},
	} {
		if kind.present {
			kinds = append(kinds, kind.name)
		}
	}
	return kinds
}

// observeSchemaPredicate splits the envelope's schemaContext the same way
// mapSchemaContext does — on the first #, base before and fragment after.
//
// Two parallel lists rather than the raw URIs, so a query can ask for a
// vocabulary without parsing a fragment out of every value. They stay the same
// length by construction: an entry naming no type contributes "" rather than
// being skipped, or every cross-match after it is a claim no request made.
//
// Absent, not empty, when the field was not sent — opentelemetry.md's
// "Absent and empty must stay distinguishable" is why.
func observeSchemaPredicate(ctx context.Context, envelope beckn.Context) {
	if len(envelope.SchemaContext) == 0 {
		return
	}

	bases := make([]string, 0, len(envelope.SchemaContext))
	types := make([]string, 0, len(envelope.SchemaContext))
	for _, uri := range envelope.SchemaContext {
		base, fragment, _ := strings.Cut(uri, "#")
		bases = append(bases, base)
		types = append(types, fragment)
	}

	fact.ObserveStrings(ctx, fact.BecknSchemaContext, bases)
	fact.ObserveStrings(ctx, fact.BecknSchemaType, types)
}

// observeRetrieval records what actually ran, at the moment the store answered.
//
// In the service rather than the controller, because which modes ran is
// negotiate's answer and known nowhere else — the controller sees only the
// degraded list, which is half of this under a name claiming all of it.
func observeRetrieval(ctx context.Context, modes []domain.Capability, degraded []string) {
	run := make([]string, 0, len(modes))
	for _, mode := range modes {
		run = append(run, string(mode))
	}

	fact.ObserveStrings(ctx, fact.RetrievalModesRun, run)
	fact.ObserveStrings(ctx, fact.RetrievalModesDegraded, degraded)
}

// observeResult records what went back.
//
// The count is written even when it is zero — zero is the answer, not the absence
// of one, which is why ResultCatalogCount carries no ZeroIsAbsent — and
// result.empty says the same thing in the form an alert can be written against.
func observeResult(ctx context.Context, catalogs []beckn.Catalog) {
	fact.ObserveInt64(ctx, fact.ResultCatalogCount, int64(len(catalogs)))
	fact.ObserveStrings(ctx, fact.ResultProviderIDs, providerIDs(catalogs))
	fact.ObserveBool(ctx, fact.ResultEmpty, len(catalogs) == 0)
}

// providerIDs is the DISTINCT provider-node ids of what was returned — whose data
// answered, never how many catalogs came back. First-seen order, so two identical
// answers produce one value.
//
// Not bounded here — ResultProviderIDs carries MaxEntries and the record clamps
// against it, so the limit is stated once, on the Definition.
//
// Read off Catalog.BppID, which keeps that spelling only because Catalog closes
// with additionalProperties: false (A24); no attribute here repeats it.
func providerIDs(catalogs []beckn.Catalog) []string {
	ids := make([]string, 0, len(catalogs))
	for _, catalog := range catalogs {
		if catalog.BppID != "" && !slices.Contains(ids, catalog.BppID) {
			ids = append(ids, catalog.BppID)
		}
	}
	return ids
}
