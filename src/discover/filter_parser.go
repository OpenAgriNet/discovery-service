package discover

import (
	"fmt"
	"strings"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/platform/jsonpath"
)

// filterGrammar is the one value `filters.type` may name; empty reads as the
// same thing (C10). Any other value is DECLINED rather than ignored — RFC 9535
// is close enough to this spelling that a caller served jsonpath instead would
// not notice the answer is to a different question.
const filterGrammar = "jsonpath"

// filtersPath is where a fault about the filter points.
const filtersPath = "$['message']['intent']['filters']"

// mapFilters turns Intent.filters into the query's attribute filter, or into
// the reason there will not be one.
//
// Every refusal here is a shape PostgreSQL runs happily and answers wrongly, so
// none can be left to the database. `narrowedElsewhere` says whether some other
// predicate has already cut the corpus down.
//
// It returns a SLICE for a single wire filter so that a `filters` array needs no
// domain change. Combining two expressions would not be a domain change either —
// it would be text assembly, which is what this service refuses to do to a
// jsonpath.
func mapFilters(filters *beckn.Filters, narrowedElsewhere bool) ([]domain.AttributeFilter, []domain.Fault) {
	if filters == nil {
		return nil, nil
	}

	if grammar := strings.ToLower(strings.TrimSpace(filters.Type)); grammar != "" && grammar != filterGrammar {
		return nil, filterFault("type", beckn.CodeSchemaTypeNotSupported, fmt.Sprintf(
			"filters.type %q is not executed here; this service runs PostgreSQL "+
				"SQL/JSON path only, which is %q", filters.Type, filterGrammar))
	}

	expression := strings.TrimSpace(filters.Expression)
	if expression == "" {
		return nil, filterFault("expression", beckn.CodeSchemaInvalidJSONPath,
			"filters is present with an empty expression; a filter that narrows nothing "+
				"and a filter that was never sent are different requests, and only one "+
				"of them is this one")
	}

	// A gate, not a rewriter: what survives is handed to `@filter::jsonpath`
	// verbatim, and PostgreSQL's own parser stays the last word on syntax.
	if err := jsonpath.Accept(expression); err != nil {
		return nil, filterFault("expression", beckn.CodeSchemaInvalidJSONPath, err.Error())
	}

	// The same posture as MaxRadiusMeters: an unbounded read is refused rather
	// than served, because the caller cannot see the cost and the deployment
	// pays it. jsonb_path_ops extracts equality and nothing else — see the
	// plan's attribute-filter table (A18, Task 22).
	if !narrowedElsewhere && !jsonpath.HasIndexableEquality(expression) {
		return nil, filterFault("expression", beckn.CodeSchemaInvalidFormat,
			"this expression narrows nothing the index can serve — inequality, like_regex "+
				"and starts with are answered by reading every row — and the request "+
				"carries no text search, spatial constraint or schemaContext to narrow "+
				"it first; add one, or compare with ==")
	}

	return []domain.AttributeFilter{{Expression: expression}}, nil
}

// filterFault points a refusal at one member of `filters`. Every refusal here is
// fatal and singular, so the shape is always this one.
func filterFault(member string, code beckn.ErrorCode, message string) []domain.Fault {
	return []domain.Fault{{
		Path:    filtersPath + "['" + member + "']",
		Code:    string(code),
		Message: message,
	}}
}
