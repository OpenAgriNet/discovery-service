package discover

import (
	"fmt"
	"strings"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/domain"
	"github.com/OpenAgriNet/discovery-service/src/platform/jsonpath"
)

// filterGrammar is the one value `filters.type` may name.
//
// Empty is accepted as the same thing: PostgreSQL SQL/JSON path is the only
// grammar this service executes (C10), so an absent type cannot be ambiguous.
// Any other value is DECLINED rather than ignored — a caller who says
// "rfc9535" and is served jsonpath gets an answer to a question they did not
// ask, and RFC 9535 is close enough to the accepted spelling that they would
// not notice.
const filterGrammar = "jsonpath"

// filtersPath is where a fault about the filter points. Spelled the way every
// other fault in this package spells a location.
const filtersPath = "$['message']['intent']['filters']"

// mapFilters turns Intent.filters into the query's attribute filter, or into
// the reason there will not be one.
//
// Every refusal here is a shape PostgreSQL runs happily and answers wrongly, so
// none of them can be left to the database. `narrowedElsewhere` says whether
// some other predicate has already reduced the corpus, which is the difference
// between an unindexable filter costing one slow query and costing a scan of
// the entire catalogue.
//
// It returns a SLICE for a single wire filter because the domain's shape
// outlives this version of the protocol: `Intent.filters` is one object today,
// and a query that carries filters as a list needs no domain change if that
// becomes an array. Combining two expressions, however, would not be a domain
// change either — it would be text assembly, which is the thing this service
// refuses to do to a jsonpath.
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

	// The gate, not a rewriter: what survives is handed to `@filter::jsonpath`
	// verbatim, and PostgreSQL's own parser stays the last word on syntax.
	if err := jsonpath.Accept(expression); err != nil {
		return nil, filterFault("expression", beckn.CodeSchemaInvalidJSONPath, err.Error())
	}

	// The same posture as MaxRadiusMeters, and for the same reason: an
	// unbounded read is refused rather than served, because the caller cannot
	// see the cost and the deployment pays it. GIN extracts equality and
	// nothing else, so an expression built only from inequality, `like_regex`
	// or `starts with` reads every gated row — which is ordinary beside a text
	// search or a spatial constraint that has already cut the corpus down, and
	// is a scan of the whole catalogue when it arrives alone.
	if !narrowedElsewhere && !jsonpath.HasIndexableEquality(expression) {
		return nil, filterFault("expression", beckn.CodeSchemaInvalidFormat,
			"this expression narrows nothing the index can serve — inequality, like_regex "+
				"and starts with are answered by reading every row — and the request "+
				"carries no text search, spatial constraint or schemaContext to narrow "+
				"it first; add one, or compare with ==")
	}

	return []domain.AttributeFilter{{Expression: expression}}, nil
}

// filterFault points a refusal at one member of `filters`.
//
// Every refusal in this file is fatal and singular, so the shape is always the
// same one — and spelling it out four times is four chances for one of them to
// point somewhere the caller cannot find.
func filterFault(member string, code beckn.ErrorCode, message string) []domain.Fault {
	return []domain.Fault{{
		Path:    filtersPath + "['" + member + "']",
		Code:    string(code),
		Message: message,
	}}
}
