package domain

import (
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// allowedNonStdlib is every third-party import this package may hold.
//
// One entry, and it is deliberate: google/uuid mints identifiers, which is a
// value-level concern the domain is allowed to have. Anything else — a driver,
// a protocol type, a logger, an HTTP client — is the leak this test exists to
// stop.
var allowedNonStdlib = map[string]bool{
	"github.com/google/uuid": true,
}

// isStdlib reports whether an import path is from the standard library.
//
// The first segment of a stdlib path never contains a dot; every module path
// begins with a hostname, which always does. That is the same rule the go
// command uses to tell them apart, and it needs neither the toolchain nor the
// network to apply.
func isStdlib(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

// TestTheDomainImportsNothingButTheStandardLibrary is the swap boundary,
// enforced rather than requested (TRD §5, T7).
//
// Every backend is written against these types, so the moment one of them
// mentions pgx the "replace the store" story stops being true — and it stops
// being true silently, because the code still compiles and every test still
// passes. This is the test that fails instead.
//
// Non-test files only. The contract is about what this package exposes to the
// backends written against it; a test file reaching for a helper library
// constrains nobody.
func TestTheDomainImportsNothingButTheStandardLibrary(t *testing.T) {
	fileSet := token.NewFileSet()
	packages, err := parser.ParseDir(fileSet, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse the domain package: %v", err)
	}

	for _, parsed := range packages {
		for name, file := range parsed.Files {
			for _, imported := range file.Imports {
				path := strings.Trim(imported.Path.Value, `"`)
				if isStdlib(path) || allowedNonStdlib[path] {
					continue
				}
				t.Errorf("%s imports %q; the domain may import only the standard library and %v",
					name, path, keys(allowedNonStdlib))
			}
		}
	}
}

func keys(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	return names
}
