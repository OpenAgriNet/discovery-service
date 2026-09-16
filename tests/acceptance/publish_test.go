package acceptance

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// Scenario 1. A catalog lands and is immediately discoverable.
//
// The discover half is the half that matters. A publish answering ACCEPTED
// proves the transaction committed; it does not prove the geometry cover was
// written inside it, and a cover written by a later job — or not at all —
// leaves a catalog that exists and cannot be found. Searching for it in the
// same test is what says the indexing happened inside the write.
func TestPublishNewCatalog(t *testing.T) {
	svc := newService(t)

	results := svc.publishCatalogs(t,
		aCatalog("c-new",
			availableAt(majestic),
			resources(aResource("r-tomato", "Tomatoes"))))

	if len(results) != 1 {
		t.Fatalf("publish returned %d verdicts, want 1: %+v", len(results), results)
	}
	if results[0].Status != beckn.StatusAccepted {
		t.Fatalf("verdict = %s %+v, want ACCEPTED", results[0].Status, results[0].Errors)
	}

	found := svc.discover(t, spatial(dwithin(providerGeoPath, majestic, 5000)))
	if got := resourceIDs(found); len(got) != 1 || got[0] != "r-tomato" {
		t.Errorf("discover returned %v, want [r-tomato]", got)
	}
}

// Scenario 2. A8, end to end and at field level.
//
// The three attribute members are the whole point: one the patch never mentions
// must survive, one it sets must change, and one it nulls must go. A merge that
// replaced the document would pass an assertion on `moisture` alone, and a merge
// that ignored nulls would pass an assertion on `grade` alone.
//
// The descriptor is asserted against the document read back BEFORE the patch
// rather than against the fixture, because both readings have been through JSONB
// and only their equality to each other says the patch left it alone.
func TestUpdateExistingCatalogMerges(t *testing.T) {
	svc := newService(t)

	svc.publishCatalogs(t, aCatalog("c-merge",
		availableAt(majestic),
		resources(
			aResource("r-tomato", "Tomatoes", withAttributes(map[string]any{
				"grade": "A", "moisture": 12, "origin": "Nashik",
			})),
			aResource("r-onion", "Onions", withAttributes(map[string]any{"grade": "B"})))))

	before := svc.near(t, majestic, 5000)
	tomatoBefore := findResource(t, before, "r-tomato")
	onionBefore := findResource(t, before, "r-onion")

	// One resource of two, carrying two of its three attributes and no
	// descriptor at all. Everything this payload does not name is what the
	// assertions below are about.
	results := svc.publishCatalogs(t, aCatalog("c-merge",
		resources(aResource("r-tomato", "",
			withAttributes(map[string]any{"moisture": 14, "origin": nil})))))
	if results[0].Status != beckn.StatusAccepted {
		t.Fatalf("republish = %s %+v, want ACCEPTED", results[0].Status, results[0].Errors)
	}

	after := svc.near(t, majestic, 5000)
	tomato := findResource(t, after, "r-tomato")

	assertJSON(t, tomato.ResourceAttributes,
		jsonLD(map[string]any{"grade": "A", "moisture": 14}), "r-tomato resourceAttributes")

	if !bytes.Equal(tomato.Descriptor, tomatoBefore.Descriptor) {
		t.Errorf("the patch changed a descriptor it never mentioned: %s, was %s",
			tomato.Descriptor, tomatoBefore.Descriptor)
	}

	// The other resource of the two. A merge that rewrote the catalog's resource
	// set rather than patching one member would empty this.
	onion := findResource(t, after, "r-onion")
	if !bytes.Equal(onion.ResourceAttributes, onionBefore.ResourceAttributes) ||
		!bytes.Equal(onion.Descriptor, onionBefore.Descriptor) {
		t.Errorf("republishing r-tomato disturbed r-onion: %s / %s, was %s / %s",
			onion.Descriptor, onion.ResourceAttributes,
			onionBefore.Descriptor, onionBefore.ResourceAttributes)
	}
}

// Scenario 3. The dangerous half of the same feature.
//
// Two halves, because FULL differs from MERGE in two ways and a mis-wired
// directive could pass either one alone: it deletes the resources the payload
// omits, and it resets the catalog ROW — the validity a MERGE would have kept.
//
// The validity half is phrased as a window that has already closed, so the
// difference between kept and cleared is observable through discover: a catalog
// whose endDate is in the past is not returned, and one whose four validity
// columns are NULL is.
func TestFullUpdateReplacesTheCatalog(t *testing.T) {
	svc := newService(t)

	svc.publishCatalogs(t, aCatalog("c-full",
		availableAt(majestic),
		resources(aResource("r-keep", "Keep"), aResource("r-drop", "Drop"))))

	svc.publishWith(t,
		[]any{aCatalog("c-full", availableAt(majestic), resources(aResource("r-keep", "Keep")))},
		directive("c-full", updateMode(beckn.UpdateModeFull)))

	if got := resourceIDs(svc.near(t, majestic, 5000)); !slices.Equal(got, []string{"r-keep"}) {
		t.Errorf("after a FULL republish the catalog holds %v, want [r-keep]", got)
	}

	// The second half. A closed window first, so that "returned" is the change
	// the reset produces rather than the state it started in.
	closed := map[string]any{
		"startDate": time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339),
		"endDate":   time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339),
	}
	svc.publishCatalogs(t, aCatalog("c-clock",
		availableAt(koramangala), withValidity(closed),
		resources(aResource("r-clock", "Clock"))))

	if got := resourceIDs(svc.near(t, koramangala, 1000)); len(got) != 0 {
		t.Fatalf("a catalog whose window closed yesterday returned %v, want none", got)
	}

	// MERGE, naming no validity: the stored window stands.
	svc.publishCatalogs(t, aCatalog("c-clock", resources(aResource("r-clock", "Clock"))))
	if got := resourceIDs(svc.near(t, koramangala, 1000)); len(got) != 0 {
		t.Errorf("a MERGE with no validity returned %v, want none: it must keep the window", got)
	}

	// FULL, naming no validity: the row is replaced, so all four columns clear.
	svc.publishWith(t,
		[]any{aCatalog("c-clock", availableAt(koramangala), resources(aResource("r-clock", "Clock")))},
		directive("c-clock", updateMode(beckn.UpdateModeFull)))
	if got := resourceIDs(svc.near(t, koramangala, 1000)); !slices.Equal(got, []string{"r-clock"}) {
		t.Errorf("after a FULL republish with no validity the catalog returned %v, want [r-clock]", got)
	}
}

// Scenario 4. An invalid payload is refused and stores nothing — twice, because
// the two spellings are refused by two different layers and the plan asks for
// both.
//
// The absent key is L1's: `Resource` declares `required: [id]`, so the schema
// validator refuses the whole request with a 400 before any handler runs. The
// EMPTY string is not L1's at all — the schema requires the key and says nothing
// about its length — so it reaches the mapper, which refuses that catalog and
// stores nothing for it. Asserting only the first would leave the one value
// `uq_resource_geometries` cannot key admitted by a presence check.
func TestInvalidPayloadIsRejected(t *testing.T) {
	svc := newService(t)

	t.Run("the id key is absent", func(t *testing.T) {
		answer := svc.post(t, "/publish", envelope(beckn.ActionPublish, map[string]any{
			"catalogs": []any{aCatalog("c-invalid",
				availableAt(majestic),
				resources(map[string]any{"descriptor": map[string]any{"name": "No id"}}))},
		}))

		if answer.status != http.StatusBadRequest {
			t.Fatalf("POST /publish = %d, want 400\nbody: %s", answer.status, answer.body)
		}
		refusal := answer.nack(t).Message.Error
		if !strings.HasPrefix(string(refusal.Code), "SCH_") {
			t.Errorf("code is %s, want a SCH_ code", refusal.Code)
		}

		// The plan calls this a JSON pointer; what the service emits is the
		// dotted path `$.message.catalogs[0].resources[0].id`, which is the one
		// spelling every layer of this repository already uses — the faults the
		// mapper raises, the paths the geometry walker stores. Asserted as it
		// is rather than as the plan words it, because a second spelling on the
		// wire would be worse than the plan being loose.
		const want = "$.message.catalogs[0].resources[0].id"
		if refusal.Details == nil || refusal.Details.Path != want {
			t.Errorf("details.path is %+v, want %s", refusal.Details, want)
		}
	})

	t.Run("the id is the empty string", func(t *testing.T) {
		results := svc.publishCatalogs(t, aCatalog("c-empty-id",
			availableAt(majestic),
			resources(map[string]any{"id": "", "descriptor": map[string]any{"name": "Empty id"}})))

		if results[0].Status != beckn.StatusRejected {
			t.Fatalf("verdict = %s, want REJECTED", results[0].Status)
		}
		if got := results[0].Errors[0].Code; got != beckn.CodeSchemaValidationFailed {
			t.Errorf("code is %s, want %s", got, beckn.CodeSchemaValidationFailed)
		}
		if got := results[0].Errors[0].Details; got == nil || !strings.Contains(got.Path, "resources") {
			t.Errorf("details.path is %+v, want a pointer into resources", got)
		}
	})

	// Neither spelling stored anything, and the catalog row is the thing to
	// check: a mapper that refused the resource and wrote the catalog anyway
	// would leave a discoverable catalog with no resources in it.
	if got := resourceIDs(svc.near(t, majestic, 5000)); len(got) != 0 {
		t.Errorf("a refused publish stored %v", got)
	}
}

// Scenario 5. A1: master catalogs and resource inheritance are refused, visibly.
//
// Both arms in one scenario because they are one decision — Phase 1 accepts
// regular resources only — and both are refused at intake rather than half
// applied. The assertion that nothing is stored is what separates "refused" from
// "accepted and ignored", which is the failure a REJECTED verdict alone cannot
// rule out.
func TestMasterCatalogAndInheritanceAreRefused(t *testing.T) {
	svc := newService(t)

	master := svc.publishWith(t,
		[]any{aCatalog("c-master", availableAt(majestic), resources(aResource("r-m", "Master")))},
		directive("c-master", catalogType(beckn.CatalogTypeMaster)))
	assertRejected(t, master[0], beckn.CodeSchemaTypeNotSupported)

	child := svc.publishWith(t,
		[]any{aCatalog("c-child", availableAt(majestic), resources(aResource("r-c", "Child")))},
		directive("c-child", extendsMaster("r-c", "r-m")))
	assertRejected(t, child[0], beckn.CodeSchemaTypeNotSupported)

	if got := resourceIDs(svc.near(t, majestic, 5000)); len(got) != 0 {
		t.Errorf("a refused publish stored %v", got)
	}
}

// Scenario 6. One request, two catalogs, two verdicts.
//
// The per-catalog transaction boundary: a refusal is that catalog's, and the
// catalog beside it in the same array lands. Without the boundary the request is
// all-or-nothing, and a publisher batching a hundred catalogs loses ninety-nine
// of them to one typo.
func TestARejectedMasterDoesNotBlockTheRegularCatalogsBesideIt(t *testing.T) {
	svc := newService(t)

	results := svc.publishWith(t,
		[]any{
			aCatalog("c-master", availableAt(majestic), resources(aResource("r-m", "Master"))),
			aCatalog("c-regular", availableAt(majestic), resources(aResource("r-r", "Regular"))),
		},
		directive("c-master", catalogType(beckn.CatalogTypeMaster)))

	if len(results) != 2 {
		t.Fatalf("publish returned %d verdicts, want 2: %+v", len(results), results)
	}
	assertRejected(t, results[0], beckn.CodeSchemaTypeNotSupported)
	if results[1].Status != beckn.StatusAccepted {
		t.Fatalf("the regular catalog beside it = %s %+v, want ACCEPTED",
			results[1].Status, results[1].Errors)
	}

	if got := resourceIDs(svc.near(t, majestic, 5000)); !slices.Equal(got, []string{"r-r"}) {
		t.Errorf("the store holds %v, want [r-r] alone", got)
	}
}

// Scenario 6a. The same catalog id twice in one request.
//
// The pin is on the STORED document rather than on the status array, because
// two ACCEPTEDs is exactly what the bug looks like from outside: left unchecked
// both entries are applied, the second wins, and one of the two success verdicts
// describes a document that no longer exists. So the assertion is on a field the
// two entries disagree about — the resource each carries — and it says the FIRST
// one is what survived.
func TestTheSameCatalogIdTwiceInOneRequestIsRefused(t *testing.T) {
	svc := newService(t)

	results := svc.publishCatalogs(t,
		aCatalog("c-twice", availableAt(majestic), resources(aResource("r-first", "First"))),
		aCatalog("c-twice", availableAt(majestic), resources(aResource("r-second", "Second"))))

	if len(results) != 2 {
		t.Fatalf("publish returned %d verdicts, want 2: %+v", len(results), results)
	}
	if results[0].Status != beckn.StatusAccepted {
		t.Fatalf("the first entry = %s %+v, want ACCEPTED", results[0].Status, results[0].Errors)
	}
	assertRejected(t, results[1], beckn.CodeSchemaValidationFailed)

	if got := resourceIDs(svc.near(t, majestic, 5000)); !slices.Equal(got, []string{"r-first"}) {
		t.Errorf("the store holds %v, want [r-first]: the FIRST entry is the one that lands", got)
	}
}

// assertRejected fails with the verdict's own errors, because "want REJECTED,
// got ACCEPTED" sends the reader to the payload to find out what the service
// thought was acceptable about it.
func assertRejected(t *testing.T, result beckn.CatalogProcessingResult, code beckn.ErrorCode) {
	t.Helper()

	if result.Status != beckn.StatusRejected {
		t.Fatalf("%s = %s %+v, want REJECTED", result.CatalogID, result.Status, result.Errors)
	}
	if len(result.Errors) == 0 || result.Errors[0].Code != code {
		t.Errorf("%s was rejected with %+v, want %s", result.CatalogID, result.Errors, code)
	}
}

// Scenario 7. The flag that has nothing behind it.
//
// This replaces the original pair — MissingSignatureIsUnauthorized and
// UnsignedRequestSucceedsWhenVerificationIsOff — which asserted both sides of a
// switch that now has nothing on either side. What made the deferral honest was
// never the flag but the impossibility of believing it was on when it wasn't,
// and with the crypto parked a boot refusal is the only thing that still
// carries that.
//
// Run against the BINARY rather than against config.load, which the config
// package already covers. The question here is different and only the binary
// answers it: that the refusal is on the path a container actually takes, and
// that the process exits non-zero rather than logging a complaint and going on
// to serve. No database is reached — the refusal happens while the
// configuration is being read, which is why the DSN below can name a port
// nothing is listening on and the scenario still means what it says.
func TestSignatureVerificationRefusesToBoot(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "discovery-service")

	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/discovery-service")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the binary: %v\n%s", err, out)
	}

	boot := exec.CommandContext(t.Context(), binary)
	// config/common.yaml is read relative to the working directory, so the
	// binary has to be started where a container starts it.
	boot.Dir = repoRoot
	// Appended AFTER the inherited environment, because a later entry wins:
	// a developer with DATABASE_URL exported must not change what this asserts.
	// The other two are here so the flag is the ONLY thing wrong — a scenario
	// that also omitted the network id would pass against a service that had
	// never heard of the flag.
	boot.Env = append(os.Environ(),
		"AUTH_ENABLE_SIGNATURE_VERIFICATION=true",
		"APP_NETWORK_ID="+network,
		"DATABASE_URL=postgres://unused:unused@127.0.0.1:1/unused",
	)

	output, err := boot.CombinedOutput()
	if err == nil {
		t.Fatalf("the binary started with signature verification on\n%s", output)
	}
	if !strings.Contains(string(output), "AUTH_ENABLE_SIGNATURE_VERIFICATION") {
		t.Errorf("the boot failure does not name the flag:\n%s", output)
	}
}

// repoRoot is where config/common.yaml and cmd/ are, from this package.
const repoRoot = "../.."

// Scenario 8. The other setting of the same flag, and the only one Phase 1
// supports: an unsigned request is processed normally.
//
// It looks like scenario 1 and is not. Scenario 1 asks whether a publish is
// indexed; this one asks whether anything in the chain has quietly started
// requiring a credential — a middleware added above the controller, a header
// check inside one. Neither would be visible to any other test here, because
// every other scenario sends the same unsigned request and would fail for
// reasons it would then be read as being about.
func TestUnsignedRequestSucceeds(t *testing.T) {
	defaults, err := config.Defaults()
	if err != nil {
		t.Fatalf("read the configuration defaults: %v", err)
	}
	if defaults.Auth.EnableSignatureVerification {
		t.Fatal("AUTH_ENABLE_SIGNATURE_VERIFICATION defaults to true, and scenario 7 says nothing boots with it on")
	}

	svc := newService(t)

	results := svc.publishCatalogs(t,
		aCatalog("c-unsigned", availableAt(majestic), resources(aResource("r-unsigned", "Unsigned"))))
	if results[0].Status != beckn.StatusAccepted {
		t.Fatalf("an unsigned publish = %s %+v, want ACCEPTED", results[0].Status, results[0].Errors)
	}

	if got := resourceIDs(svc.near(t, majestic, 5000)); !slices.Equal(got, []string{"r-unsigned"}) {
		t.Errorf("discover returned %v, want [r-unsigned]", got)
	}
}

// Scenario 8a. C14, end to end.
//
// The ceiling is set low for the scenario rather than the body grown to ten
// mebibytes: the number under test is SERVER_MAX_REQUEST_BODY_BYTES, not any
// particular count of bytes, and a fixture that sent the production default
// would spend a second of every run proving the same thing.
//
// It sits beside scenario 9 for the same reason 9 exists. Envelope's own unit
// tests already cover the ceiling; what they cannot cover is that it is
// MOUNTED, and a re-wiring of the chain that dropped it would leave every one
// of them green.
func TestAnOversizedBodyIsRefusedWithA413(t *testing.T) {
	const ceiling = 2048

	svc := newService(t, func(cfg *config.Config) { cfg.Server.MaxRequestBodyBytes = ceiling })

	// Comfortably over the ceiling and otherwise a perfectly good publish, so
	// the refusal cannot be about anything else in it. The padding rides on a
	// descriptor name because that is a string field the schema does not bound.
	oversized := envelope(beckn.ActionPublish, map[string]any{"catalogs": []any{
		aCatalog("c-oversized",
			availableAt(majestic),
			resources(aResource("r-oversized", strings.Repeat("padding ", ceiling/4)))),
	}})
	encoded, err := json.Marshal(oversized)
	if err != nil {
		t.Fatalf("encode the oversized request: %v", err)
	}
	if len(encoded) <= ceiling {
		t.Fatalf("the fixture is %d bytes, which is under the %d-byte ceiling it is meant to breach",
			len(encoded), ceiling)
	}

	answer := svc.postRaw(t, "/publish", encoded)
	if answer.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /publish = %d, want 413\nbody: %s", answer.status, answer.body)
	}
	if got := answer.nack(t).Message.Error.Code; got != beckn.CodePolicyNPCapacityExceeded {
		t.Errorf("code = %q, want %q", got, beckn.CodePolicyNPCapacityExceeded)
	}

	// Nothing stored, and the service still serving. The second half is what
	// separates a refusal from a crash: a ceiling enforced by falling over
	// would satisfy every assertion above it.
	if got := resourceIDs(svc.near(t, majestic, 5000)); len(got) != 0 {
		t.Errorf("the refused publish stored %v, want nothing", got)
	}

	results := svc.publishCatalogs(t,
		aCatalog("c-after", availableAt(majestic), resources(aResource("r-after", "After"))))
	if results[0].Status != beckn.StatusAccepted {
		t.Fatalf("the publish after the refusal = %s %+v, want ACCEPTED", results[0].Status, results[0].Errors)
	}
}

// Scenario 9. Burst+1 requests, and the last one is refused.
//
// The limiter keys on the peer host and every request in this suite comes from
// 127.0.0.1, so the burst has to be small enough that this scenario can exhaust
// it on purpose and large enough that the boot itself does not. RPS is set to 1
// as well: a refill fast enough to hand a token back between two consecutive
// requests would make the last one pass, and the scenario would report the
// limiter as unmounted.
//
// Like 8a, the point is as much that the middleware is MOUNTED as that it
// works. An unmounted limiter is invisible to every other test in this suite,
// because every other test stays far under any ceiling.
func TestACallerOverItsRateGetsA429(t *testing.T) {
	const burst = 3

	svc := newService(t, func(cfg *config.Config) {
		cfg.RateLimit.RPS = 1
		cfg.RateLimit.Burst = burst
	})

	intent := text("nothing in particular")
	for i := range burst {
		if answer := svc.discoverResponse(t, intent); answer.status != http.StatusOK {
			t.Fatalf("request %d of the burst = %d, want 200\nbody: %s", i+1, answer.status, answer.body)
		}
	}

	refused := svc.discoverResponse(t, intent)
	if refused.status != http.StatusTooManyRequests {
		t.Fatalf("request %d = %d, want 429\nbody: %s", burst+1, refused.status, refused.body)
	}
	if got := refused.nack(t).Message.Error.Code; got != beckn.CodeAuthRateLimited {
		t.Errorf("code = %q, want %q", got, beckn.CodeAuthRateLimited)
	}
	// Not optional. A 429 with no interval leaves a caller to guess, and every
	// caller guesses the same wrong thing at the same moment.
	if refused.header.Get("Retry-After") == "" {
		t.Error("the 429 carries no Retry-After")
	}
}

// Scenario 10. A visibility change carrying no resources at all.
//
// The gate lives on `resources`, not on `catalogs` — discover never joins the
// catalog row on the scoped path (that is what scenario 25's latency depends
// on). So a publish that narrows visibleTo has to propagate the new gate onto
// every resource row, unconditionally, even when the payload names none of
// them. Without that the catalog row changes, discover goes on reading the old
// gate off the resources, and a visibility change reports success and does
// nothing.
//
// Asserted in both directions: gone from the network it left, present on the
// one it moved to. One direction alone would also pass against a publish that
// deleted the resources outright.
func TestChangingVisibleToWithNoResourcesInThePayloadTakesEffect(t *testing.T) {
	svc := newService(t)

	svc.publishCatalogs(t, aCatalog("c-gate",
		availableAt(majestic),
		resources(aResource("r-gated", "Gated"))))

	if got := resourceIDs(svc.discoverOn(t, network, spatial(dwithin(providerGeoPath, majestic, 5000)))); !slices.Equal(got, []string{"r-gated"}) {
		t.Fatalf("before the change %s sees %v, want [r-gated]", network, got)
	}

	// The catalog document again, carrying no resources, and a directive that
	// moves it to another network.
	//
	// An EMPTY resources array rather than an absent key, because the schema
	// puts Catalog's `resources` and `offers` in an anyOf and a document with
	// neither is a 400 before the service sees it. It is "no resources" in the
	// only sense this scenario is about: the mapper reads an empty array
	// exactly as it reads an absent one, so nothing reaches the merge that
	// could rewrite a resource row by naming it.
	results := svc.publishWith(t,
		[]any{aCatalog("c-gate", availableAt(majestic), resources())},
		directive("c-gate", visibleTo("bharatvistar")))
	if results[0].Status != beckn.StatusAccepted {
		t.Fatalf("the visibility change = %s %+v, want ACCEPTED", results[0].Status, results[0].Errors)
	}

	if got := resourceIDs(svc.discoverOn(t, network, spatial(dwithin(providerGeoPath, majestic, 5000)))); len(got) != 0 {
		t.Errorf("after the change %s still sees %v, want nothing", network, got)
	}
	if got := resourceIDs(svc.discoverOn(t, "bharatvistar", spatial(dwithin(providerGeoPath, majestic, 5000)))); !slices.Equal(got, []string{"r-gated"}) {
		t.Errorf("after the change bharatvistar sees %v, want [r-gated]", got)
	}
}

// Scenario 10a. The other half of 10, and the one no response can show.
//
// Scenario 10's guarantee is that the gate reaches every resource row. This one
// is that it reaches them only when it CHANGED. The failure it prevents is
// invisible from outside — every value is still correct — and shows up as forty
// dead tuples and forty fastupdate=off GIN insertions per publish, paid on the
// write path and read later as a publish that got slower for no reason.
//
// xmin is the transaction that last wrote each row, so it answers "was this row
// rewritten" where a value comparison can only answer "does this row still say
// the right thing". The IS DISTINCT FROM guard on the propagate is what
// separates the two republishes below.
func TestChangingTheGateRewritesOnlyTheRowsItChanges(t *testing.T) {
	svc := newService(t)

	const count = 40
	catalog := aCatalog("c-many", availableAt(majestic), resources(manyResources(count)...))
	svc.publishCatalogs(t, catalog)

	before := dbtest.ResourceVersions(t, svc.pool, "c-many")
	if len(before) != count {
		t.Fatalf("the catalog stored %d resources, want %d", len(before), count)
	}

	// The catalog document again: no resources — an empty array, for the reason
	// scenario 10 gives — and no visibleTo either, so A9 resolves it to the same
	// single network it already had. Nothing about the gate changed, so nothing
	// should be rewritten.
	svc.publishWith(t,
		[]any{aCatalog("c-many", availableAt(majestic), resources())},
		directive("c-many"))

	unchanged := dbtest.ResourceVersions(t, svc.pool, "c-many")
	if !maps.Equal(unchanged, before) {
		t.Errorf("a republish that changed no gate rewrote %d of %d rows",
			countMoved(before, unchanged), count)
	}

	// Now narrow it. Every row carries the gate, so every row has to move.
	svc.publishWith(t,
		[]any{aCatalog("c-many", availableAt(majestic), resources())},
		directive("c-many", visibleTo("bharatvistar")))

	moved := dbtest.ResourceVersions(t, svc.pool, "c-many")
	if got := countMoved(before, moved); got != count {
		t.Errorf("narrowing visibleTo rewrote %d of %d rows, want all of them", got, count)
	}

	// And scenario 10's guarantee, restated here because 10a is the change that
	// could break it: the guard must skip rows, not skip the propagate.
	// Paged explicitly: the default page is twenty and this fixture is forty, so
	// a scenario reading the default would be asserting on SEARCH_DEFAULT_LIMIT
	// rather than on the gate.
	found := svc.discoverPaged(t, "bharatvistar", count, spatial(dwithin(providerGeoPath, majestic, 5000)))
	if got := resourceIDs(found); len(got) != count {
		t.Errorf("bharatvistar sees %d resources, want %d", len(got), count)
	}
}

// manyResources is the forty-resource fixture 10a and 15 are both phrased over.
func manyResources(count int) []map[string]any {
	list := make([]map[string]any, 0, count)
	for i := range count {
		id := "r-" + strconv.Itoa(i)
		list = append(list, aResource(id, "Resource "+strconv.Itoa(i)))
	}
	return list
}

// countMoved reports how many rows carry a different xmin than they did.
func countMoved(before, after map[string]string) int {
	moved := 0
	for id, version := range before {
		if after[id] != version {
			moved++
		}
	}
	return moved
}

// Scenario 11. A FULL republish that drops a resource, and the two offers that
// pointed at it.
//
// One offer named only the dropped resource; the other named it plus a
// survivor. The first must be DELETED and the second SHORTENED, and the
// distinction is the whole scenario: an empty resource_ids means catalog-wide,
// so an offer pruned to empty and left in place is not a no-op — it is silently
// promoted from covering one resource to covering all of them.
//
// Asserted through the response rather than through the table, and that is
// enough here: a promoted offer would be hydrated onto every resource in the
// page, which is exactly what the assertion below refuses.
func TestAFullRepublishPrunesOrphanedOffers(t *testing.T) {
	svc := newService(t)

	svc.publishCatalogs(t, aCatalog("c-prune",
		availableAt(majestic),
		resources(
			aResource("r-dropped", "Dropped"),
			aResource("r-kept", "Kept")),
		offers(
			anOffer("o-only-dropped", "r-dropped"),
			anOffer("o-both", "r-dropped", "r-kept"))))

	if got := offerIDs(svc.near(t, majestic, 5000)); len(got) != 2 {
		t.Fatalf("the first publish hydrated %v, want both offers", got)
	}

	// FULL, and the payload omits r-dropped. Under FULL that is a deletion, and
	// the deletion is what the offers have to be reconciled against.
	results := svc.publishWith(t,
		[]any{aCatalog("c-prune",
			availableAt(majestic),
			resources(aResource("r-kept", "Kept")),
			offers(
				anOffer("o-only-dropped", "r-dropped"),
				anOffer("o-both", "r-dropped", "r-kept")))},
		directive("c-prune", updateMode(beckn.UpdateModeFull)))
	// PARTIAL, not ACCEPTED, and that is the right verdict rather than a
	// tolerated one: the catalog landed, and two offers in it did not land as
	// sent. A publisher told ACCEPTED would go on believing o-only-dropped
	// exists. Both warnings are BIZ_ITEM_NOT_FOUND — one for the offer that
	// lost every id it named, one for the offer that lost one of two.
	if results[0].Status != beckn.StatusPartial {
		t.Fatalf("the FULL republish = %s %+v, want PARTIAL", results[0].Status, results[0].Errors)
	}
	assertWarned(t, results[0], beckn.CodeBusinessItemNotFound, "o-only-dropped", "o-both")

	after := svc.near(t, majestic, 5000)
	if got := resourceIDs(after); !slices.Equal(got, []string{"r-kept"}) {
		t.Fatalf("the FULL republish left %v, want [r-kept]", got)
	}
	if got := offerIDs(after); !slices.Equal(got, []string{"o-both"}) {
		t.Errorf("r-kept carries %v, want [o-both]: an offer pruned to empty is deleted, "+
			"not promoted to catalog-wide", got)
	}
}

// assertWarned checks that a PARTIAL verdict names each subject once, under the
// given code. Written over the message rather than over a structured field
// because the qualifying faults carry the offer id nowhere else, and a scenario
// asserting only the count would pass against a publish that warned twice about
// the same offer.
func assertWarned(t *testing.T, result beckn.CatalogProcessingResult, code beckn.ErrorCode, subjects ...string) {
	t.Helper()

	for _, subject := range subjects {
		found := false
		for _, fault := range result.Errors {
			if fault.Code == code && strings.Contains(fault.Message, subject) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no %s warning names %q; the verdict carries %+v", code, subject, result.Errors)
		}
	}
}
