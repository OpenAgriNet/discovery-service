// Package acceptance runs the plan's thirty-five scenarios against the
// assembled service: the real router, the real middleware chain, the real
// repositories and a real PostgreSQL, over HTTP.
//
// Only the embedder is stubbed. Everything else a request passes through on a
// deployment it passes through here, which is the only way a scenario can pin
// something the layer tests below it cannot see — a middleware that was never
// mounted, a route that answers 400 where it should answer 404, an index that
// the write path fills and the read path never consults.
//
// One file per scenario group, so the file a failure names is the section of
// the plan it came from.
package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/OpenAgriNet/discovery-service/src/app"
	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/tests/dbtest"
)

// specPath is the published beckn.yaml this suite validates against, mounted
// from the repository rather than fetched.
//
// VALIDATION_SPEC_URL is left empty beside it, which makes LoadSpecIndex return
// before it reaches the network at all. That is deliberate and not a
// convenience: a suite that fetched the spec would fail on an aeroplane, and —
// worse — would pass on a machine whose registry had quietly started serving a
// different document from the one this repository was written against.
const specPath = "../testdata/beckn-v2.0.0.yaml"

// network is the deployment's own network id (APP_NETWORK_ID), which publish
// falls back to when a directive names no visibleTo (C8).
const network = "oan"

// service is the assembled deployment plus the two handles a scenario needs
// besides its URL: the pool, for the assertions no response can make, and the
// clock-free helpers below.
type service struct {
	url  string
	pool dbtest.Pool
}

// newService boots the whole service against a migrated, empty database and
// returns it listening on a port the kernel picked.
//
// opts mutate the configuration before Build sees it, because three scenarios
// are about a knob rather than about a payload: 8a needs a low body ceiling, 9
// a low rate limit, and 25 a pool it can starve. Passing a whole config would
// make every scenario restate twelve fields it does not care about; passing the
// knob names exactly what the scenario is varying.
func newService(t *testing.T, opts ...func(*config.Config)) *service {
	t.Helper()

	pool := dbtest.NewPostgres(t)

	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("read the configuration defaults: %v", err)
	}

	cfg.App.Network = network
	cfg.Database.URL = dbtest.DSN(t)

	// The migrations already ran, in dbtest, against this same database. Boot
	// migration is a separate decision (D10) and Task 20 pins it; running it
	// here would only re-assert ErrNoChange.
	cfg.Database.AutoMigrate = false

	cfg.Validation.SpecURL = ""
	cfg.Validation.SpecCachePath = specPath

	// hashing rather than the production noop. The stub is the whole of what
	// this suite fakes, and it is faked in the direction that exercises MORE
	// code: noop would leave `semantic` undeclared, so every response would
	// carry X-Beckn-Degraded and the vector path would go unexecuted end to
	// end. What hashing cannot do is rank a paraphrase, which is why no
	// scenario here claims to test semantics.
	cfg.Embeddings.Provider = "hashing"

	// Deliberately far above the production default. Every request in this
	// suite arrives from 127.0.0.1, so the whole suite shares one bucket per
	// service; at RATE_LIMIT_BURST=40 the scenarios that publish forty
	// resources would start refusing themselves, and the failure would read as
	// a publish bug. Scenario 9 sets it back down, and it is the scenario that
	// pins the limiter is mounted at all.
	cfg.RateLimit.RPS = 100000
	cfg.RateLimit.Burst = 100000

	// warn rather than info: an acceptance run publishes tens of thousands of
	// resources, and one line each buries the failure that matters.
	cfg.Log.Level = "warn"

	for _, opt := range opts {
		opt(&cfg)
	}

	built, err := app.Build(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build the service: %v", err)
	}
	t.Cleanup(built.Close)

	server := httptest.NewServer(app.NewRouter(built))
	t.Cleanup(server.Close)

	svc := &service{url: server.URL, pool: pool}

	// The invariant the plan puts in teardown rather than in a scenario of its
	// own: no row of offers.resource_ids names a (catalog_id, resource_id) that
	// resources does not have. Registered before any scenario runs, so it runs
	// after all of them — Cleanup is a stack — and costs one query.
	t.Cleanup(func() { svc.assertNoOrphanedOfferResources(t) })

	return svc
}

func (s *service) assertNoOrphanedOfferResources(t *testing.T) {
	t.Helper()

	if orphans := dbtest.OrphanedOfferResources(t, s.pool); len(orphans) > 0 {
		t.Errorf("offers.resource_ids names resources that do not exist: %v", orphans)
	}
}

// response is one answer, read in full so a scenario can assert on the status,
// a header and the body without holding a live connection open.
type response struct {
	status int
	header http.Header
	body   []byte
}

// post sends one request to path and reads the whole answer.
//
// The body is any rather than a typed envelope because half the scenarios send
// documents no Go struct in this repository can express — a resource with no
// `id`, the same catalog id twice, an RFC 9535 filter — and a helper that could
// only send valid requests could not test the rejections.
func (s *service) post(t *testing.T, path string, body any) response {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode the request: %v", err)
	}
	return s.postRaw(t, path, encoded)
}

// postRaw is post with the bytes already encoded, for the scenario that sends
// more of them than a marshalled fixture would.
func (s *service) postRaw(t *testing.T, path string, body []byte) response {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.url+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")

	answer, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() {
		if err := answer.Body.Close(); err != nil {
			t.Errorf("close the response body: %v", err)
		}
	}()

	read := make([]byte, 0)
	buffer := bytes.NewBuffer(read)
	if _, err := buffer.ReadFrom(answer.Body); err != nil {
		t.Fatalf("read the response body: %v", err)
	}
	return response{status: answer.StatusCode, header: answer.Header, body: buffer.Bytes()}
}

// decode reads the response body into target, failing with the body itself
// rather than with a type error — an unexpected NACK decoded as a success is a
// zero value, and the message that explains it is the one already on the wire.
func (r response) decode(t *testing.T, target any) {
	t.Helper()

	if err := json.Unmarshal(r.body, target); err != nil {
		t.Fatalf("decode the response: %v\nbody: %s", err, r.body)
	}
}

// publishEnvelope wraps a message in the five context fields C6 requires:
// action, version, messageId, transactionId and timestamp. The published
// Context declares no `required` list, so these are enforced by the envelope
// rules and by nothing in the schema — which means a helper that omitted one
// would fail every scenario for the same uninformative reason.
//
// The variadic options are for the one context field a scenario varies:
// networkId, which is optional on both actions and means two different things
// by its absence — publish falls back to the deployment's own network (C8),
// discover searches every network (scenario 29).
func envelope(action string, message any, opts ...func(map[string]any)) map[string]any {
	context := map[string]any{
		"action":        action,
		"version":       beckn.Version,
		"messageId":     nextID(),
		"transactionId": nextID(),
		"timestamp":     time.Now().UTC().Format(time.RFC3339),
	}
	for _, opt := range opts {
		opt(context)
	}
	return map[string]any{"context": context, "message": message}
}

// onNetwork scopes one request to a network id.
func onNetwork(id string) func(map[string]any) {
	return func(context map[string]any) { context["networkId"] = id }
}

// ids are canonical UUIDs because the envelope rules require them to be (C6):
// messageId and transactionId are checked for the 8-4-4-4-12 hex shape before
// any handler runs, so an ordered counter — which would make a failing
// scenario's log easier to follow — is not a value this service accepts.
func nextID() string { return uuid.NewString() }

// publish sends one publish request and returns the per-catalog verdicts.
//
// It asserts the 200 itself: a publish answers 200 even when every catalog came
// back REJECTED (C3), so any other status means the request never reached the
// service's judgement at all, and a scenario that went on to index into an
// empty results array would fail one assertion later with the wrong message.
func (s *service) publish(t *testing.T, message any) []beckn.CatalogProcessingResult {
	t.Helper()

	answer := s.post(t, "/publish", envelope(beckn.ActionPublish, message))
	if answer.status != http.StatusOK {
		t.Fatalf("POST /publish = %d, want 200\nbody: %s", answer.status, answer.body)
	}

	var body struct {
		Message beckn.CatalogOnPublishAction `json:"message"`
	}
	answer.decode(t, &body)
	return body.Message.Results
}

// publishCatalogs is the common case: a publish carrying catalogs and no
// directives, which is exactly what A9's field-wise defaults are about.
func (s *service) publishCatalogs(t *testing.T, catalogs ...any) []beckn.CatalogProcessingResult {
	t.Helper()

	return s.publish(t, map[string]any{"catalogs": catalogs})
}

// discover sends one intent and returns the matched catalogs.
//
// Like publish it asserts the 200, and for the same reason: the degradations
// and the empty results are both 200s, so a non-200 means the request was
// refused rather than answered and the body says why.
func (s *service) discover(t *testing.T, intent map[string]any) []beckn.Catalog {
	t.Helper()

	answer := s.discoverResponse(t, intent)
	if answer.status != http.StatusOK {
		t.Fatalf("POST /discover = %d, want 200\nbody: %s", answer.status, answer.body)
	}

	var body struct {
		Message beckn.OnDiscoverAction `json:"message"`
	}
	answer.decode(t, &body)
	return body.Message.Catalogs
}

// discoverOn is discover scoped to one network, which is what makes a
// visibleTo gate observable: an intent carrying no networkId searches every
// network, so a scenario about visibility that omitted it would assert nothing.
func (s *service) discoverOn(t *testing.T, networkID string, intent map[string]any) []beckn.Catalog {
	t.Helper()

	answer := s.post(t, "/discover",
		envelope(beckn.ActionDiscover, map[string]any{"intent": intent}, onNetwork(networkID)))
	if answer.status != http.StatusOK {
		t.Fatalf("POST /discover = %d, want 200\nbody: %s", answer.status, answer.body)
	}

	var body struct {
		Message beckn.OnDiscoverAction `json:"message"`
	}
	answer.decode(t, &body)
	return body.Message.Catalogs
}

// discoverPaged is discover with an explicit page size.
//
// The page is a query parameter rather than a member of the intent: `limit` and
// `offset` are read off the URL, which is the one part of the transport the
// plan does not spell out. Named here rather than in each scenario so that if
// it moves into the body there is one call site to move.
//
// Two scenarios need it because the default page is twenty and their fixtures
// are forty: a scenario asserting on a count would otherwise be asserting on
// SEARCH_DEFAULT_LIMIT.
func (s *service) discoverPaged(
	t *testing.T, networkID string, limit int, intent map[string]any,
) []beckn.Catalog {
	t.Helper()

	body := envelope(beckn.ActionDiscover, map[string]any{"intent": intent})
	if networkID != "" {
		onNetwork(networkID)(nested(body, "context"))
	}

	answer := s.post(t, "/discover?limit="+strconv.Itoa(limit), body)
	if answer.status != http.StatusOK {
		t.Fatalf("POST /discover = %d, want 200\nbody: %s", answer.status, answer.body)
	}

	var decoded struct {
		Message beckn.OnDiscoverAction `json:"message"`
	}
	answer.decode(t, &decoded)
	return decoded.Message.Catalogs
}

// discoverResponse is discover without the 200 assertion, for the scenarios
// whose subject IS the refusal.
func (s *service) discoverResponse(t *testing.T, intent map[string]any) response {
	t.Helper()

	return s.post(t, "/discover", envelope(beckn.ActionDiscover, map[string]any{"intent": intent}))
}

// nack decodes a refusal. Separate from decode so the scenarios asserting a
// code read as one line rather than as a struct literal.
func (r response) nack(t *testing.T) beckn.Nack {
	t.Helper()

	var body beckn.Nack
	r.decode(t, &body)
	return body
}

// resourceIDs flattens a discover response to the resource ids it returned, in
// the order they arrived. Almost every discover scenario asserts on this and
// nothing else, and spelling the two nested loops out per scenario is how one
// of them ends up quietly reading only the first catalog.
func resourceIDs(catalogs []beckn.Catalog) []string {
	ids := make([]string, 0)
	for _, catalog := range catalogs {
		for _, resource := range catalog.Resources {
			ids = append(ids, resource.ID)
		}
	}
	return ids
}

// providerGeoPath is where a catalog's own locations sit, and the pointer a
// spatial constraint targets to reach them. Written in the dot form a caller
// would send rather than the bracket form the walker stores, because
// jsonpath.Canonicalise is what reconciles the two and scenario 28 is what
// pins that it does.
const providerGeoPath = `$.catalogs[*].provider.availableAt[*].geo`

// Two points in Bengaluru, ~4.6 km apart, and one 400 km away in Hyderabad.
// Named rather than spelled per scenario so a fixture that means "far away"
// says so.
var (
	majestic    = [2]float64{77.5713, 12.9767}
	koramangala = [2]float64{77.6245, 12.9352}
	hyderabad   = [2]float64{78.4867, 17.3850}
)

// aCatalog builds the smallest catalog beckn.yaml accepts — id, descriptor and
// provider — and applies the options a scenario adds to it.
//
// A builder rather than a literal per scenario because Catalog closes
// additionalProperties: a scenario that spelled its own map would be one typo
// away from a SCH_ rejection that looks like the behaviour under test.
func aCatalog(id string, opts ...func(map[string]any)) map[string]any {
	catalog := map[string]any{
		"id":         id,
		"descriptor": map[string]any{"name": id},
		"provider": map[string]any{
			"id":         id + "-provider",
			"descriptor": map[string]any{"name": id + " provider"},
		},
	}
	for _, opt := range opts {
		opt(catalog)
	}
	return catalog
}

// availableAt puts the catalog's provider at one or more points, which is where
// the geometry walker finds them.
func availableAt(points ...[2]float64) func(map[string]any) {
	geometries := make([]map[string]any, 0, len(points))
	for _, p := range points {
		geometries = append(geometries, geoPoint(p))
	}
	return availableAtGeometry(geometries...)
}

// availableAtGeometry is availableAt for the scenarios whose subject is the
// geometry TYPE rather than the place — a polygon service area, a point
// shopfront — and it is what availableAt is written over so that both reach
// storage by the same path.
func availableAtGeometry(geometries ...map[string]any) func(map[string]any) {
	return func(catalog map[string]any) {
		locations := make([]any, 0, len(geometries))
		for _, geometry := range geometries {
			locations = append(locations, map[string]any{"geo": geometry})
		}
		nested(catalog, "provider")["availableAt"] = locations
	}
}

func resources(list ...map[string]any) func(map[string]any) {
	return func(catalog map[string]any) {
		entries := make([]any, 0, len(list))
		for _, resource := range list {
			entries = append(entries, resource)
		}
		catalog["resources"] = entries
	}
}

// aResource is the minimum the schema requires — an id — plus the descriptor
// every text search in this suite matches on.
// An empty name OMITS the descriptor rather than sending an empty one. The
// difference is the whole of scenario 2: a MERGE patch that names no descriptor
// must leave the stored one alone, and a fixture that always sent
// {"name": ""} would overwrite it with a blank and the scenario would be
// asserting the opposite of what it says.
func aResource(id, name string, opts ...func(map[string]any)) map[string]any {
	resource := map[string]any{"id": id}
	if name != "" {
		resource["descriptor"] = map[string]any{"name": name}
	}
	for _, opt := range opts {
		opt(resource)
	}
	return resource
}

// nested returns the object at key, and panics when the fixture above it does
// not have one.
//
// A panic rather than a t.Fatal because the caller is a fixture builder with no
// *testing.T, and because the only way to reach it is to have changed one of
// the builders in this file — a bug in the harness, not a failure of the
// service, and the stack is what says which builder.
func nested(document map[string]any, key string) map[string]any {
	child, ok := document[key].(map[string]any)
	if !ok {
		panic(fmt.Sprintf("the fixture has no %q object: %v", key, document))
	}
	return child
}

func geoPoint(p [2]float64) map[string]any {
	return map[string]any{"type": beckn.GeometryPoint, "coordinates": []float64{p[0], p[1]}}
}

// geoPolygon closes a ring for you: a GeoJSON polygon's first and last
// positions must be the same one, and a fixture that forgot would be refused by
// the walker rather than by the assertion it was written for.
func geoPolygon(ring [][2]float64) map[string]any {
	closed := make([][2]float64, 0, len(ring)+1)
	closed = append(closed, ring...)
	if ring[0] != ring[len(ring)-1] {
		closed = append(closed, ring[0])
	}

	positions := make([]any, 0, len(closed))
	for _, p := range closed {
		positions = append(positions, []float64{p[0], p[1]})
	}
	return map[string]any{"type": beckn.GeometryPolygon, "coordinates": []any{positions}}
}

// boxAround is a square polygon of side 2*halfMetres centred on a point.
//
// The degree conversion is deliberately crude — it treats a degree of longitude
// as constant across the box — because at the few kilometres these fixtures use
// the error is metres, and no scenario here is phrased closer to a boundary
// than scenario 14, which uses points and not boxes.
func boxAround(centre [2]float64, halfMetres float64) map[string]any {
	const metresPerDegreeLatitude = 111132.0
	lat := halfMetres / metresPerDegreeLatitude
	lon := halfMetres / (metresPerDegreeLatitude * math.Cos(centre[1]*math.Pi/180))

	west, east := centre[0]-lon, centre[0]+lon
	south, north := centre[1]-lat, centre[1]+lat
	return geoPolygon([][2]float64{
		{west, south}, {east, south}, {east, north}, {west, north},
	})
}

// dwithin is the radius constraint most of the spatial scenarios are phrased
// in: everything within metres of a point, found through target.
func dwithin(target string, centre [2]float64, metres float64, opts ...func(map[string]any)) map[string]any {
	constraint := map[string]any{
		"op":             beckn.OpSDWithin,
		"targets":        target,
		"geometry":       geoPoint(centre),
		"distanceMeters": metres,
	}
	for _, opt := range opts {
		opt(constraint)
	}
	return constraint
}

// quantified sets how a constraint is evaluated when `targets` resolves to more
// than one geometry. Omitting it reads as ANY, which is why the scenarios about
// NONE and ALL always say so.
func quantified(kind string) func(map[string]any) {
	return func(constraint map[string]any) { constraint["quantifier"] = kind }
}

// spatial wraps one constraint into an intent, so a scenario states the part it
// is about and nothing else.
func spatial(constraint map[string]any) map[string]any {
	return map[string]any{"spatial": []any{constraint}}
}

// near is the discover most scenarios use to read back what a publish stored: a
// radius around the catalog's own provider location.
//
// A radius rather than a text search, because the geometry a fixture publishes
// is under the scenario's control while the tsvector is not — `discover_tsquery`
// ORs its terms and stems nothing, so a text probe would couple every publish
// scenario to the tokeniser it is not about.
func (s *service) near(t *testing.T, centre [2]float64, metres float64) []beckn.Catalog {
	t.Helper()

	return s.discover(t, spatial(dwithin(providerGeoPath, centre, metres)))
}

// text is the other half: an intent that carries only a query string.
func text(query string) map[string]any {
	return map[string]any{"textSearch": query}
}

// publishWith sends catalogs together with the directives that govern them, for
// the scenarios whose subject is a directive: the update mode, the catalog type,
// the visibility.
func (s *service) publishWith(
	t *testing.T, catalogs []any, directives ...map[string]any,
) []beckn.CatalogProcessingResult {
	t.Helper()

	return s.publish(t, map[string]any{
		"catalogs":          catalogs,
		"publishDirectives": directives,
	})
}

// directive is one publishDirectives entry. The variadic options are the fields
// A9 gives a default to, so a scenario states the one it is varying and inherits
// the rest — which is the behaviour A9 describes.
//
// catalogType is spelled here rather than left to A9 because the schema puts it
// in the directive's `required` list: a directive naming only `catalogId` is a
// 400, so the field-wise default A9 describes is reachable only by omitting the
// whole directive. REGULAR is the value every scenario but 5 wants, and 5
// overrides it.
func directive(catalogID string, opts ...func(map[string]any)) map[string]any {
	entry := map[string]any{
		"catalogId":   catalogID,
		"catalogType": beckn.CatalogTypeRegular,
	}
	for _, opt := range opts {
		opt(entry)
	}
	return entry
}

func updateMode(mode string) func(map[string]any) {
	return func(entry map[string]any) { entry["updateMode"] = mode }
}

func catalogType(kind string) func(map[string]any) {
	return func(entry map[string]any) { entry["catalogType"] = kind }
}

func visibleTo(networks ...string) func(map[string]any) {
	return func(entry map[string]any) { entry["visibleTo"] = networks }
}

// extendsMaster is the resource directive A1 refuses. Built here rather than
// spelled in the scenario so the shape stays the one the schema admits — the
// refusal has to be the service's, not the validator's.
func extendsMaster(resourceID, masterID string) func(map[string]any) {
	return func(entry map[string]any) {
		entry["resourceDirectives"] = []any{map[string]any{
			"resourceId": resourceID,
			"extends":    map[string]any{"masterResourceId": masterID},
		}}
	}
}

// The JSON-LD pair every resourceAttributes document in this suite carries.
//
// Not decoration: `Attributes` declares both in its `required` list, so a
// fixture omitting them is a 400 before any behaviour under test runs. They are
// also what the derivation reads into schema_context and schema_type, which is
// why they are constants — a scenario filtering on the pair and a scenario
// merely carrying it must be carrying the same one.
const (
	schemaContext = "https://schema.org"
	schemaType    = "Product"
)

// withAttributes puts a resourceAttributes document on a resource, filling in
// the required JSON-LD pair when the fixture has not.
//
// A map with a nil value marshals to an explicit JSON null, which is what a
// MERGE patch uses to CLEAR a member rather than to leave it alone (A8) — so a
// caller passing one is stating a deletion, and this helper must not touch it.
func withAttributes(attributes map[string]any) func(map[string]any) {
	document := map[string]any{"@context": schemaContext, "@type": schemaType}
	for key, value := range attributes {
		document[key] = value
	}
	return func(resource map[string]any) { resource["resourceAttributes"] = document }
}

// jsonLD is the pair withAttributes adds, as an assertion reads it back.
func jsonLD(members map[string]any) map[string]any {
	document := map[string]any{"@context": schemaContext, "@type": schemaType}
	for key, value := range members {
		document[key] = value
	}
	return document
}

// withValidity sets catalog.validity. `nil` reaches the wire as an explicit
// null, which clears all four columns.
func withValidity(period any) func(map[string]any) {
	return func(catalog map[string]any) { catalog["validity"] = period }
}

// inactive is the publisher's own off switch, and it is spelled as a helper
// because `false` is the value A9 makes interesting: absent means true.
func inactive() func(map[string]any) {
	return func(catalog map[string]any) { catalog["isActive"] = false }
}

func offers(list ...map[string]any) func(map[string]any) {
	return func(catalog map[string]any) {
		entries := make([]any, 0, len(list))
		for _, offer := range list {
			entries = append(entries, offer)
		}
		catalog["offers"] = entries
	}
}

// anOffer names the resources it covers. An EMPTY list is catalog-wide and an
// absent one is too, which is why resourceIds is only set when there are ids:
// a `"resourceIds": []` and no key at all must reach the same stored row.
func anOffer(id string, resourceIDs ...string) map[string]any {
	offer := map[string]any{
		"id":         id,
		"descriptor": map[string]any{"name": id},
	}
	if len(resourceIDs) > 0 {
		offer["resourceIds"] = resourceIDs
	}
	return offer
}

// findResource returns the resource with this id from a discover response, and
// fails the scenario when it is absent — a zero beckn.Resource would answer
// every assertion after it with something plausible.
func findResource(t *testing.T, catalogs []beckn.Catalog, id string) beckn.Resource {
	t.Helper()

	for _, catalog := range catalogs {
		for _, resource := range catalog.Resources {
			if resource.ID == id {
				return resource
			}
		}
	}
	t.Fatalf("resource %q is not in the response; it holds %v", id, resourceIDs(catalogs))
	return beckn.Resource{}
}

// offerIDs flattens a discover response to the offers hydrated onto it.
func offerIDs(catalogs []beckn.Catalog) []string {
	ids := make([]string, 0)
	for _, catalog := range catalogs {
		for _, offer := range catalog.Offers {
			ids = append(ids, offer.ID)
		}
	}
	return ids
}

// assertJSON compares two documents as JSON rather than as bytes.
//
// Every document in a response has been through JSONB, which reorders members
// and normalises numbers, so a byte comparison against a fixture would fail on
// a difference no caller can observe. Comparing a stored document against
// ITSELF read back earlier — which is what scenario 2 does — is the one place
// bytes would be fair, and even there this is what says why they differ.
func assertJSON(t *testing.T, got json.RawMessage, want any, what string) {
	t.Helper()

	var decoded any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("%s is not JSON: %v\nbody: %s", what, err, got)
	}
	if !reflect.DeepEqual(decoded, normalised(t, want)) {
		t.Errorf("%s is %s, want %v", what, got, want)
	}
}

// normalised round-trips the expectation through JSON so that a Go int and the
// float64 a decoder produces compare equal. Without it every attribute fixture
// would have to be written in float64 to pass, which is a fixture written
// around the assertion rather than around the behaviour.
func normalised(t *testing.T, want any) any {
	t.Helper()

	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("encode the expectation: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode the expectation: %v", err)
	}
	return decoded
}
