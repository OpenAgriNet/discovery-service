package validation

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	apperrors "github.com/OpenAgriNet/discovery-service/src/platform/errors"
)

// rootPath is where a fault lands when nothing narrower is known about it.
const rootPath = "$"

// L1 validates the whole envelope against the schema the published document
// declares for this action, and reports every fault it finds.
//
// The published beckn.yaml is the validator. Nothing here restates a rule the
// document already carries, which is the point: a hand-rolled second copy of
// the protocol is a copy that drifts, and it drifts silently because both
// copies keep passing their own tests.
//
// Every fault comes back, each against its own path, for a caller to chain
// through details.cause (C7). Stopping at the first would make the chain
// unreachable from the layer that produces the most faults.
func L1(index *SpecIndex, action string, body []byte) []*apperrors.AppError {
	entry, found := index.lookup(action)
	if !found {
		// The plan's pin: an unknown action NACKs rather than 500s. There is no
		// schema to hold the body against, so the absence is the whole fault,
		// and it is reported against the field that carried it.
		return []*apperrors.AppError{apperrors.Context(beckn.CodeContextActionMismatch,
			"this service serves no action named "+action).At(rootPath + ".context.action")}
	}

	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		// Envelope refuses this above, but L1 is callable without it and a
		// validator that panics on a body it was handed is a 500 where a 400
		// belongs.
		return []*apperrors.AppError{
			apperrors.Schema(beckn.CodeSchemaInvalidJSON, "request body is not readable JSON").At(rootPath),
		}
	}

	canonicaliseAction(document, entry.canonical)

	if err := entry.schema.Value.VisitJSON(document, openapi3.MultiErrors()); err != nil {
		return schemaFaults(document, err)
	}
	return nil
}

// canonicaliseAction rewrites `$.context.action` to the spelling the schema
// constrains it to, in the map L1 decoded for itself.
//
// C2 says `publish` and `catalog/publish` are one request; the document says
// the field equals `catalog/publish` (A12). Reconciling them here rather than editing
// the published document keeps that document the single source of truth — and
// doing it in a freshly decoded map rather than in the buffered request body
// keeps the bytes this service signs, hashes and audits identical to the ones
// the caller sent.
//
// An absent action is left absent. Minting one would hide the omission from the
// envelope rules, which are the only pass that can report it (C6).
func canonicaliseAction(document any, canonical string) {
	envelope, isObject := document.(map[string]any)
	if !isObject {
		return
	}
	context, isObject := envelope["context"].(map[string]any)
	if !isObject {
		return
	}
	if _, present := context["action"]; present {
		context["action"] = canonical
	}
}

// schemaFaults turns what the visitor reported into one fault per problem.
func schemaFaults(document any, err error) []*apperrors.AppError {
	var multi openapi3.MultiError
	if !errors.As(err, &multi) {
		return []*apperrors.AppError{faultFrom(document, err)}
	}

	faults := make([]*apperrors.AppError, 0, len(multi))
	for _, reported := range multi {
		faults = append(faults, faultFrom(document, reported))
	}
	return faults
}

// faultFrom renders one visitor error as a fault against the path it names.
func faultFrom(document any, err error) *apperrors.AppError {
	var reported *openapi3.SchemaError
	if !errors.As(err, &reported) {
		return schemaFault(rootPath, err.Error())
	}

	reason := reported.Reason
	if reason == "" {
		// Rare, and the field it would have explained is still named by the
		// path, so a caller is not left with nothing.
		reason = "does not satisfy the schema"
	}
	return schemaFault(renderPath(document, reported.JSONPointer()), reason)
}

// schemaFault builds the one fault every L1 rejection is. The code is named
// here as a literal rather than taken as a parameter, so the minted-codes pin
// in src/platform/errors can see it — a code that reaches a family constructor
// through a variable is invisible to that walk, and the walk is what keeps a
// SCH_ code from being reported as a CTX_ one.
func schemaFault(path, message string) *apperrors.AppError {
	return apperrors.Schema(beckn.CodeSchemaValidationFailed, message).At(path)
}

// renderPath turns the visitor's pointer segments into the JSONPath the fault
// is reported at.
//
// The document is walked alongside the segments because a segment alone cannot
// say whether it is a member name or an array index — the visitor reports both
// as bare strings, and a catalog keyed "0" is a real shape. Only the value at
// that point knows, so only a walk can render `spatial[0]` rather than
// `spatial.0`, which is not a path any tool the caller owns will evaluate.
func renderPath(document any, segments []string) string {
	var path strings.Builder
	path.WriteString(rootPath)

	node := document
	for _, segment := range segments {
		rendered, next := descend(node, segment)
		path.WriteString(rendered)
		node = next
	}
	return path.String()
}

// descend renders one segment against the node it indexes, and returns the node
// it selects. A segment that selects nothing yields nil, and the remaining
// segments render as member names — the fault still names a path, which is
// better than truncating it at the point the walk lost its footing.
func descend(node any, segment string) (string, any) {
	if array, isArray := node.([]any); isArray {
		return "[" + segment + "]", elementAt(array, segment)
	}

	object, isObject := node.(map[string]any)
	if !isObject {
		return member(segment), nil
	}
	return member(segment), object[segment]
}

func elementAt(array []any, segment string) any {
	index, err := strconv.Atoi(segment)
	if err != nil || index < 0 || index >= len(array) {
		return nil
	}
	return array[index]
}

// member renders a member name, in bracket notation where the shorthand cannot
// carry it. `@type` and `beckn:id` are both real keys in this protocol and
// neither is a legal shorthand, so `$.message.@type` would be a path that reads
// fine and evaluates to nothing.
func member(name string) string {
	if isShorthand(name) {
		return "." + name
	}
	return "['" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(name) + "']"
}

// isShorthand reports whether name is an RFC 9535 member-name-shorthand.
func isShorthand(name string) bool {
	if name == "" {
		return false
	}

	for offset, char := range name {
		alphabetic := char == '_' || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
		if alphabetic || (offset > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}
