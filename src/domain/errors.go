package domain

// Fault is one thing wrong with a request, named by the domain.
//
// A value, not an error: faults are aggregated — a publish reporting three bad
// geometries reports three — and the first `return err` would drop the rest.
type Fault struct {
	// Path is a JSONPath into the request that carried the fault.
	Path string

	// Code is the DOM_ or BIZ_ string the wire layer maps to a Beckn error.
	Code string

	Message string
}
