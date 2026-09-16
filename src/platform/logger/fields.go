package logger

import (
	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// Fields projects a request's observed facts onto the zap fields the log line
// carries.
//
// This is the log half of the seam (opentelemetry.md §The seam), and it lives HERE rather
// than beside the span projection because the log field names are already
// spelled in this package's constructors and nowhere else. A projection
// elsewhere would be a second file claiming the same spellings. What the seam
// promises is one place to change an attribute — the registry TABLE, not one
// projection file per signal.
//
// It is also what tests/architecture/boundary_test.go enforces: this package may
// import fact and may not import src/platform/telemetry, so moving this file
// there and calling it from here is the one relocation that breaks the build.
// See the exemption list's comment for why request_logger.go is not on it.
//
// Only keys whose Definition carries the Log signal are projected. From 23c the
// record also carries the span's attributes, and writing everything found would
// silently turn one completion line into the whole span.
//
// A nil record projects nothing rather than panicking. The probes chain in
// router.go allocates no record deliberately, so a panic in /healthz arrives
// here with nothing to project and must not panic again inside the recovery
// answering the first.
func Fields(record *fact.Record) []zap.Field {
	if record == nil {
		return nil
	}

	// Six is the completion line today: three correlators, status, duration and
	// sometimes error_type.
	fields := make([]zap.Field, 0, 6)

	for observation := range record.All() {
		def := fact.Of(observation.Key)
		if def.Signals&fact.Log == 0 || def.LogKey == "" {
			continue
		}

		// Definition.Kind, not Observation.Kind, chooses the constructor: the two
		// cannot disagree — Record.requireKind refuses a mismatched write — so the
		// question is which is authoritative, and the registry is. fields_test.go
		// asserts every Kind the Log column uses is handled here, because a Kind
		// this switch does not know drops the field, and a dropped field is
		// indistinguishable from a request that never had one.
		switch def.Kind {
		case fact.KindString:
			fields = append(fields, zap.String(def.LogKey, observation.Text))
		case fact.KindInt64:
			fields = append(fields, zap.Int64(def.LogKey, observation.Int))
		case fact.KindFloat64:
			fields = append(fields, zap.Float64(def.LogKey, observation.Float))
		case fact.KindBool:
			fields = append(fields, zap.Bool(def.LogKey, observation.Bool))
		case fact.KindStrings:
			fields = append(fields, zap.Strings(def.LogKey, observation.List))
		}
	}
	return fields
}
