package validation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
