// Package architecture holds the import-graph guards that keep the TRD §5 swap
// boundary real rather than aspirational.
package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	modulePath = "github.com/OpenAgriNet/discovery-service"

	// repoRoot is where this test walks from. A relative path rather than a
	// go:generate'd constant, because the walk must work in a checkout that has
	// never run a generator.
	repoRoot = "../.."
)

// The ban is two bans, because the two have different reasons and therefore
// different allow-lists. Collapsing them into one list was the original shape,
// and it made the test harness's legitimate need for the driver look like a
// reason to hand it the adapter as well.

// driverOnly is the PostgreSQL vocabulary: the driver and the vector type.
// Anything holding these is talking to PostgreSQL rather than to a store.
var driverOnly = []string{
	"github.com/jackc/pgx",
	"github.com/pgvector/pgvector-go",
}

// adapterOnly is the generated package, and it is here for a subtler reason
// than the driver: sqlc's output is a set of Go structs that look exactly like
// domain types, and a service that starts passing them around has swapped its
// domain model for its schema without anyone deciding to.
var adapterOnly = []string{
	modulePath + "/src/storage/postgres",
}

// The adapter package itself, obviously. And the composition root, because
// something has to construct the concrete store — that is what a composition
// root is for, and the alternative is a reflective registry that hides the same
// edge behind more machinery.
func mayImportTheAdapter(path string) bool {
	return strings.HasPrefix(path, filepath.Join("src", "storage", "postgres")+string(filepath.Separator)) ||
		path == filepath.Join("src", "app", "container.go")
}

// mayImportTheDriver adds the database test harness, and only the harness.
//
// tests/dbtest starts a real PostgreSQL, runs the migrations against it and
// reads pg_indexes and pg_stat_user_indexes back. It cannot be written through
// the store interface, because what it is testing is the schema underneath the
// interface — an assertion about idx_rg_cells_full has nowhere else to live.
//
// It is NOT granted the adapter: a harness that constructs the real store is a
// harness that has started testing the adapter through itself, and the
// conformance suite in src/storage/postgres is where that belongs. So the two
// lists stay separate rather than becoming one list with three entries.
func mayImportTheDriver(path string) bool {
	return mayImportTheAdapter(path) ||
		strings.HasPrefix(path, filepath.Join("tests", "dbtest")+string(filepath.Separator))
}

// otelOnly is the OpenTelemetry SDK and everything that links it. A23: a
// controller records facts and never links the telemetry stack, so that a
// controller's build does not break on an SDK release.
//
// A23's own wording is "controllers never import the telemetry package", which
// read literally would also ban the attribute registry that controllers must
// name a key from. The rule's PURPOSE is that a controller links no exporter
// and no SDK, and that purpose is preserved exactly — so the ban carries an
// exception for .../telemetry/fact, whose entire dependency set is a closed
// list of standard-library packages pinned by TestTheRegistryLinksNothing below.
var otelOnly = []string{
	"go.opentelemetry.io",
	modulePath + "/src/platform/telemetry",
}

// factIsNotTheSDK is the one exception to otelOnly, and it is a subpath rather
// than a package list because fact/ will grow files (instruments.go in Task 25)
// and each one would otherwise need adding here.
var factIsNotTheSDK = []string{
	modulePath + "/src/platform/telemetry/fact",
}

// mayImportOtel: the telemetry package itself, and the composition root that
// constructs the provider — the same pairing mayImportTheAdapter already makes,
// for the same reason.
//
// src/platform/logger is deliberately NOT here, although the seam's original
// sketch of this allow-list listed it. It projects a fact.Record into zap fields and so reads a
// Definition, but fact is not banned, so the exemption it would receive is one
// it does not need — and granting it would permit exactly the SDK import that
// keeps request_logger.go free of a telemetry dependency. An allow-list entry
// nobody needs is an allow-list entry nobody will notice being used.
//
// The rest are 23c's, and they are individual FILES rather than packages on
// purpose: `src/platform/middlewares` as a prefix would let every future
// middleware link the SDK, which is most of what this rule is for. Each entry
// below is one file that has to touch the SDK to do something the design named,
// and adding a seventh should be as awkward as these six were.
//
// src/platform/logger is deliberately still NOT here. It projects a fact.Record
// into zap fields and so reads a Definition, but fact is not banned, so the
// exemption it would receive is one it does not need — and granting it would
// permit exactly the SDK import that keeps request_logger.go free of a telemetry
// dependency. An allow-list entry nobody needs is an entry nobody will notice
// being used.
//
// Note src/app/router.go is NOT here either, though 23c was expected to need it.
// It calls a.Telemetry.Tracer() through a field whose type container.go already
// declares, so it names no telemetry package itself — the narrower outcome, kept
// because the guard reported it.
var mayImportOtelFiles = []string{
	// Trace starts the span and needs trace.Tracer, trace.SpanKind and codes.
	filepath.Join("src", "platform", "middlewares", "trace.go"),

	// Its tests read spans back, which no amount of indirection avoids: the
	// question they ask is what the SDK exported.
	filepath.Join("src", "platform", "middlewares", "trace_test.go"),

	// Task 20's chain-order assertion, moved off X-Beckn-Chain and onto the span.
	filepath.Join("src", "app", "router_test.go"),

	// The two outbound clients, which inject the traceparent so the far side can
	// join this trace. Both reach only telemetry.Inject, not the SDK.
	filepath.Join("src", "platform", "validation", "http_fetcher.go"),
	filepath.Join("src", "indexing", "embeddings", "ollama.go"),
}

func mayImportOtel(path string) bool {
	return strings.HasPrefix(path, filepath.Join("src", "platform", "telemetry")+string(filepath.Separator)) ||
		path == filepath.Join("src", "app", "container.go") ||
		slices.Contains(mayImportOtelFiles, path)
}

// dbtestOnly is tests/dbtest itself: whether a package requires a real
// PostgreSQL to run at all, which is a stronger claim than merely being
// allowed to talk to one. Nothing here marks a Go test as "integration" —
// no build tag, no file suffix — so this allow-list IS the boundary; a
// package added to it is a package that now starts a testcontainer on every
// `go test`.
var dbtestOnly = []string{
	modulePath + "/tests/dbtest",
}

// mayImportDbtest names every package that has already made that trade.
// src/app earns its place through container_test.go, Build's own happy-path
// test — the composition root is where "does everything really wire
// together against a real database" has to be answered, the same way it
// already owns the adapter and the driver above.
func mayImportDbtest(path string) bool {
	return mayImportTheAdapter(path) ||
		strings.HasPrefix(path, filepath.Join("src", "app")+string(filepath.Separator)) ||
		strings.HasPrefix(path, filepath.Join("tests", "acceptance")+string(filepath.Separator)) ||
		strings.HasPrefix(path, filepath.Join("tests", "dbtest")+string(filepath.Separator))
}

// TestNothingButTheAdapterImportsPostgres walks every package in the module.
//
// Its twin, src/domain/purity_test.go, protects the contract; this protects
// everything that consumes it, which is where the leak actually happens. A
// domain that imports nothing is no use if src/discover reaches around it and
// talks to pgx directly.
//
// It passes trivially today, because no adapter exists yet — which is the
// point. A guard written after the thing it guards is written against code
// somebody already has a reason to keep.
func TestNothingButTheAdapterImportsPostgres(t *testing.T) {
	fileSet := token.NewFileSet()

	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return skipNonSource(entry)
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		checkFile(t, fileSet, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}
}

// skipNonSource prunes the directories a source walk has no business entering.
func skipNonSource(entry fs.DirEntry) error {
	switch entry.Name() {
	case ".git", "bin", "vendor", "node_modules":
		return filepath.SkipDir
	}
	return nil
}

func checkFile(t *testing.T, fileSet *token.FileSet, path string) {
	t.Helper()

	file, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
	if err != nil {
		t.Errorf("parse %s: %v", path, err)
		return
	}

	relative, err := filepath.Rel(repoRoot, path)
	if err != nil {
		t.Errorf("locate %s: %v", path, err)
		return
	}
	bans := []struct {
		paths   []string
		except  []string
		allowed bool
		who     string
	}{
		{driverOnly, nil, mayImportTheDriver(relative), "src/storage/postgres/**, src/app/container.go and tests/dbtest/**"},
		{adapterOnly, nil, mayImportTheAdapter(relative), "src/storage/postgres/** and src/app/container.go"},
		{dbtestOnly, nil, mayImportDbtest(relative), "src/storage/postgres/**, src/app/**, tests/acceptance/** and tests/dbtest/**"},
		{otelOnly, factIsNotTheSDK, mayImportOtel(relative), "src/platform/telemetry/** and src/app/container.go (everyone may import src/platform/telemetry/fact)"},
	}

	for _, imported := range file.Imports {
		importPath := strings.Trim(imported.Path.Value, `"`)
		for _, ban := range bans {
			if ban.allowed || matchesAny(importPath, ban.except) {
				continue
			}
			if matchesAny(importPath, ban.paths) {
				t.Errorf("%s imports %q — only %s may", relative, importPath, ban.who)
			}
		}
	}
}

func matchesAny(importPath string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(importPath, prefix) {
			return true
		}
	}
	return false
}

// factMayImport is the registry's entire dependency set, and it is a closed
// list rather than "the standard library" on purpose.
//
// The seam's file sketch said "context, iter, time. NOTHING ELSE", and
// opentelemetry.md §Two packages, and why the split is load-bearing states the
// property that sketch was serving: fact imports only the standard library, so
// that src/discover naming a key pulls no SDK into a controller's dependency
// graph. The property is what binds; the sketch's three
// packages were an estimate of what implementing it would take, made before it
// was implemented. Where they disagree the property wins, and the four
// additions each have a reason:
//
//   - fmt renders the failure messages. A guard that cannot say which row is
//     wrong is a guard someone deletes.
//   - slices and strings do the bounds and the enum names.
//   - sync is Record's mutex, and it is the one addition worth arguing about.
//     Record is exported and written from a controller, the response writer and
//     any middleware below Trace, so unlike middlewares.correlation it cannot
//     document its way out of synchronisation. An uncontended mutex costs less
//     than the race it removes, and `go test -race` is a gate here.
//
// time is unused today and kept because the sketch named it: a row carrying a
// timestamp is a plausible next edit and it costs nothing to allow.
//
// Widening this is a decision, not a formality. Anything with a dot in its
// first path segment is a module outside the standard library and belongs
// nowhere near this package.
var factMayImport = []string{
	"context", "fmt", "iter", "slices", "strings", "sync", "time",
}

// TestTheRegistryLinksNothing is the other half of the otelOnly ban.
//
// The ban says a controller may import fact; this says what importing fact
// costs. Without it the exception carved out of otelOnly is unbounded: fact
// could acquire a dependency on anything at all, and every controller would
// acquire it too, silently, because the ban that would have caught it is the
// one fact is exempt from.
func TestTheRegistryLinksNothing(t *testing.T) {
	fileSet := token.NewFileSet()
	factDir := filepath.Join(repoRoot, "src", "platform", "telemetry", "fact")

	entries, err := os.ReadDir(factDir)
	if err != nil {
		t.Fatalf("read the registry package: %v", err)
	}

	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++

		file, err := parser.ParseFile(fileSet, filepath.Join(factDir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", name, err)
			continue
		}
		for _, imported := range file.Imports {
			importPath := strings.Trim(imported.Path.Value, `"`)
			if !slices.Contains(factMayImport, importPath) {
				t.Errorf("src/platform/telemetry/fact/%s imports %q, which is not in "+
					"factMayImport %v.\n"+
					"        Every package that observes a fact imports this one, so its\n"+
					"        dependency set is theirs. If %q genuinely belongs here, widen\n"+
					"        the list and say why in the same commit.",
					name, importPath, factMayImport, importPath)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no non-test Go files found under src/platform/telemetry/fact; " +
			"this test is checking nothing")
	}
}
