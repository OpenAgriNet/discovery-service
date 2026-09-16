package discover_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// The mapper's faults reach the wire as typed AppErrors, and `typed` in
// service.go is the switch that does it. A code the mapper mints and that
// switch does not name becomes a 500 — loud rather than wrong, but still not
// the caller's fault reported back to them.
//
// So the two files are compared. This is syntactic on purpose: it asks whether
// every beckn.Code constant intent_mapper.go NAMES is also named in service.go,
// which needs no value resolution and cannot be satisfied by remembering.
//
// Modelled on TestEveryMintedCodeIsADeclaredConstant in src/platform/errors,
// and for the same reason: a family constructor reached through a variable is
// invisible to that walk, so the switch has to exist, and something has to keep
// it complete.
func TestEveryCodeTheMapperMintsIsTyped(t *testing.T) {
	minted := becknCodesIn(t, "intent_mapper.go")
	if len(minted) == 0 {
		t.Fatal("no beckn.Code constants found in intent_mapper.go; the walk is broken, not the mapper")
	}

	handled := becknCodesIn(t, "service.go")
	for _, code := range minted {
		if !slices.Contains(handled, code) {
			t.Errorf("intent_mapper.go mints beckn.%s and service.go's `typed` does not name it, "+
				"so that fault reaches the caller as a 500", code)
		}
	}
}

// becknCodesIn returns the names of every `beckn.CodeXxx` selector in one file
// of this package.
func becknCodesIn(t *testing.T, filename string) []string {
	t.Helper()

	parsed, err := parser.ParseFile(token.NewFileSet(), filename, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", filename, err)
	}

	var found []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "beckn" || !strings.HasPrefix(selector.Sel.Name, "Code") {
			return true
		}
		if !slices.Contains(found, selector.Sel.Name) {
			found = append(found, selector.Sel.Name)
		}
		return true
	})
	return found
}
