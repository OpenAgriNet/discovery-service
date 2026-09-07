package acceptance

import (
	"net/http"
	"slices"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// Scenario 18. A structured filter narrows the result, and the spelling that
// looks like it is refused.
//
// The two halves belong in one scenario because the defect is that they LOOK
// ALIKE. `? (@.x == "y")` and `[?(@.x == 'y')]` differ by one bracket and one
// quote style; the second is RFC 9535, which every JSONPath tutorial teaches
// and PostgreSQL does not speak (C10). Run as written it returns an empty page
// — a plausible answer, indistinguishable from a filter that matched nothing —
// so the assertion that matters is not that the good one works but that the
// near-miss cannot be mistaken for it.
//
// The third half the plan first wrote — the same filter against a backend
// declaring no `jsonpath` capability, answered with X-Beckn-Degraded — is gone
// with A18: PostgreSQL executes the subset, so there is no configuration of
// this service that degrades it. What replaces it is the memory backend's
// Capabilities, which declares the filter absent, and the conformance suite's
// note on why no case there names it.

// filters is the intent member scenario 18 is about.
func filters(grammar, expression string) map[string]any {
	return map[string]any{"filters": map[string]any{"type": grammar, "expression": expression}}
}

// twoManufacturers is one catalog whose resources differ in exactly the leaf
// the filter names, and in nothing else.
//
// One catalog, not two. Across two catalogs the filter passes by reaching the
// right ROW — the case that works whatever the composite holds. Siblings under
// one catalog are the case A18 was measured against: a composite carrying the
// catalog's every resource answers this predicate for the neighbour's value and
// returns both.
func twoManufacturers(t *testing.T) *service {
	t.Helper()

	svc := newService(t)
	svc.publishCatalogs(t, aCatalog("c1", resources(
		aResource("r-hul", "Soap", withAttributes(map[string]any{
			"packagedGoodsDeclaration": map[string]any{
				"manufacturerOrPacker": map[string]any{"name": "Hindustan Unilever Limited"},
			},
		})),
		aResource("r-other", "Soap", withAttributes(map[string]any{
			"packagedGoodsDeclaration": map[string]any{
				"manufacturerOrPacker": map[string]any{"name": "Some Other Packer"},
			},
		})),
	)))
	return svc
}

const manufacturerFilter = `$.catalogs[*].resources[*] ? ` +
	`(@.resourceAttributes.packagedGoodsDeclaration.manufacturerOrPacker.name == "Hindustan Unilever Limited")`

func TestAStructuredFilterNarrowsTheResult(t *testing.T) {
	svc := twoManufacturers(t)

	got := resourceIDs(svc.discover(t, filters("jsonpath", manufacturerFilter)))
	if !slices.Equal(got, []string{"r-hul"}) {
		t.Errorf("the manufacturer filter returned %v, want [r-hul] alone — r-other shares "+
			"the catalog and differs only in the leaf the filter names, so both is what "+
			"a filter that ran against the wrong value returns", got)
	}
}

// The RFC 9535 spelling of the SAME filter is a 400, not an empty page.
//
// Refused at the edge rather than sent to PostgreSQL, because PostgreSQL does
// not refuse it either: `[?(...)]` parses, selects nothing, and `@?` reports no
// error. The caller would receive 200 and an empty catalogs array, and would
// conclude their corpus has no Hindustan Unilever listing.
func TestTheRFC9535SpellingOfTheSameFilterIsRefused(t *testing.T) {
	svc := twoManufacturers(t)

	rfc9535 := `$.catalogs[*].resources[?(` +
		`@.resourceAttributes.packagedGoodsDeclaration.manufacturerOrPacker.name == 'Hindustan Unilever Limited')]`

	response := svc.discoverResponse(t, filters("jsonpath", rfc9535))
	if response.status != http.StatusBadRequest {
		t.Fatalf("the RFC 9535 spelling answered %d, want 400 — it selects nothing and "+
			"reports no error, so a caller who is served it learns their filter matched "+
			"no listing rather than that it was never run", response.status)
	}

	if got := response.nack(t).Message.Error.Code; got != beckn.CodeSchemaInvalidJSONPath {
		t.Errorf("the refusal is %s, want %s", got, beckn.CodeSchemaInvalidJSONPath)
	}
}

// A filter the edge gate lets through and PostgreSQL cannot parse is the same
// 400, from the other side of the query.
//
// The expression is the manufacturer filter above with ONE character removed:
// the dot between `catalogs[*]` and `resources[*]`. It passes the gate in front
// of the query because the gate is deliberately not a parser — it settles the
// root and the predicate form, both of which this has — and PostgreSQL then
// refuses the cast from inside the search, where every other failure is the
// deployment's fault and a 500.
//
// A 500 is the wrong answer twice over. It tells the caller to retry a request
// that can never succeed, and it puts a client typo in the log line that pages
// whoever is on call.
func TestAFilterPostgreSQLCannotParseIsTheCallersFaultNotAFiveHundred(t *testing.T) {
	svc := twoManufacturers(t)

	malformed := `$.catalogs[*]resources[*] ? ` +
		`(@.resourceAttributes.packagedGoodsDeclaration.manufacturerOrPacker.name == "Hindustan Unilever Limited")`

	response := svc.discoverResponse(t, filters("jsonpath", malformed))
	if response.status != http.StatusBadRequest {
		t.Fatalf("a filter PostgreSQL cannot parse answered %d, want 400 — the dropped "+
			"dot is the caller's to fix, and a 5xx asks them to retry it unchanged",
			response.status)
	}

	fault := response.nack(t).Message.Error
	if fault.Code != beckn.CodeSchemaInvalidJSONPath {
		t.Errorf("the refusal is %s, want %s — the same code the edge gate uses, because "+
			"which side of the query noticed is not a distinction the caller can act on",
			fault.Code, beckn.CodeSchemaInvalidJSONPath)
	}
	if fault.Details == nil || fault.Details.Path != "$.message.intent.filters.expression" {
		t.Errorf("the refusal points at %+v, want $.message.intent.filters.expression",
			fault.Details)
	}
	if fault.Message != domain.ErrInvalidFilterExpression.Error() {
		t.Errorf("the refusal reads %q, want %q — the operation that was running and the "+
			"SQLSTATE belong in the operator's log, not in the answer",
			fault.Message, domain.ErrInvalidFilterExpression.Error())
	}
}
