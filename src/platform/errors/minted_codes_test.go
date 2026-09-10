package errors

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	repoRoot    = "../../.."
	becknSource = repoRoot + "/src/beckn"
)

// The constructor each family's codes must be built through, so a SCH_ code
// reported as a context fault is caught rather than merely unlikely. The six
// bodies are identical, which is exactly why nothing else can catch it: picking
// the wrong one changes no behaviour at the call site and every behaviour on
// the wire, because Status and error_type are both derived from the code.
var familyOf = map[string]string{
	"Context":  "CTX",
	"Auth":     "AUT",
	"Schema":   "SCH",
	"Network":  "NET",
	"Business": "BIZ",
	"Policy":   "POL",
}

// The `MUST` the schema could not encode, encoded where it actually binds.
//
// src/beckn asserts that every declared ErrorCode constant is a member of the
// fixture's enum. That pins the constants and nothing else: beckn.ErrorCode is
// a string type, so an untyped literal converts to it implicitly and
// Schema("SCH_MADE_UP", …) compiles, ships, and is a Level 1 code the spec
// forbids — with no constant anywhere for the other test to find.
//
// So the two halves have to meet: a code this service mints is a declared
// constant, and the declared constants are enum members. Neither test is worth
// much without the other.
//
// Test files are exempt deliberately. XYZ_FROM_THE_FUTURE and
// DOM_OCPI_SESSION_REJECTED exist only in tests, and both are the point of the
// test they appear in — one proves an unknown prefix is still attributed, the
// other proves a relayed Level 2 code is passed through. Neither is minted.
func TestEveryMintedCodeIsADeclaredConstant(t *testing.T) {
	declared := beckenErrorCodes(t)

	checked := 0
	walkProductionSource(t, func(file *ast.File, fset *token.FileSet) {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			family, ok := familyOf[calleeName(call.Fun)]
			if !ok {
				return true
			}

			checked++
			where := fset.Position(call.Pos())
			name, ok := constantName(call.Args[0])
			if !ok {
				t.Errorf("%s: the code is not a beckn.Code constant; a Level 1 code "+
					"this service mints must be one, or the enum pin in src/beckn "+
					"never sees it", where)
				return true
			}
			value, exists := declared[name]
			if !exists {
				t.Errorf("%s: beckn.%s is not a declared ErrorCode constant", where, name)
				return true
			}
			if prefix, _, _ := strings.Cut(value, "_"); prefix != family {
				t.Errorf("%s: %s reported through the %s_ constructor", where, value, family)
			}
			return true
		})
	})

	// A walk that found nothing would keep passing while the rule it holds
	// quietly stopped being held — the same failure the src/beckn walk guards
	// against by counting its constants.
	if checked == 0 {
		t.Fatal("no constructor call sites found; the walk is checking nothing")
	}
}

// The other half of the same door. A relayed Level 2 code is converted from a
// value that arrived over the network, never from a literal — so a literal
// conversion in production source is a code being invented under cover of the
// escape hatch that exists for relaying one.
func TestNoProductionCodeConvertsALiteralToAnErrorCode(t *testing.T) {
	walkProductionSource(t, func(file *ast.File, fset *token.FileSet) {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || calleeName(call.Fun) != "ErrorCode" || len(call.Args) != 1 {
				return true
			}
			if literal, ok := call.Args[0].(*ast.BasicLit); ok && literal.Kind == token.STRING {
				t.Errorf("%s: beckn.ErrorCode(%s) invents a code; the conversion is "+
					"for relaying one that arrived, not for writing one",
					fset.Position(call.Pos()), literal.Value)
			}
			return true
		})
	})
}

// beckenErrorCodes reads package beckn's ErrorCode constants, keyed by Go name.
// Parsed rather than reflected over for the reason src/beckn's own walk gives:
// Go cannot enumerate a package's constants at run time.
func beckenErrorCodes(t *testing.T) map[string]string {
	t.Helper()

	fset := token.NewFileSet()
	packages, err := parser.ParseDir(fset, beckenSourceDir(t), func(file os.FileInfo) bool {
		return !strings.HasSuffix(file.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package beckn: %v", err)
	}

	codes := map[string]string{}
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				spec, ok := node.(*ast.ValueSpec)
				if !ok {
					return true
				}
				if name, ok := spec.Type.(*ast.Ident); !ok || name.Name != "ErrorCode" {
					return true
				}
				for i, name := range spec.Names {
					literal, ok := spec.Values[i].(*ast.BasicLit)
					if !ok {
						continue
					}
					if value, err := strconv.Unquote(literal.Value); err == nil {
						codes[name.Name] = value
					}
				}
				return true
			})
		}
	}
	if len(codes) == 0 {
		t.Fatal("package beckn declares no ErrorCode constants; the walk is checking nothing")
	}
	return codes
}

func beckenSourceDir(t *testing.T) string {
	t.Helper()

	if _, err := os.Stat(becknSource); err != nil {
		t.Fatalf("stat %s: %v", becknSource, err)
	}
	return becknSource
}

// walkProductionSource visits every non-test Go file in the repository.
func walkProductionSource(t *testing.T, visit func(*ast.File, *token.FileSet)) {
	t.Helper()

	fset := token.NewFileSet()
	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// path, not entry.Name(): the root is spelled ".." from here, and a
			// dotfile check against that name skips the whole repository — which
			// the vacuity guard below is what caught.
			name := filepath.Base(path)
			if path != repoRoot && (name == "bin" || name == "vendor" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		visit(file, fset)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repoRoot, err)
	}
}

// calleeName reads the function name off a call, qualified or not, so
// errors.Schema and a bare Schema inside this package are one case.
func calleeName(fun ast.Expr) string {
	switch named := fun.(type) {
	case *ast.Ident:
		return named.Name
	case *ast.SelectorExpr:
		return named.Sel.Name
	}
	return ""
}

// constantName returns the Go name behind a beckn.CodeXxx reference.
func constantName(arg ast.Expr) (string, bool) {
	selector, ok := arg.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != "beckn" || !strings.HasPrefix(selector.Sel.Name, "Code") {
		return "", false
	}
	return selector.Sel.Name, true
}
