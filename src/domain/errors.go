package domain

// Fault is one thing wrong with a request, named by the domain.
//
// It lives here rather than in platform/errors because UpsertCatalog returns
// faults ACROSS the port, and this package may import neither `beckn` nor
// `platform/errors` — purity_test.go fails the build if it tries.
//
// The domain NAMES a fault; exactly one place turns it into wire bytes. That is
// the DRY rule on error construction, held rather than restated: a domain that
// built Beckn errors would be a second error vocabulary to keep in step with
// the first.
//
// A value, not an error. A fault is aggregated rather than returned — a
// publish reporting three bad geometries reports three — and something that
// travels in a slice has no business also satisfying `error`, where the first
// `return err` would drop the other two.
type Fault struct {
	// Path is a JSONPath into the request that carried the fault.
	Path string

	// Code is the DOM_ or BIZ_ string the wire layer maps to a Beckn error.
	Code string

	Message string
}
