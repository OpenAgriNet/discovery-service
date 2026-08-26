// Package architecture holds the import-graph guards that keep the TRD §5 swap
// boundary real rather than aspirational.
package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
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

// adapterOnly is what may not escape src/storage/postgres.
//
// The driver and the vector type are obvious. The generated package is here for
// a subtler reason: sqlc's output is a set of Go structs that look exactly like
// domain types, and a service that starts passing them around has swapped its
// domain model for its schema without anyone deciding to.
var adapterOnly = []string{
	"github.com/jackc/pgx",
	"github.com/pgvector/pgvector-go",
	modulePath + "/src/storage/postgres",
}

// mayImportTheAdapter is the allow-list, and it is deliberately two entries.
//
// The adapter package itself, obviously. And the composition root, because
// something has to construct the concrete store — that is what a composition
// root is for, and the alternative is a reflective registry that hides the same
// edge behind more machinery.
func mayImportTheAdapter(path string) bool {
	return strings.HasPrefix(path, filepath.Join("src", "storage", "postgres")+string(filepath.Separator)) ||
		path == filepath.Join("src", "app", "container.go")
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
	if mayImportTheAdapter(relative) {
		return
	}

	for _, imported := range file.Imports {
		importPath := strings.Trim(imported.Path.Value, `"`)
		for _, banned := range adapterOnly {
			if strings.HasPrefix(importPath, banned) {
				t.Errorf("%s imports %q — only src/storage/postgres/** and src/app/container.go may",
					relative, importPath)
			}
		}
	}
}
