package discover

import (
	"context"
	"slices"
	"strings"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// The discover path's contribution to the span: the three events of 23d, put on
// the record and projected onto the span by Trace.
//
// Through fact rather than onto a span directly, and A23 is why: a controller
// that linked the OpenTelemetry SDK would drag it into src/domain and
// src/storage behind it, and tests/architecture/boundary_test.go refuses the
// import. Every name below comes from the registry — nothing here spells an
// attribute key.

// observeIntent records the SHAPE of the question, at intake.
//
// The shape and never the content: textSearch, filters.expression and every
// coordinate are on the never-emitted list, so what goes out is which KINDS of
// criterion were sent, which grammar was named and which operators — the parts
// a capacity question needs and a farmer's query does not survive in.
//
// Called before MapIntent rather than after, which is what makes it able to
// report a grammar or an operator this service refuses. Observing the mapped
// query instead would answer "what did we run" a second time — retrieval_info
// already does that — and would leave "who is asking for what we do not serve"
// unaskable, which is the one question intake is uniquely placed to answer.
func observeIntent(ctx context.Context, envelope beckn.Context, intent beckn.Intent) {
	fact.ObserveStrings(ctx, fact.IntentKinds, intentKinds(intent))

	// Omitted, not written empty, when the caller sent no filter: an empty
	// filter_type would say somebody asked for the empty grammar.
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
// beckn.Intent declares them so two identical intents cannot produce two
// different attribute values.
//
// The field AS SENT, untrimmed. A whitespace-only textSearch is a text search
// the caller believes they sent, and MapIntent trims it away — so request_info
// says what arrived and retrieval.modes_run says what ran, and the pair
// disagreeing is precisely the diagnosis. Trimming here would hide it in the
// one place a reader could have seen it.
//
// An intent with none returns an empty list rather than nil, which the record
// keeps: examples/17-discover-no-criterion.json is a 400, and the empty list is
// what distinguishes it from a request that never reached this controller.
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
// being skipped, or the pairing shifts by one and every cross-match after it is
// a claim no request made.
//
// Absent, not empty, when the field was not sent. Absent means no predicate at
// all — every capability matches, which is the seeking-anything bucket and the
// larger of the two — and empty means a seeker who sent an empty array.
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
// In the service rather than the controller, which is an extra file beyond the
// three opentelemetry.md:1140 names. Which modes ran is negotiate's answer and
// is known nowhere else — the controller sees only the degraded list — so
// observing up there would report half of this under a name claiming all of it.
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
// result.catalog_count is written even when it is zero, deliberately: zero is
// the answer and not the absence of one, and ResultCatalogCount carries no
// ZeroIsAbsent for that reason. result.empty says the same thing in the form an
// alert can be written against without knowing that zero is special — somebody
// asked and nobody serves it, which is the most valuable signal this service
// gives the network.
func observeResult(ctx context.Context, catalogs []beckn.Catalog) {
	fact.ObserveInt64(ctx, fact.ResultCatalogCount, int64(len(catalogs)))
	fact.ObserveStrings(ctx, fact.ResultProviderIDs, providerIDs(catalogs))
	fact.ObserveBool(ctx, fact.ResultEmpty, len(catalogs) == 0)
}

// providerIDs is the DISTINCT provider-node ids of what was returned — whose
// data answered, never how many catalogs came back.
//
// Distinct because a discover answering with 200 catalogs from one provider
// must emit one id, or the attribute becomes a page-size measurement wearing an
// identity's name. First-seen order, so two identical answers produce one value.
//
// Not bounded here: ResultProviderIDs carries MaxEntries and the record clamps
// against it, so the limit is stated once, on the Definition, and a second
// clamp in this function would be a second place to change when it moves.
//
// Read off Catalog.BppID. OAN does not use bap/bpp terminology and no attribute
// here repeats it; the struct field keeps that spelling only because Catalog
// closes with additionalProperties: false.
func providerIDs(catalogs []beckn.Catalog) []string {
	ids := make([]string, 0, len(catalogs))
	for _, catalog := range catalogs {
		if catalog.BppID != "" && !slices.Contains(ids, catalog.BppID) {
			ids = append(ids, catalog.BppID)
		}
	}
	return ids
}
