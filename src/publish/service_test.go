package publish_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/indexing/embeddings"
	"github.com/OpenAgriNet/discovery-service/src/publish"
	"github.com/OpenAgriNet/discovery-service/src/storage/memory"
)

// indexResolution is the H3 resolution the in-memory store covers at. Any value
// works here — nothing in this file asserts on cells — but it must be one, and
// naming it stops a bare literal reading as significant.
const indexResolution = 8

// recordingReplicator is the A7 seam under observation.
//
// The ordering rule it exists for — announce only after the transaction
// commits — is invisible in every response, so the only way to assert it is to
// record the calls and compare them against what was stored.
type recordingReplicator struct {
	mu        sync.Mutex
	announced []string
	err       error
}

func (r *recordingReplicator) Replicate(_ context.Context, catalogID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.announced = append(r.announced, catalogID)
	return r.err
}

func (r *recordingReplicator) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.announced...)
}

// recordingRepo is the real in-memory store with a tap on the write.
//
// Embedded rather than reimplemented: the assertions about what was STORED have
// to run against the same merge every backend runs, or a test can pass against
// a store that agrees with nothing. The tap records the two things the response
// cannot show — the mode the service resolved, and the patch it built.
type recordingRepo struct {
	*memory.Repository

	modes   []domain.UpdateMode
	patches []domain.CatalogPatch

	// err, when set, fails the write. Nothing is stored, which is what makes
	// this the rolled-back transaction the replicator must not have seen.
	err error
}

func newRepo() *recordingRepo {
	return &recordingRepo{Repository: memory.New(indexResolution)}
}

func (r *recordingRepo) UpsertCatalog(
	ctx context.Context, patch domain.CatalogPatch, mode domain.UpdateMode, derive domain.DeriveFunc,
) ([]domain.Fault, error) {
	r.modes = append(r.modes, mode)
	r.patches = append(r.patches, patch)

	if r.err != nil {
		return nil, r.err
	}
	return r.Repository.UpsertCatalog(ctx, patch, mode, derive)
}

func newService(t *testing.T, repo domain.CatalogRepository, replicator domain.CatalogReplicator) *publish.Service {
	t.Helper()

	return publish.NewService(repo, replicator, embeddings.NewNoop(0), network, kolkata(t))
}

// publishBody runs one publish request expressed as the JSON a caller sends, so
// a test states the wire shape rather than a struct literal that has already
// made half the decisions under test.
func publishBody(t *testing.T, service *publish.Service, message string) []beckn.CatalogProcessingResult {
	t.Helper()

	var action beckn.CatalogPublishAction
	if err := json.Unmarshal([]byte(message), &action); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	return service.Publish(t.Context(), beckn.Context{Action: beckn.ActionPublish}, action)
}

func resultFor(t *testing.T, results []beckn.CatalogProcessingResult, catalogID string) beckn.CatalogProcessingResult {
	t.Helper()

	for _, result := range results {
		if result.CatalogID == catalogID {
			return result
		}
	}
	t.Fatalf("no result for %q in %+v", catalogID, results)
	return beckn.CatalogProcessingResult{}
}

// A1, and the per-catalog transaction boundary that makes it survivable.
//
// One publisher's refused catalog must not take the catalogs beside it down:
// wrapping the request in one transaction would make a MASTER in slot two an
// outage for slot one.
func TestAMasterCatalogBesideARegularOneLandsTheRegularOne(t *testing.T) {
	repo, replicator := newRepo(), &recordingReplicator{}

	results := publishBody(t, newService(t, repo, replicator), `{
		"catalogs": [{"id":"regular"}, {"id":"master"}],
		"publishDirectives": [
			{"catalogId":"regular","catalogType":"REGULAR"},
			{"catalogId":"master","catalogType":"MASTER"}
		]
	}`)

	if len(results) != 2 {
		t.Fatalf("results = %d, want one per catalog", len(results))
	}
	if got := resultFor(t, results, "regular").Status; got != beckn.StatusAccepted {
		t.Errorf("the regular catalog came back %q, want ACCEPTED", got)
	}

	refused := resultFor(t, results, "master")
	if refused.Status != beckn.StatusRejected {
		t.Errorf("the master catalog came back %q, want REJECTED", refused.Status)
	}
	if len(refused.Errors) != 1 || refused.Errors[0].Code != beckn.CodeSchemaTypeNotSupported {
		t.Fatalf("errors = %+v, want one SCH_TYPE_NOT_SUPPORTED", refused.Errors)
	}
	// The directive's REAL index. A literal `i` in a response is a placeholder
	// that shipped.
	if path := refused.Errors[0].Details.Path; path != "$.message.publishDirectives[1]" {
		t.Errorf("details.path = %q, want the directive's own index", path)
	}

	if _, err := repo.GetCatalog(t.Context(), "regular"); err != nil {
		t.Errorf("the regular catalog was not stored: %v", err)
	}
	if _, err := repo.GetCatalog(t.Context(), "master"); !errors.Is(err, domain.ErrCatalogNotFound) {
		t.Error("the master catalog was stored; A1 refuses it at intake, it does not partially handle it")
	}
}

// The other half of A1: inheritance is refused, visibly, and named at the
// resource directive that asked for it.
func TestAResourceDirectiveCarryingExtendsIsRefused(t *testing.T) {
	repo := newRepo()

	results := publishBody(t, newService(t, repo, &recordingReplicator{}), `{
		"catalogs": [{"id":"c1","resources":[{"id":"r1"}]}],
		"publishDirectives": [{"catalogId":"c1","resourceDirectives":[
			{"resourceId":"r0"},
			{"resourceId":"r1","extends":{"masterResourceId":"m1"}}
		]}]
	}`)

	if len(results) != 1 || results[0].Status != beckn.StatusRejected {
		t.Fatalf("results = %+v, want one REJECTED", results)
	}
	if results[0].Errors[0].Code != beckn.CodeSchemaTypeNotSupported {
		t.Errorf("code = %q, want SCH_TYPE_NOT_SUPPORTED", results[0].Errors[0].Code)
	}
	if path := results[0].Errors[0].Details.Path; path != "$.message.publishDirectives[0].resourceDirectives[1]" {
		t.Errorf("details.path = %q, want the offending resource directive", path)
	}
	if _, err := repo.GetCatalog(t.Context(), "c1"); !errors.Is(err, domain.ErrCatalogNotFound) {
		t.Error("a catalog whose inheritance was refused was stored anyway")
	}
}

// One request carrying the same catalog id twice.
//
// Without the check both come back ACCEPTED and the stored catalog is the
// SECOND — so one of the two success verdicts describes a document that no
// longer exists. The pin is on what is stored, because two ACCEPTEDs is exactly
// what the bug looks like from outside.
func TestTheSameCatalogIDTwiceInOneRequestIsRefused(t *testing.T) {
	repo := newRepo()

	results := publishBody(t, newService(t, repo, &recordingReplicator{}), `{
		"catalogs": [
			{"id":"c1","provider":{"id":"first"}},
			{"id":"c1","provider":{"id":"second"}}
		]
	}`)

	if len(results) != 2 {
		t.Fatalf("results = %d, want one per entry", len(results))
	}
	if results[0].Status != beckn.StatusAccepted {
		t.Errorf("the first entry came back %q, want ACCEPTED", results[0].Status)
	}
	if results[1].Status != beckn.StatusRejected {
		t.Errorf("the second entry came back %q, want REJECTED", results[1].Status)
	}
	if code := results[1].Errors[0].Code; code != beckn.CodeSchemaValidationFailed {
		t.Errorf("code = %q, want SCH_SCHEMA_VALIDATION_FAILED", code)
	}
	if path := results[1].Errors[0].Details.Path; path != "$.message.catalogs[1]" {
		t.Errorf("details.path = %q, want the duplicate entry's own index", path)
	}

	stored, err := repo.GetCatalog(t.Context(), "c1")
	if err != nil {
		t.Fatalf("nothing stored: %v", err)
	}
	if !strings.Contains(string(stored.Provider), "first") {
		t.Errorf("stored provider = %s, want the FIRST entry's — the one that was ACCEPTED", stored.Provider)
	}
}

// A fatal mapping fault stores NOTHING.
//
// Asserted by looking in the store afterwards rather than at the verdict: a
// service that returned REJECTED and wrote anyway would pass an
// assertion on the response alone.
func TestAValidationFailureStoresNothing(t *testing.T) {
	repo := newRepo()

	results := publishBody(t, newService(t, repo, &recordingReplicator{}), `{
		"catalogs": [{"id":"c1","resources":[{"id":"good"},{"descriptor":{}}]}]
	}`)

	if len(results) != 1 || results[0].Status != beckn.StatusRejected {
		t.Fatalf("results = %+v, want one REJECTED", results)
	}
	if code := results[0].Errors[0].Code; !strings.HasPrefix(string(code), "SCH_") {
		t.Errorf("code = %q, want a SCH_ code", code)
	}
	if _, err := repo.GetCatalog(t.Context(), "c1"); !errors.Is(err, domain.ErrCatalogNotFound) {
		t.Error("a rejected catalog was stored")
	}
}

// A9, field-wise, and the one that is a data-loss bug if it goes the other way.
//
// applyDirectiveDefaults is the only thing standing between an omitted
// publishDirectives and a republish under FULL that deletes every resource the
// payload did not mention.
func TestADirectiveLessCatalogIsMergedNotReplaced(t *testing.T) {
	repo := newRepo()

	results := publishBody(t, newService(t, repo, &recordingReplicator{}),
		`{"catalogs": [{"id":"c1"}]}`)

	if len(results) != 1 || results[0].Status != beckn.StatusAccepted {
		t.Fatalf("results = %+v, want one ACCEPTED", results)
	}
	if len(repo.modes) != 1 || repo.modes[0] != domain.UpdateModeMerge {
		t.Errorf("mode = %v, want MERGE — FULL would delete what the payload omitted", repo.modes)
	}
	if got := repo.patches[0].VisibleTo; len(got) != 1 || got[0] != network {
		t.Errorf("VisibleTo = %v, want the request's own network (C8)", got)
	}
}

// A directive naming ONLY catalogId must come out the same as no directive at
// all — the publisher meant the same thing by both, so the defaults are
// resolved field-wise rather than all-or-nothing.
func TestAPartialDirectiveIsFilledFieldWise(t *testing.T) {
	repo := newRepo()

	publishBody(t, newService(t, repo, &recordingReplicator{}), `{
		"catalogs": [{"id":"c1"}],
		"publishDirectives": [{"catalogId":"c1"}]
	}`)

	if len(repo.modes) != 1 || repo.modes[0] != domain.UpdateModeMerge {
		t.Errorf("mode = %v, want MERGE", repo.modes)
	}
	if got := repo.patches[0].VisibleTo; len(got) != 1 || got[0] != network {
		t.Errorf("VisibleTo = %v, want the defaulted single network", got)
	}
}

// A7, and the reason the call sits after UpsertCatalog returns rather than
// inside the closure: a fan-out that runs before commit announces a catalog
// that then rolls back, and no response anywhere shows it.
func TestARolledBackTransactionDoesNotAnnounceTheCatalog(t *testing.T) {
	repo, replicator := newRepo(), &recordingReplicator{}
	repo.err = errors.New("the transaction rolled back")

	results := publishBody(t, newService(t, repo, replicator), `{"catalogs": [{"id":"c1"}]}`)

	if len(results) != 1 || results[0].Status != beckn.StatusRejected {
		t.Fatalf("results = %+v, want one REJECTED", results)
	}
	if calls := replicator.calls(); len(calls) != 0 {
		t.Errorf("announced %v after a write that did not commit", calls)
	}
}

// The positive half, without which the test above passes against a service that
// never replicates at all.
func TestACommittedCatalogIsAnnouncedOnce(t *testing.T) {
	replicator := &recordingReplicator{}

	publishBody(t, newService(t, newRepo(), replicator), `{"catalogs": [{"id":"c1"}]}`)

	if calls := replicator.calls(); len(calls) != 1 || calls[0] != "c1" {
		t.Errorf("announced %v, want exactly [c1]", calls)
	}
}

// A failed announcement does not change the verdict. The catalog is stored;
// re-reporting it as rejected would ask the publisher to send it again.
func TestAFailedAnnouncementDoesNotChangeTheVerdict(t *testing.T) {
	repo := newRepo()
	replicator := &recordingReplicator{err: errors.New("the second store is down")}

	results := publishBody(t, newService(t, repo, replicator), `{"catalogs": [{"id":"c1"}]}`)

	if len(results) != 1 || results[0].Status != beckn.StatusAccepted {
		t.Fatalf("results = %+v, want one ACCEPTED", results)
	}
	if _, err := repo.GetCatalog(t.Context(), "c1"); err != nil {
		t.Errorf("the catalog is not stored: %v", err)
	}
}

// C5 and C12: the counts are REQUEST-scoped.
//
// itemCount counts what THIS request landed, not what the catalog now holds —
// a MERGE carrying one resource into a forty-resource catalog reports 1. Read
// back from the row set instead, a re-publish of one resource would report 40.
func TestTheStatsCountWhatThisRequestLanded(t *testing.T) {
	service := newService(t, newRepo(), &recordingReplicator{})

	first := publishBody(t, service, `{"catalogs":[{"id":"c1","resources":[
		{"id":"r1","resourceAttributes":{"@type":"SeedLot"}},
		{"id":"r2","resourceAttributes":{"@type":"SeedLot"}},
		{"id":"r3","resourceAttributes":{"@type":"Fertiliser"}}
	]}]}`)

	stats := first[0].Stats
	if stats == nil {
		t.Fatal("no stats on an ACCEPTED catalog")
	}
	if stats.ItemCount != 3 {
		t.Errorf("itemCount = %d, want 3", stats.ItemCount)
	}
	if stats.ProviderCount != 1 {
		t.Errorf("providerCount = %d, want 1 — a catalog has exactly one provider", stats.ProviderCount)
	}
	// Distinct @type, because the spec has no category field anywhere (C5).
	if stats.CategoryCount != 2 {
		t.Errorf("categoryCount = %d, want 2 distinct @type values", stats.CategoryCount)
	}

	second := publishBody(t, service,
		`{"catalogs":[{"id":"c1","resources":[{"id":"r1","resourceAttributes":{"@type":"SeedLot"}}]}]}`)

	if got := second[0].Stats.ItemCount; got != 1 {
		t.Errorf("itemCount = %d after a one-resource MERGE, want 1 — the catalog now holds 3", got)
	}
}

// A geometry that cannot be read costs one geometry, not the catalog — and the
// verdict says so. ACCEPTED with a non-empty errors array tells a publisher
// whose tooling branches on the field the spec made an enum the opposite of
// what happened.
func TestAnUnreadableGeometryLandsTheCatalogAsPartial(t *testing.T) {
	repo := newRepo()

	results := publishBody(t, newService(t, repo, &recordingReplicator{}), `{"catalogs":[{"id":"c1",
		"provider":{"id":"p1","availableAt":[{"geo":{"type":"Point","coordinates":[]}}]},
		"resources":[{"id":"r1"}]}]}`)

	if len(results) != 1 || results[0].Status != beckn.StatusPartial {
		t.Fatalf("results = %+v, want one PARTIAL", results)
	}
	if len(results[0].Errors) != 1 {
		t.Fatalf("errors = %+v, want the one geometry that could not be read", results[0].Errors)
	}
	if results[0].Stats == nil || results[0].Stats.ItemCount != 1 {
		t.Errorf("stats = %+v, want the resource that landed counted", results[0].Stats)
	}
	// The path is rebased onto the request, so a publisher can find the value.
	if path := results[0].Errors[0].Details.Path; !strings.HasPrefix(path, "$.message.catalogs[0]") {
		t.Errorf("details.path = %q, want it rooted at the request body", path)
	}
	if _, err := repo.GetCatalog(t.Context(), "c1"); err != nil {
		t.Errorf("a PARTIAL catalog was not stored: %v", err)
	}
}

// derive runs on the MERGE RESULT, inside the transaction.
//
// Asserted on a field only the STORED document has: the second publish patches
// attributes and never mentions the descriptor, so a derivation reading the
// patch would find no name at all.
func TestDeriveRunsAgainstTheMergedDocument(t *testing.T) {
	repo := newRepo()
	service := newService(t, repo, &recordingReplicator{})

	publishBody(t, service, `{"catalogs":[{"id":"c1","resources":[
		{"id":"r1","descriptor":{"name":"Alphonso mangoes"},
		 "resourceAttributes":{"@context":"https://beckn.org/Agri","@type":"SeedLot","grade":"A"}}
	]}]}`)
	publishBody(t, service,
		`{"catalogs":[{"id":"c1","resources":[{"id":"r1","resourceAttributes":{"grade":"B"}}]}]}`)

	stored, err := repo.GetCatalog(t.Context(), "c1")
	if err != nil {
		t.Fatalf("nothing stored: %v", err)
	}
	if len(stored.Resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(stored.Resources))
	}

	resource := stored.Resources[0]
	if resource.Name != "Alphonso mangoes" {
		t.Errorf("Name = %q — derive ran against the patch rather than the merge result", resource.Name)
	}
	// C4's two filter columns. Nothing else in the service writes them, so a
	// discover filtering on schemaContext matches nothing without this.
	if resource.SchemaContext != "https://beckn.org/Agri" || resource.SchemaType != "SeedLot" {
		t.Errorf("schema columns = %q / %q, want them read off the merged attributes",
			resource.SchemaContext, resource.SchemaType)
	}
	if !strings.Contains(resource.SearchText, "Alphonso mangoes") {
		t.Errorf("SearchText = %q, want it derived from the merged document", resource.SearchText)
	}
	// A5: the hash records what the derived text currently is, whether or not a
	// vector was produced. Written only alongside a vector, every Phase 1 row
	// would be NULL and the Phase 2 backfill could not tell stale from missing.
	if len(resource.EmbeddingSourceHash) == 0 {
		t.Error("EmbeddingSourceHash is empty; the noop provider must still record what was derived")
	}
}

// C6's publish half: an omitted networkId falls back to APP_NETWORK_ID, and a
// supplied one wins. Only visibleTo reads it, which is the whole of C8.
func TestTheEnvelopeNetworkWinsOverTheConfiguredOne(t *testing.T) {
	repo := newRepo()
	service := newService(t, repo, &recordingReplicator{})

	var action beckn.CatalogPublishAction
	if err := json.Unmarshal([]byte(`{"catalogs":[{"id":"c1"}]}`), &action); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	service.Publish(t.Context(), beckn.Context{NetworkID: "bharatvistar"}, action)

	if got := repo.patches[0].VisibleTo; len(got) != 1 || got[0] != "bharatvistar" {
		t.Errorf("VisibleTo = %v, want the envelope's network", got)
	}
}

// The other rebase. The mapper walks ONE catalog and paths its faults relative
// to it — `$['resources'][1]['id']` — while the geometry walker paths its own
// from the catalogs array. Both are correct where they are produced and neither
// is a path a publisher can run against the body they sent.
//
// Pinned separately from the geometry case because the two take different
// branches, and the geometry test passes against a service that leaves a
// mapper fault's path untouched.
func TestAMapperFaultIsRebasedOntoTheRequestToo(t *testing.T) {
	results := publishBody(t, newService(t, newRepo(), &recordingReplicator{}), `{
		"catalogs": [{"id":"c0"},{"id":"c1","resources":[{"id":"good"},{"descriptor":{}}]}]
	}`)

	refused := resultFor(t, results, "c1")
	if len(refused.Errors) != 1 {
		t.Fatalf("errors = %+v, want the one unnamed resource", refused.Errors)
	}
	if path := refused.Errors[0].Details.Path; path != "$.message.catalogs[1].resources[1].id" {
		t.Errorf("details.path = %q, want it rooted at the request and naming the catalog's own slot", path)
	}
}

// derive replaces the catalog's covers, it does not add to them.
//
// It runs on the MERGED document, which under MERGE already carries whatever the
// LAST publish derived — so appending doubles every geometry at each republish,
// and the symptom is a spatial query returning the same resource N times after
// the Nth publish rather than an error anyone would notice.
func TestARepublishDoesNotDoubleTheGeometries(t *testing.T) {
	repo := newRepo()
	service := newService(t, repo, &recordingReplicator{})

	body := `{"catalogs":[{"id":"c1",
		"provider":{"id":"p1","availableAt":[{"geo":{"type":"Point","coordinates":[77.6,12.9]}}]},
		"resources":[{"id":"r1","descriptor":{"geo":{"type":"Point","coordinates":[77.7,12.8]}}}]}]}`

	for round := 1; round <= 3; round++ {
		publishBody(t, service, body)

		stored, err := repo.GetCatalog(t.Context(), "c1")
		if err != nil {
			t.Fatalf("round %d: nothing stored: %v", round, err)
		}
		if len(stored.Geometries) != 1 {
			t.Fatalf("round %d: catalog geometries = %d, want 1", round, len(stored.Geometries))
		}
		if len(stored.Resources) != 1 || len(stored.Resources[0].Geometries) != 1 {
			t.Fatalf("round %d: resource geometries = %+v, want exactly one",
				round, stored.Resources[0].Geometries)
		}
	}
}
