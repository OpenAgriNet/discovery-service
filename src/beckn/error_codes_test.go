package beckn

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The enum constraint the schema could not express, expressed here instead.
//
// `Error.code` is declared `type: string`, not `$ref: ErrorCode`, so L1
// validation accepts any string and the property-name walk in
// schema_conformance_test.go compares names rather than values. The only
// normative statement is prose in Error's own description — "The topmost
// (Level 1) Error in any payload MUST carry a code from the canonical Beckn
// error code enum" — and this test is what turns it into something that fails.
//
// Level 2 is deliberately not checked, and cannot be: the same sentence says a
// details.cause "MAY carry domain-specific or non-canonical error codes from
// downstream systems". A relayed DOM_ code is a string this service passes
// through, not a constant it declares, so it never reaches this list.
func TestEveryErrorCodeIsAMemberOfTheEnum(t *testing.T) {
	members := errorCodeEnum(t)
	declared := declaredErrorCodes(t)

	// A parser change that stopped finding constants would make every
	// assertion below vacuous, and the test would keep passing while the pin
	// it exists to hold quietly stopped holding.
	if len(declared) == 0 {
		t.Fatalf("no ErrorCode constants found in package beckn; the walk is checking nothing")
	}

	for name, value := range declared {
		if !members[value] {
			t.Errorf("%s = %q, which is not a member of the ErrorCode enum in %s; "+
				"Level 1 codes are closed, so map it onto a member that exists",
				name, value, specFixture)
		}
	}
}

// errorCodeEnum reads the fixture's ErrorCode members.
func errorCodeEnum(t *testing.T) map[string]bool {
	t.Helper()

	node := resolve(t, loadSpec(t), schemaPath("ErrorCode"))
	values, ok := node["enum"].([]any)
	if !ok || len(values) == 0 {
		t.Fatalf("ErrorCode: declares no enum; the fixture is not the document this package was written against")
	}

	members := make(map[string]bool, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("ErrorCode: enum member %v is not a string", value)
		}
		members[text] = true
	}
	return members
}

// declaredErrorCodes returns every constant in this package typed ErrorCode,
// keyed by its Go name. The source is parsed rather than reflected over for the
// same reason TestEveryWireStructIsBound parses it: Go cannot enumerate a
// package's constants at run time, so a table written by hand here would be one
// more thing to remember to update — which is the failure the test exists to
// prevent.
func declaredErrorCodes(t *testing.T) map[string]string {
	t.Helper()

	packages, err := parser.ParseDir(token.NewFileSet(), ".", func(file os.FileInfo) bool {
		return !strings.HasSuffix(file.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package beckn: %v", err)
	}

	codes := map[string]string{}
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			collectErrorCodes(t, file, codes)
		}
	}
	return codes
}

func collectErrorCodes(t *testing.T, file *ast.File, into map[string]string) {
	t.Helper()

	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		// The type is written on every constant in the block rather than
		// inherited from the first, so a walk this shallow sees all of them.
		if name, ok := spec.Type.(*ast.Ident); !ok || name.Name != "ErrorCode" {
			return true
		}
		for i, name := range spec.Names {
			literal, ok := spec.Values[i].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				t.Fatalf("%s: an ErrorCode constant that is not a string literal", name.Name)
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatalf("%s: %v", name.Name, err)
			}
			into[name.Name] = value
		}
		return true
	})
}

// Two names for one code is two call sites that read differently and log
// identically, and the pair only ever diverges by one being updated.
func TestNoErrorCodeIsDeclaredTwice(t *testing.T) {
	seen := map[string]string{}
	for name, value := range declaredErrorCodes(t) {
		if other, duplicate := seen[value]; duplicate {
			t.Errorf("%s and %s are both %q; one code, two names", name, other, value)
		}
		seen[value] = name
	}
}
