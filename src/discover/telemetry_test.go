package discover_test

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// The discover path's half of 23d: the request_info, retrieval_info and
// response_info events, asserted against the record rather than against an
// exported span.
//
// Against the record because that is where the controller's contribution ends.
// A23 keeps the OTel SDK out of this package entirely, so a test here that
// wanted a span would have to build a provider it is forbidden to import; the
// projection from record to span is pinned once, in
// telemetry/span_test.go, and pinning it again per controller would be
// three more copies of the same assertion to keep true.

// recorded serves one discover request and gives back the facts it produced.
//
// The record is allocated by a wrapper standing in for Trace rather than by
// mounting Trace itself, because Trace needs a tracer and a tracer needs the
// SDK. fact.New is the same call Trace makes, and it is the whole of what a
// controller depends on.
func recorded(
	t *testing.T, repo domain.SearchRepository, cfg config.Config, body string,
) *fact.Record {
	t.Helper()

	var record *fact.Record
	mounted := mount(t, repo, cfg)

	post(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, allocated := fact.New(r.Context())
		record = allocated
		mounted.ServeHTTP(w, r.WithContext(ctx))
	}), "/discover", body)

	return record
}

// look reads one fact, failing rather than returning a zero Observation that
// every subsequent assertion would then compare against and pass.
func look(t *testing.T, record *fact.Record, key fact.Key) fact.Observation {
	t.Helper()

	observation, found := record.Lookup(key)
	if !found {
		t.Fatalf("%s was never observed", fact.Of(key).Name)
	}
	return observation
}

func absent(t *testing.T, record *fact.Record, key fact.Key) {
	t.Helper()

	if observation, found := record.Lookup(key); found {
		t.Errorf("%s was observed as %+v; it should be absent", fact.Of(key).Name, observation)
	}
}

// TestTheIntentShapeIsObservedBeforeTheIntentIsRefused.
//
// Both the grammar and the operator here are ones this service does not serve,
// so this request is a 400 that never reaches the store. The facts are recorded
// anyway, and that is the entire reason request_info fires at intake: "who is
// asking for something we do not answer" is the question worth having, and
// observing after the mapper would answer it only for requests that succeeded.
func TestTheIntentShapeIsObservedBeforeTheIntentIsRefused(t *testing.T) {
	repo := &stubRepo{capabilities: everything()}

	record := recorded(t, repo, settings(), `{"context":{
		"action":"discover","networkId":"oan"},
		"message":{"intent":{
			"textSearch":"wheat",
			"filters":{"type":"rfc9535","expression":"$..price"},
			"spatial":[{
				"op":"S_CROSSES",
				"targets":"$.catalogs[*].provider.availableAt[*].geo",
				"geometry":{"type":"Point","coordinates":[77.5946,12.9716]}}]}}}`)

	if repo.calls != 0 {
		t.Fatalf("the store was searched %d times; this intent is refused", repo.calls)
	}

	kinds := look(t, record, fact.IntentKinds).List
	if want := []string{"textSearch", "filters", "spatial"}; !slices.Equal(kinds, want) {
		t.Errorf("intent.kinds = %v, want %v", kinds, want)
	}
	if got := look(t, record, fact.IntentFilterType).Text; got != "rfc9535" {
		t.Errorf("intent.filter_type = %q, want the grammar as SENT, refused or not", got)
	}
	if got := look(t, record, fact.IntentSpatialOps).List; !slices.Equal(got, []string{"S_CROSSES"}) {
		t.Errorf("intent.spatial_ops = %v, want [S_CROSSES]", got)
	}
	if got := look(t, record, fact.IntentScoped).Bool; !got {
		t.Errorf("intent.scoped = false for an envelope naming networkId oan")
	}
}

// TestAnIntentWithNoCriterionReportsNoKindsRatherThanNothing.
//
// examples/17-discover-no-criterion.json is a 400, and the empty list is what
// says so. Omitting intent.kinds instead would make a criterion-less intent
// indistinguishable from a request that never reached the controller — the
// larger population, since every refusal above it is one.
func TestAnIntentWithNoCriterionReportsNoKindsRatherThanNothing(t *testing.T) {
	record := recorded(t, &stubRepo{capabilities: everything()}, settings(),
		`{"context":{"action":"discover"},"message":{"intent":{}}}`)

	if got := look(t, record, fact.IntentKinds).List; len(got) != 0 {
		t.Errorf("intent.kinds = %v for an empty intent, want []", got)
	}

	// The two that describe a part of the intent nobody sent. Written empty they
	// would say a caller asked for the empty grammar over no operators.
	absent(t, record, fact.IntentFilterType)
	absent(t, record, fact.IntentSpatialOps)

	// Not scoped is an answer, so it is present and false.
	if look(t, record, fact.IntentScoped).Bool {
		t.Errorf("intent.scoped = true for an envelope with no networkId")
	}
}

// TestTheSchemaPredicateIsAbsentRatherThanEmptyWhenTheSeekerSentNone.
//
// The acceptance criterion at opentelemetry.md:1140. Absent means no predicate
// at all — every capability matches, which is the seeking-anything bucket and
// likely the largest one — and empty means a seeker who sent an empty array.
// Collapsing them loses the larger of the two.
func TestTheSchemaPredicateIsAbsentRatherThanEmptyWhenTheSeekerSentNone(t *testing.T) {
	repo := &stubRepo{capabilities: everything()}

	absent(t, recorded(t, repo, settings(), wheat), fact.BecknSchemaContext)
	absent(t, recorded(t, repo, settings(), wheat), fact.BecknSchemaType)
}

// TestTheSchemaPredicateIsSplitIntoItsTwoParallelLists, so a query can ask for
// a base vocabulary without also having to parse the fragment out of it.
//
// The two stay the same length by construction: an entry naming no type
// contributes an empty string rather than being skipped, or the pairing between
// the lists — which is the only thing that makes either useful — silently
// shifts by one.
func TestTheSchemaPredicateIsSplitIntoItsTwoParallelLists(t *testing.T) {
	record := recorded(t, &stubRepo{capabilities: everything()}, settings(),
		`{"context":{"action":"discover",
			"schemaContext":["https://schema.org#WeatherForecast","https://example.org/agri"]},
		"message":{"intent":{"textSearch":"rain"}}}`)

	contexts := look(t, record, fact.BecknSchemaContext).List
	types := look(t, record, fact.BecknSchemaType).List

	if want := []string{"https://schema.org", "https://example.org/agri"}; !slices.Equal(contexts, want) {
		t.Errorf("beckn.schemaContext = %v, want %v — the base, with the fragment cut off", contexts, want)
	}
	if want := []string{"WeatherForecast", ""}; !slices.Equal(types, want) {
		t.Errorf("beckn.schemaType = %v, want %v", types, want)
	}
	if len(contexts) != len(types) {
		t.Errorf("the two lists are %d and %d long; they are read as pairs", len(contexts), len(types))
	}
}

// TestTheProviderIdsAreDistinct, which is what makes the attribute an identity
// rather than a page-size measurement wearing one.
//
// Twenty catalogs from two providers is two ids — the acceptance criterion's own
// example, scaled down. More catalogs than the registry's bound on purpose: if
// distinctness were lost, the clamp would silently leave a full sixteen, which
// looks like a plausible answer. Twenty asserted against two is the shape that
// cannot be mistaken for one.
//
// It still does not restate the bound. That number lives on the Definition, and
// a test naming it here would be a second place to change when it moves;
// fact/record_test.go pins the clamp itself over one key for every key.
func TestTheProviderIdsAreDistinct(t *testing.T) {
	catalogs := make([]domain.Catalog, 0, 20)
	for index := range 20 {
		id := fmt.Sprintf("c%d", index)
		publisher := "imd.gov.in"
		if index == 19 {
			publisher = "agmarknet.gov.in"
		}
		catalogs = append(catalogs, domain.Catalog{
			ID:       id,
			Document: []byte(fmt.Sprintf(`{"id":%q,"bppId":%q}`, id, publisher)),
		})
	}
	repo := &stubRepo{
		capabilities: everything(),
		result:       domain.SearchResult{Catalogs: catalogs},
	}

	record := recorded(t, repo, settings(), wheat)

	// The count is the page size and the ids are an identity. They answer
	// different questions, which is exactly what a list that counted catalogs
	// would hide.
	if got := look(t, record, fact.ResultCatalogCount).Int; got != 20 {
		t.Errorf("result.catalog_count = %d, want 20", got)
	}
	ids := look(t, record, fact.ResultProviderIDs).List
	if want := []string{"imd.gov.in", "agmarknet.gov.in"}; !slices.Equal(ids, want) {
		t.Errorf("result.provider_ids = %v, want %v — how many providers SERVED, "+
			"not how many catalogs came back", ids, want)
	}
	if look(t, record, fact.ResultEmpty).Bool {
		t.Errorf("result.empty = true for twenty catalogs")
	}
}

// TestAnEmptyAnswerSaysSo. The most valuable signal this service gives the
// network: somebody asked and nobody serves it. A zero catalog_count says the
// same thing, and this is the one an alert can be written against without
// knowing that zero is special.
func TestAnEmptyAnswerSaysSo(t *testing.T) {
	record := recorded(t, &stubRepo{capabilities: everything()}, settings(), wheat)

	if !look(t, record, fact.ResultEmpty).Bool {
		t.Errorf("result.empty = false for an answer with no catalogs")
	}
	if got := look(t, record, fact.ResultCatalogCount).Int; got != 0 {
		t.Errorf("result.catalog_count = %d, want 0", got)
	}

	// Zero is the answer, not the absence of one — so the count is present
	// alongside the flag rather than dropped by ZeroIsAbsent.
	if _, found := record.Lookup(fact.ResultCatalogCount); !found {
		t.Errorf("result.catalog_count was dropped because it was zero")
	}
}

// TestTheModesRunAreObservedWhereTheStoreAnswered.
//
// In the service, not the controller, and that is an extra file beyond the
// three opentelemetry.md:1140 lists — deliberately. Which modes ran is decided
// by negotiate and known nowhere else; the controller sees only the degraded
// list, so observing there would report half the fact under a name claiming the
// whole of it.
func TestTheModesRunAreObservedWhereTheStoreAnswered(t *testing.T) {
	record := recorded(t, &stubRepo{capabilities: phase1()}, settings(), wheat)

	run := look(t, record, fact.RetrievalModesRun).List
	if !slices.Contains(run, "lexical") {
		t.Errorf("retrieval.modes_run = %v, want lexical among them", run)
	}
	if slices.Contains(run, "semantic") {
		t.Errorf("retrieval.modes_run = %v; this deployment cannot run semantic", run)
	}

	degraded := look(t, record, fact.RetrievalModesDegraded).List
	if !slices.Contains(degraded, "semantic") {
		t.Errorf("retrieval.modes_degraded = %v, want semantic among them", degraded)
	}
}

// TestNothingIsObservedAboutRetrievalWhenTheStoreWasNeverReached, which is what
// makes the absence of a retrieval_info event mean something.
//
// An empty event stamped at the span's end would read as a phase that ran and
// produced nothing — a different and much more alarming claim than a phase that
// did not run at all.
func TestNothingIsObservedAboutRetrievalWhenTheStoreWasNeverReached(t *testing.T) {
	record := recorded(t, &stubRepo{capabilities: everything()}, settings(),
		`{"context":{"action":"discover"},"message":{"intent":{}}}`)

	absent(t, record, fact.RetrievalModesRun)
	absent(t, record, fact.RetrievalModesDegraded)
	absent(t, record, fact.ResultCatalogCount)
	absent(t, record, fact.ResultEmpty)
}

// TestTheEmbeddingDurationIsAbsentWhenNoVectorWasComputed — the criterion at
// opentelemetry.md:1140, in the configuration Phase 1 actually ships as.
//
// The stub repository computes no embedding at all, which is the same shape
// EMBEDDING_PROVIDER=noop produces: queryVector returns a nil vector, so there
// is no duration to report. A zero would be a claim that embedding took no
// measurable time, which is a different and false statement — and the one an
// average over the attribute would silently believe.
func TestTheEmbeddingDurationIsAbsentWhenNoVectorWasComputed(t *testing.T) {
	absent(t, recorded(t, &stubRepo{capabilities: everything()}, settings(), wheat),
		fact.RetrievalEmbeddingMs)
}
