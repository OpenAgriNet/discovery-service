package telemetry

import (
	"strconv"

	"go.opentelemetry.io/otel/attribute"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// SpanAttributes projects a request's observed facts onto the span's attributes.
//
// This is the span half of the seam (telemetry-seam.md:130-136), and it lives
// here rather than in package fact for the reason that package's whole design
// rests on: fact's dependency set is a closed list of standard-library packages,
// pinned by tests/architecture/boundary_test.go, and that is what lets a
// controller observe a fact without linking the OpenTelemetry SDK. An
// attribute.KeyValue in there would link it into every importer at once.
//
// It returns a slice rather than setting them, so the caller decides WHEN — and
// the answer is once, at the end. Attributes set at span start would be set
// before the envelope has been parsed and there would be nothing to say.
//
// A nil record is not an error and does not project nothing: the probes chain
// (router.go:158-163) allocates no record deliberately, and a span with no
// sender.unidentified on it reads as a request whose sender was checked. Nil is
// "we observed nothing", which is exactly what the absent flags describe, so the
// absent pass below runs over it unchanged.
func SpanAttributes(record *fact.Record) []attribute.KeyValue {
	projection := newSpanProjection()
	for observation := range record.All() {
		projection.observe(observation)
	}
	projection.absent()
	return projection.attributes
}

// spanProjection accumulates the attributes across the two passes.
//
// A struct rather than a closure over locals because the flag dedupe has to
// outlive both passes: a row whose truncation flag fired in the first must not
// have its absent flag fire in the second under the same name.
type spanProjection struct {
	attributes []attribute.KeyValue
	observed   map[fact.Key]bool
	flagged    map[string]bool
}

func newSpanProjection() *spanProjection {
	return &spanProjection{
		// Twenty is what the worked example carries
		// (telemetry-examples.md:83-110): six http.*, seven beckn.*, the two
		// identities and their flags, the status under both spellings, and the
		// observed time.
		attributes: make([]attribute.KeyValue, 0, 20),
		observed:   make(map[fact.Key]bool),
		flagged:    make(map[string]bool),
	}
}

// flag emits a derived boolean once.
//
// Flags are named by the row, and two rows may name the same one:
// beckn.schemaContext and beckn.schemaType share beckn.schemaTruncated
// deliberately, because they are cut by one bound and staying the same length is
// the point of them. Emitting it per row would set the attribute twice and the
// exporter would keep one of the two with nothing saying which.
func (p *spanProjection) flag(name string) {
	if name == "" || p.flagged[name] {
		return
	}
	p.flagged[name] = true
	p.attributes = append(p.attributes, attribute.Bool(name, true))
}

// observe projects one observed fact, its aliases and its derived flags.
func (p *spanProjection) observe(observation fact.Observation) {
	def := fact.Of(observation.Key)
	if !onTheSpan(def) {
		return
	}
	p.observed[observation.Key] = true

	// Definition.Kind chooses the constructor, not Observation.Kind. The two
	// cannot disagree — Record.requireKind refuses a mismatched write — so this
	// is about which is authoritative, and the registry is.
	value := spanValue(def.Kind, observation)
	p.attributes = append(p.attributes, attribute.KeyValue{
		Key:   attribute.Key(def.SpanKey),
		Value: value,
	})

	// The aliases: one value under a second key, which is what makes them unable
	// to drift. Two of them exist and no more should — the cross-layer join
	// spellings onix's collectors key on (I1), and http.status.code as a string
	// beside http.status_code as an int (divergence 3).
	for _, alias := range def.SpanAliases {
		p.attributes = append(p.attributes, attribute.KeyValue{
			Key:   attribute.Key(alias.Key),
			Value: aliasValue(value, alias),
		})
	}

	// Clamped at the record, reported here. A short value that does not say it
	// was cut reads as a complete one, and somebody comparing the predicate on
	// the span against the predicate they sent concludes the service received
	// something else.
	if observation.Truncated {
		p.flag(def.TruncationFlag)
	}

	// sender.unverified, and note it is not the opposite of sender.unidentified:
	// this one says we were told who the caller is and did not check. Task 6 is
	// parked, so today it is on every identified sender — and when signature
	// verification lands it becomes conditional here rather than at every call
	// site.
	p.flag(def.PresentFlag)
}

// absent is the second pass, over the TABLE rather than the record, because its
// subject is the rows the record does not have. An operator can act on "we do
// not know who this was"; they cannot act on an attribute that is simply
// missing, because that is also what a span from an older build looks like.
func (p *spanProjection) absent() {
	for key, def := range fact.All() {
		if onTheSpan(def) && !p.observed[key] {
			p.flag(def.AbsentFlag)
		}
	}
}

// onTheSpan is the filter both passes share, and its two bits exclude different
// things. The Span bit drops the Log-only facts — duration_ms above all, which
// the span already answers with its own start and end, and a second answer is
// free to disagree with the first. NoEvent drops the facts that belong on span
// EVENTS: they are on the same record, and emitting them here as well would put
// every one of them on the span too.
//
// PromoteToSpan is the per-row exception to that second bit, not a softening of
// it — the row still ships on its event, and only the rows that declare the
// bool are copied here. See the field's own comment for what buys the
// duplication; fact.Validate refuses the bool on a row where it would mean
// nothing.
func onTheSpan(def fact.Definition) bool {
	return def.Signals&fact.Span != 0 && (def.Event == fact.NoEvent || def.PromoteToSpan)
}

// spanValue converts one observation to an attribute value.
//
// A Kind this switch does not know would emit an attribute with no value rather
// than dropping the row, which is why the default is the string form: an
// unreadable value is recoverable, a silently absent one is not. The registry's
// completeness test refuses KindUnspecified, so the default is unreachable
// through the front door.
func spanValue(kind fact.Kind, observation fact.Observation) attribute.Value {
	switch kind {
	case fact.KindString:
		return attribute.StringValue(observation.Text)
	case fact.KindInt64:
		return attribute.Int64Value(observation.Int)
	case fact.KindFloat64:
		return attribute.Float64Value(observation.Float)
	case fact.KindBool:
		return attribute.BoolValue(observation.Bool)
	case fact.KindStrings:
		return attribute.StringSliceValue(observation.List)
	default:
		return attribute.StringValue(observation.Text)
	}
}

// aliasValue renders the value under the alias's key.
//
// AsString exists for exactly one row: onix writes http.status.code as a string
// where the semantic conventions say http.status_code is an int, so both go out
// and a collector rule keyed on either finds it. strconv rather than fmt because
// the input is already known to be an int64 and a %v would render a non-int64
// Kind as something that looks deliberate.
func aliasValue(value attribute.Value, alias fact.Alias) attribute.Value {
	if !alias.AsString {
		return value
	}
	if value.Type() == attribute.INT64 {
		return attribute.StringValue(strconv.FormatInt(value.AsInt64(), 10))
	}
	return attribute.StringValue(value.String())
}
