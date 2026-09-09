package logger

import (
	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// Fields projects a request's observed facts onto the zap fields the log line
// carries.
//
// This is the log half of the seam (telemetry-seam.md:130-136), and it lives in
// this package rather than beside the span projection on purpose: the log field
// names are already spelled here, in the constructors below, and nowhere else.
// Putting the projection anywhere else would make a second file that claims the
// same spellings, which is the drift the constructors exist to prevent. What the
// seam promises is one place to change an attribute — that place is the registry
// TABLE, not one projection file per signal.
//
// It is also the arrangement tests/architecture/boundary_test.go enforces: this
// package may import fact and may not import src/platform/telemetry, so moving
// this file there and calling it from here is the one relocation that breaks the
// build. See the exemption list's comment for why request_logger.go is not on it.
//
// Only keys whose Definition carries the Log signal are projected. From 23c the
// record also carries the span's attributes — beckn.version, beckn.networkId and
// a dozen more — and writing everything found would silently turn one completion
// line into the whole span on every deployment.
//
// A nil record projects nothing rather than panicking. The probes chain
// (router.go:158-163) allocates no record deliberately, so a panic in /healthz
// arrives here with nothing to project and must not panic a second time inside
// the recovery that was answering the first.
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

		// Definition.Kind, not Observation.Kind, chooses the constructor. The two
		// cannot disagree — Record.requireKind refuses a mismatched write — so the
		// choice is about which of the two is authoritative, and the registry is.
		// fields_test.go asserts every Kind the Log column uses is handled
		// here, because a Kind this switch does not know drops the field, and a
		// dropped log field is indistinguishable from a request that never had one.
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
