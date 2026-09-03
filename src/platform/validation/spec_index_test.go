package validation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// The pinned copy of the protocol this build compiles against. The same
// fixture the other conformance tests read, so a spec bump moves one file and
// every layer that reads it fails together rather than one at a time.
const specFixture = "../../../tests/testdata/beckn-v2.0.0.yaml"

func specDocument(t *testing.T) []byte {
	t.Helper()

	document, err := os.ReadFile(specFixture)
	if err != nil {
		t.Fatalf("read %s: %v", specFixture, err)
	}
	return document
}

// servingIndex is the index built from the pinned document — what a healthy
// boot produces.
func servingIndex(t *testing.T) *SpecIndex {
	t.Helper()

	index, err := NewSpecIndex(specDocument(t))
	if err != nil {
		t.Fatalf("compile the pinned spec: %v", err)
	}
	return index
}

// C2: one route, two spellings. `publish` and `catalog/publish` are the same
// request, and the index is keyed by action rather than by URL precisely so
// both land on one schema — which is also what makes a second index for a
// second protocol version additive rather than a rewrite (T5).
func TestBothPublishSpellingsResolveToOneSchema(t *testing.T) {
	index := servingIndex(t)

	short, shortFound := index.lookup(beckn.ActionPublish)
	long, longFound := index.lookup(beckn.ActionCatalogPublish)
	if !shortFound || !longFound {
		t.Fatalf("lookup found publish=%v catalog/publish=%v, want both", shortFound, longFound)
	}
	if short.schema != long.schema {
		t.Error("the two publish spellings resolved to different schemas")
	}
}

// The document spells the action `catalog/publish` as a const inside the
// schema, so a body saying `publish` fails its own schema on the field that
// named it. The canonical spelling is read off the schema and carried here so
// L1 can reconcile the two without editing the published document.
func TestTheIndexCarriesTheSpellingTheDocumentDeclares(t *testing.T) {
	index := servingIndex(t)

	for _, spelling := range []string{beckn.ActionPublish, beckn.ActionCatalogPublish} {
		entry, found := index.lookup(spelling)
		if !found {
			t.Fatalf("%s is not in the index", spelling)
		}
		if entry.canonical != beckn.ActionCatalogPublish {
			t.Errorf("%s: canonical = %q, want %q", spelling, entry.canonical, beckn.ActionCatalogPublish)
		}
	}
}

func TestDiscoverIsIndexedUnderItsOwnAction(t *testing.T) {
	entry, found := servingIndex(t).lookup(beckn.ActionDiscover)
	if !found {
		t.Fatal("discover is not in the index")
	}
	if entry.canonical != beckn.ActionDiscover {
		t.Errorf("canonical = %q, want %q", entry.canonical, beckn.ActionDiscover)
	}
}

// An action nothing declares is absent rather than present-and-empty. L1 turns
// that into a NACK; a zero-valued entry would turn it into a nil dereference,
// which is the 500 the plan's pin rules out.
func TestAnUndeclaredActionIsAbsentFromTheIndex(t *testing.T) {
	if entry, found := servingIndex(t).lookup("search"); found {
		t.Errorf("an action the document never declares resolved to %+v", entry)
	}
}

// A response action is not a request action. on_discover is a callback shape
// this service writes, and indexing it would let a caller POST one in and have
// it validate.
func TestAResponseActionIsNotIndexedAsARequest(t *testing.T) {
	if _, found := servingIndex(t).lookup(beckn.ActionOnDiscover); found {
		t.Error("on_discover is indexed as a request schema")
	}
}

func TestADocumentThatIsNotOpenAPIFailsToCompile(t *testing.T) {
	if _, err := NewSpecIndex([]byte("this: is not\n  - a spec\n")); err == nil {
		t.Error("a document that is not an OpenAPI spec compiled clean")
	}
}

// A spec that parses but declares none of the paths this service answers is a
// spec for a different protocol. Booting on it would produce a service that
// NACKs every request it was deployed to serve — a failure that looks like a
// caller's fault and is not.
func TestADocumentMissingAServedPathFailsToCompile(t *testing.T) {
	document := strings.Replace(string(specDocument(t)), "  /discover:", "  /discover-not:", 1)

	_, err := NewSpecIndex([]byte(document))
	if err == nil {
		t.Fatal("a spec declaring no /discover compiled clean")
	}
	if !strings.Contains(err.Error(), "/discover") {
		t.Errorf("error = %q, want it to name the missing path", err)
	}
}

// discoverRequestBodyBlock is /discover's requestBody, verbatim from the
// pinned fixture — unique in the document because of `const: discover`, so
// it is a safe anchor for a targeted edit that leaves every other path alone.
const discoverRequestBodyBlock = `      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required:
              - context
              - message
              properties:
                context:
                  allOf:
                  - $ref: '#/components/schemas/Context'
                  - type: object
                    properties:
                      action:
                        type: string
                        const: discover
                message:`

// A path with no requestBody at all — requestSchema's own refusal, not the
// "path missing" one TestADocumentMissingAServedPathFailsToCompile pins.
func TestAServedPathWithNoRequestBodyFailsToCompile(t *testing.T) {
	edited := strings.Replace(discoverRequestBodyBlock, "requestBody:", "requestBodyRenamed:", 1)
	document := strings.Replace(string(specDocument(t)), discoverRequestBodyBlock, edited, 1)

	_, err := NewSpecIndex([]byte(document))
	if err == nil {
		t.Fatal("a /discover with no requestBody compiled clean")
	}
	if !strings.Contains(err.Error(), "no request body") {
		t.Errorf("error = %q, want it to name the missing request body", err)
	}
}

// A requestBody present with no application/json content — requestSchema's
// third refusal.
func TestAServedPathWithNoJSONContentFailsToCompile(t *testing.T) {
	edited := strings.Replace(discoverRequestBodyBlock, "application/json:", "application/xml:", 1)
	document := strings.Replace(string(specDocument(t)), discoverRequestBodyBlock, edited, 1)

	_, err := NewSpecIndex([]byte(document))
	if err == nil {
		t.Fatal("a /discover with no application/json content compiled clean")
	}
	if !strings.Contains(err.Error(), "application/json") {
		t.Errorf("error = %q, want it to name the missing media type", err)
	}
}

// canonicalAction's own fallback: a schema with no `context` property at all
// falls back to the action it is indexed under, rather than panicking on a
// nil dereference.
func TestCanonicalActionWithNoContextPropertyFallsBackToTheIndexedName(t *testing.T) {
	schema := &openapi3.SchemaRef{Value: &openapi3.Schema{Properties: openapi3.Schemas{}}}

	if got := canonicalAction(schema, "myAction"); got != "myAction" {
		t.Errorf("canonicalAction = %q, want the indexed name %q", got, "myAction")
	}
}

// A context schema whose AllOf carries an unresolved $ref (Value == nil,
// skipped rather than dereferenced) and branches naming no action const at
// all falls back the same way — the walk exhausts every branch and finds
// nothing to declare.
func TestCanonicalActionSkipsAnUnresolvedBranchAndFallsBack(t *testing.T) {
	context := &openapi3.SchemaRef{Value: &openapi3.Schema{
		AllOf: openapi3.SchemaRefs{
			{Ref: "#/components/schemas/Unresolved"}, // Value is nil
			{Value: &openapi3.Schema{}},              // resolved, but names no action
		},
	}}
	schema := &openapi3.SchemaRef{Value: &openapi3.Schema{Properties: openapi3.Schemas{"context": context}}}

	if got := canonicalAction(schema, "myAction"); got != "myAction" {
		t.Errorf("canonicalAction = %q, want the indexed name %q", got, "myAction")
	}
}

// loadFromRegistry's two configuration refusals, neither of which involves a
// network call.
func TestLoadFromRegistryRefusesAnUnconfiguredURLOrFetcher(t *testing.T) {
	asked := 0

	if _, err := loadFromRegistry(context.Background(), config.Validation{}, fetchOK(t, &asked)); err == nil {
		t.Error("loadFromRegistry with no SpecURL configured succeeded")
	}
	if _, err := loadFromRegistry(context.Background(),
		config.Validation{SpecURL: "https://spec.example/beckn.yaml"}, nil); err == nil {
		t.Error("loadFromRegistry with no fetcher succeeded")
	}
	if asked != 0 {
		t.Errorf("the fetcher was asked %d times; neither refusal reaches it", asked)
	}
}

// loadFromCache's own refusal for an unconfigured path, distinct from
// LoadSpecIndex's caller-facing message that names both sources.
func TestLoadFromCacheRefusesAnUnconfiguredPath(t *testing.T) {
	if _, err := loadFromCache(""); err == nil {
		t.Error("loadFromCache with no path configured succeeded")
	}
}

// writeCache's own refusal for an unconfigured path — reached directly since
// LoadSpecIndex only calls it after a successful fetch, and every fetch case
// in this file configures a cache path.
func TestWriteCacheRefusesAnUnconfiguredPath(t *testing.T) {
	if err := writeCache("", []byte("x")); err == nil {
		t.Error("writeCache with no path configured succeeded")
	}
}

// A cache directory that cannot be created — its parent is a plain file, not
// a directory — must not fail the boot: LoadSpecIndex only warns when the
// cache write fails, because this boot still has a compiled index to serve.
func TestAFetchSucceedsEvenWhenTheCacheCannotBeWritten(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed the blocking file: %v", err)
	}

	cfg := config.Validation{
		SpecURL:       "https://spec.example/beckn.yaml",
		SpecCachePath: filepath.Join(blocker, "beckn.yaml"), // "not-a-directory" can't hold a child
	}
	asked := 0

	index, err := LoadSpecIndex(context.Background(), cfg, fetchOK(t, &asked))
	if err != nil {
		t.Fatalf("load with a healthy fetch and an unwritable cache: %v", err)
	}
	if _, found := index.lookup(beckn.ActionDiscover); !found {
		t.Error("the index built from the fetch does not serve discover")
	}
}

// writeCache's own MkdirAll and CreateTemp failures, exercised directly
// rather than through LoadSpecIndex's warn-and-continue wrapper above.
func TestWriteCacheWrapsAMkdirFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed the blocking file: %v", err)
	}

	if err := writeCache(filepath.Join(blocker, "child", "beckn.yaml"), []byte("x")); err == nil {
		t.Error("writeCache created a directory under a plain file")
	}
}

// A directory that exists but cannot be written into — read+execute only —
// fails CreateTemp rather than MkdirAll, which is a no-op on a directory
// that is already there.
func TestWriteCacheWrapsACreateTempFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	directory := t.TempDir()
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatalf("chmod the cache directory read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(directory, 0o700); err != nil { // let t.TempDir() clean up
			t.Errorf("restore the cache directory's permissions: %v", err)
		}
	})

	if err := writeCache(filepath.Join(directory, "beckn.yaml"), []byte("x")); err == nil {
		t.Error("writeCache created a file in a directory it cannot write to")
	}
}

// writeAndClose's own write failure: a file already closed refuses the write
// before it ever reaches the close.
func TestWriteAndCloseWrapsAWriteFailure(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "beckn.*.yaml")
	if err != nil {
		t.Fatalf("create a temp file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close it early: %v", err)
	}

	if err := writeAndClose(file, []byte("x")); err == nil {
		t.Error("writeAndClose wrote to an already-closed file")
	}
}

// fetchOK serves the pinned document, and records that it was asked.
func fetchOK(t *testing.T, asked *int) Fetcher {
	t.Helper()

	document := specDocument(t)
	return func(_ context.Context, _ string) ([]byte, error) {
		*asked++
		return document, nil
	}
}

func fetchFails(asked *int) Fetcher {
	return func(_ context.Context, _ string) ([]byte, error) {
		*asked++
		return nil, errors.New("registry unreachable")
	}
}

func loadConfig(t *testing.T) config.Validation {
	t.Helper()

	return config.Validation{
		EnableL1Schema: true,
		SpecURL:        "https://spec.example/beckn.yaml",
		SpecCachePath:  filepath.Join(t.TempDir(), "beckn", "beckn.yaml"),
	}
}

// The cache is written on the way past, not on demand. A cache only populated
// when something asks for it is a cache that is empty on exactly the boot that
// needs it — the first one after the registry goes down.
func TestASuccessfulFetchIsWrittenToTheCache(t *testing.T) {
	cfg, asked := loadConfig(t), 0

	if _, err := LoadSpecIndex(context.Background(), cfg, fetchOK(t, &asked)); err != nil {
		t.Fatalf("load from a healthy fetch: %v", err)
	}
	if asked != 1 {
		t.Errorf("the fetcher was asked %d times, want 1", asked)
	}

	cached, err := os.ReadFile(cfg.SpecCachePath)
	if err != nil {
		t.Fatalf("read the cache the load should have written: %v", err)
	}
	if len(cached) == 0 {
		t.Error("the cache was written empty")
	}
}

// The plan's pin. A registry that is down must not take the service with it:
// the spec changes on the timescale of a protocol release, and the copy on disk
// is the same document the last boot validated against.
func TestAFetchFailureFallsBackToTheCache(t *testing.T) {
	cfg, asked := loadConfig(t), 0

	if err := os.MkdirAll(filepath.Dir(cfg.SpecCachePath), 0o750); err != nil {
		t.Fatalf("seed the cache directory: %v", err)
	}
	if err := os.WriteFile(cfg.SpecCachePath, specDocument(t), 0o600); err != nil {
		t.Fatalf("seed the cache: %v", err)
	}

	index, err := LoadSpecIndex(context.Background(), cfg, fetchFails(&asked))
	if err != nil {
		t.Fatalf("load with a failed fetch and a warm cache: %v", err)
	}
	if asked != 1 {
		t.Errorf("the fetcher was asked %d times, want 1 — the cache is a fallback, not the source", asked)
	}
	if _, found := index.lookup(beckn.ActionDiscover); !found {
		t.Error("the index built from the cache does not serve discover")
	}
}

// Neither source, so there is nothing to validate against. Booting anyway would
// mean starting with L1 configured on and silently doing nothing — a service
// that reports healthy while accepting bodies it was deployed to refuse.
func TestABootWithNoFetchAndNoCacheFails(t *testing.T) {
	cfg, asked := loadConfig(t), 0

	_, err := LoadSpecIndex(context.Background(), cfg, fetchFails(&asked))
	if err == nil {
		t.Fatal("the boot succeeded with neither a fetch nor a cache")
	}
	for _, want := range []string{cfg.SpecURL, cfg.SpecCachePath} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q — both sources failed and an operator needs to know which", err, want)
		}
	}
}

// A cache holding something that is not the protocol is not a fallback. It is
// refused rather than indexed empty, for the same reason the missing-path case
// is: an index nothing is in NACKs every request.
func TestACacheHoldingRubbishFailsTheBoot(t *testing.T) {
	cfg, asked := loadConfig(t), 0

	if err := os.MkdirAll(filepath.Dir(cfg.SpecCachePath), 0o750); err != nil {
		t.Fatalf("seed the cache directory: %v", err)
	}
	if err := os.WriteFile(cfg.SpecCachePath, []byte("not a spec"), 0o600); err != nil {
		t.Fatalf("seed the cache: %v", err)
	}

	if _, err := LoadSpecIndex(context.Background(), cfg, fetchFails(&asked)); err == nil {
		t.Error("a cache holding rubbish booted clean")
	}
}

// A fetch that succeeds and returns a document that will not compile is a
// failed fetch. Overwriting the good cache with it would turn one bad response
// into a service that cannot boot again.
func TestAnUncompilableFetchDoesNotOverwriteTheCache(t *testing.T) {
	cfg := loadConfig(t)

	if err := os.MkdirAll(filepath.Dir(cfg.SpecCachePath), 0o750); err != nil {
		t.Fatalf("seed the cache directory: %v", err)
	}
	if err := os.WriteFile(cfg.SpecCachePath, specDocument(t), 0o600); err != nil {
		t.Fatalf("seed the cache: %v", err)
	}

	fetch := func(_ context.Context, _ string) ([]byte, error) { return []byte("not a spec"), nil }
	if _, err := LoadSpecIndex(context.Background(), cfg, fetch); err != nil {
		t.Fatalf("load with a bad fetch and a warm cache: %v", err)
	}

	cached, err := os.ReadFile(cfg.SpecCachePath)
	if err != nil {
		t.Fatalf("read the cache: %v", err)
	}
	if string(cached) == "not a spec" {
		t.Error("the cache was overwritten with a document that does not compile")
	}
}
