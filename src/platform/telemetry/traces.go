package telemetry

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/OpenAgriNet/discovery-service/src/platform/buildinfo"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// SpanAttributes projects a request's observed facts onto the span's attributes
// — the span half of the seam (opentelemetry.md §Two packages, and why the
// split is load-bearing).
//
// It RETURNS a slice rather than setting them, so the caller decides when, and
// the answer is once at the end: attributes set at span start would be set
// before the envelope has been parsed.
//
// A nil record still projects the absent flags. Nil means "we observed nothing",
// which is what those flags describe — a span with no sender.unidentified on it
// would read as a request whose sender was checked.
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
		// (telemetry-examples.md §3. Span).
		attributes: make([]attribute.KeyValue, 0, 20),
		observed:   make(map[fact.Key]bool),
		flagged:    make(map[string]bool),
	}
}

// flag emits a derived boolean ONCE. Two rows may name the same flag —
// beckn.schemaContext and beckn.schemaType share beckn.schemaTruncated, because
// they are cut by one bound — and emitting it per row would set the attribute
// twice with nothing saying which copy the exporter kept.
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

	// Definition.Kind chooses the constructor, not Observation.Kind: the
	// registry is what is authoritative.
	value := spanValue(def.Kind, observation)
	p.attributes = append(p.attributes, attribute.KeyValue{
		Key:   attribute.Key(def.SpanKey),
		Value: value,
	})

	// Aliases are ONE value under a second key, which is what makes them unable
	// to drift. Two exist and no more should: the cross-layer join spelling
	// onix's collectors key on (I1), and http.status.code as a string beside
	// http.status_code as an int (divergence 3).
	for _, alias := range def.SpanAliases {
		p.attributes = append(p.attributes, attribute.KeyValue{
			Key:   attribute.Key(alias.Key),
			Value: aliasValue(value, alias),
		})
	}

	// Clamped at the record, reported here: a short value that does not say it
	// was cut reads as a complete one, and whoever compares the predicate on the
	// span against the one they sent concludes we received something else.
	if observation.Truncated {
		p.flag(def.TruncationFlag)
	}

	// sender.unverified — not the opposite of sender.unidentified. This one says
	// we were told who the caller is and did not check, so with Task 6 parked it
	// is on every identified sender.
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
// things: the Span bit drops the Log-only facts (duration_ms above all, which
// the span already answers from its own start and end), and NoEvent drops the
// facts that belong on span EVENTS. PromoteToSpan is the per-row exception to
// the second bit — the row still ships on its event as well.
func onTheSpan(def fact.Definition) bool {
	return def.Signals&fact.Span != 0 && (def.Event == fact.NoEvent || def.PromoteToSpan)
}

// spanValue converts one observation to an attribute value. The default is the
// string form rather than a dropped row: an unreadable value is recoverable, a
// silently absent one is not. The registry refuses KindUnspecified, so it is
// unreachable through the front door.
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
// AsString exists for exactly one row: the spec declares http.status.code an Int
// and emits it as a string in all three of its examples, and we follow the
// examples (opentelemetry.md §Divergences from the spec, row 3).
func aliasValue(value attribute.Value, alias fact.Alias) attribute.Value {
	if !alias.AsString {
		return value
	}
	if value.Type() == attribute.INT64 {
		return attribute.StringValue(strconv.FormatInt(value.AsInt64(), 10))
	}
	return attribute.StringValue(value.String())
}

// SpanEvent is one projected event: what happened, when, and the shape of it.
// Data rather than a call against a span, for the reason SpanAttributes returns
// a slice — and it is what lets this be tested without an exporter.
type SpanEvent struct {
	Name       string
	Time       time.Time
	Attributes []attribute.KeyValue
}

// SpanEvents projects the record's point-in-time facts onto span events, by the
// rule the registry encodes (opentelemetry.md §How the span learns the status):
// true for the whole request →
// span attribute; produced at a point during processing → event.
//
// An event whose facts were never observed is NOT emitted. An empty event
// stamped at the span's end would read as a phase that ran and produced nothing
// — a much more alarming claim than a phase that did not run. There is no
// absent-flag pass here for that reason.
func SpanEvents(record *fact.Record) []SpanEvent {
	building := make(map[fact.Event]*SpanEvent)

	for observation := range record.All() {
		def := fact.Of(observation.Key)
		if def.Signals&fact.Span == 0 || def.Event == fact.NoEvent {
			continue
		}

		event := building[def.Event]
		if event == nil {
			event = &SpanEvent{Name: def.Event.EventName(), Time: observation.Time}
			building[def.Event] = event
		}

		// The EARLIEST of its facts, not the latest. A phase happens over an
		// interval however instantaneous it looks — response_info is three
		// writes — and anchoring at the last of them slides every event toward
		// the span's end by however long its own work took, which is exactly
		// the interval the deltas between events measure.
		if observation.Time.Before(event.Time) {
			event.Time = observation.Time
		}

		event.Attributes = append(event.Attributes, attribute.KeyValue{
			Key:   attribute.Key(def.SpanKey),
			Value: spanValue(def.Kind, observation),
		})

		if observation.Truncated && def.TruncationFlag != "" {
			event.Attributes = append(event.Attributes,
				attribute.Bool(def.TruncationFlag, true))
		}
	}

	return ordered(building)
}

// ordered flattens the events into the order they happened in.
//
// Sorted rather than emitted in record order, and the case is not hypothetical:
// the error event is written from logNack, which on a partially-written response
// runs after the result facts were observed. It is also what makes the output
// deterministic at all, since map iteration order is randomised.
func ordered(building map[fact.Event]*SpanEvent) []SpanEvent {
	events := make([]SpanEvent, 0, len(building))
	for _, event := range building {
		events = append(events, *event)
	}
	slices.SortFunc(events, func(a, b SpanEvent) int { return a.Time.Compare(b.Time) })
	return events
}

// spanUUID stamps the spec's Required `span_uuid` on every span the service
// starts.
//
// A SpanProcessor rather than a line in the Trace middleware, so it cannot be
// forgotten by a later task's spans, and so the middleware stays a projection of
// a fact.Record with no identity-minting of its own.
//
// NOT a fact.Key observation like everything else on the span: a Record is per
// request and this is per span, so recording it as a fact would put one value on
// a request that may hold several. The key spelling still comes from the
// registry.
type spanUUID struct{}

// OnStart is where the attribute has to be set: OnEnd receives a ReadOnlySpan,
// which cannot take one — the same constraint that puts observedTimeUnixNano in
// the middleware just before End.
func (spanUUID) OnStart(_ context.Context, span sdktrace.ReadWriteSpan) {
	span.SetAttributes(attribute.String(keyOf(fact.SpanUUID), uuid.NewString()))
}

// OnEnd does nothing. This processor is not in the export path; the batcher is.
func (spanUUID) OnEnd(sdktrace.ReadOnlySpan) {}

// Shutdown has nothing to release: no buffer, no connection, no goroutine.
func (spanUUID) Shutdown(context.Context) error { return nil }

// ForceFlush has nothing to flush, for the same reason.
func (spanUUID) ForceFlush(context.Context) error { return nil }

// --- Who this process says it is -------------------------------------------
//
// The Resource is read once at boot and rides every span this file projects,
// which is why it sits beside them. It rides the metric streams too — metrics.go
// projects labels, never the Resource, because there is only ever one.

// Identity is who this deployment says it is, read off config once at boot.
//
// A struct rather than three string parameters: projectResource(id, build)
// cannot be called with domain and network the wrong way round, and
// projectResource(a, b, c) can.
type Identity struct {
	// Producer is the registered subscriber id, an FQDN. Never service.name.
	Producer string

	// Domain is the sector, already checked against the registry's declared
	// values by config.validateOTel.
	Domain string

	// NetworkID is the network this deployment serves — our key, not the spec's.
	NetworkID string
}

// NewIdentity reads the three off config. Exported so the composition root can
// build one without this package importing anything of app's.
func NewIdentity(cfg config.Config) Identity {
	return Identity{
		Producer:  cfg.App.Subscriber,
		Domain:    cfg.App.Domain,
		NetworkID: cfg.App.Network,
	}
}

// The two Resource values that are constants rather than configuration.
const (
	// eidAPI and eidMetric are the entity ids for the two signals this service
	// emits. The third, AUDIT, has no constant because nothing here emits the
	// LOG signal — it is optional in the spec (otel-specification.md:27) and
	// discovery transitions no entity, so there is no item.prevstate to report.
	// A constant for a signal nobody sends is a claim the code does not keep.
	//
	// This is the one Resource attribute the projections vary, which is why it
	// is a registry row (fact.ResourceEID, Values API/METRIC/AUDIT) rather than
	// a literal.
	eidAPI    = "API"
	eidMetric = "METRIC"

	// serviceName is what SOFTWARE this is and does not vary by deployment,
	// which is the whole difference from Identity.Producer.
	serviceName = "discovery-service"
)

// The four build attributes moved to src/platform/buildinfo on 2026-09-10, with
// their -ldflags targets. cmd's boot line needs the same values the Resource
// carries — otherwise an operator reading the log and a facilitator reading a
// span are told two different things about which build is running — and cmd may
// not import this package. buildinfo's doc comment carries the rest.

// projectResource is the Resource half of the seam: every attribute name comes
// off the registry and none is a literal here. A Resource is built once at boot,
// so a wrong key is wrong on every signal the process ever emits and no test of
// a single signal catches it.
func projectResource(ctx context.Context, id Identity, build buildinfo.Stamp) (*resource.Resource, error) {
	attributes := []attribute.KeyValue{
		attribute.String(keyOf(fact.ResourceEID), eidAPI),
		attribute.String(keyOf(fact.ResourceProducer), id.Producer),
		attribute.String(keyOf(fact.ResourceDomain), id.Domain),
		attribute.String(keyOf(fact.ResourceServiceName), serviceName),
		attribute.String(keyOf(fact.ResourceNetworkID), id.NetworkID),

		attribute.String(keyOf(fact.ResourceServiceVersion), build.Version),
		attribute.String(keyOf(fact.ResourceBuildCommit), build.Commit),
		attribute.String(keyOf(fact.ResourceBuildTreeState), build.TreeState),
		// zeroTime when the binary carries no VCS stamp, which is the case in
		// the release image.
		attribute.String(keyOf(fact.ResourceBuildDate), build.Date),
	}

	// WithFromEnv FIRST, ours second: resource.New merges in order and the last
	// writer wins. The env detector carries pod identity via
	// OTEL_RESOURCE_ATTRIBUTES (opentelemetry.md §Build identity), and letting it win would
	// let an operator put a `producer` there and quietly undo the boot refusal
	// validateOTel just performed.
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(attributes...),
	)
	if err != nil {
		// resource.New returns a usable Resource alongside a partial-detection
		// error, and it is refused anyway: the env detector is the only one
		// running, so its error means OTEL_RESOURCE_ATTRIBUTES is malformed, and
		// booting on half-parsed operator intent is how a pod ends up
		// unattributed.
		return nil, fmt.Errorf("assemble the resource: %w", err)
	}
	return res, nil
}

// withEID returns res with its `eid` replaced, and is how the metrics half gets
// a Resource that says METRIC where the trace half says API.
//
// A DERIVATION rather than a second projectResource call, deliberately. Every
// other Resource attribute — producer, domain, service.name, network.id and the
// four build values — is Required on both signals and must be identical, or a
// facilitator correlating a metric with the trace it came from cannot tell they
// describe the same participant and the same build. Deriving makes that true for
// attributes added later without anyone remembering to add them twice; two
// independent calls would make it true only for as long as the two argument
// lists happened to match.
//
// resource.Merge's SECOND argument wins on a key collision, which is the whole
// mechanism. Schemaless because res is schemaless: Merge treats two differing
// non-empty schema URLs as a conflict, and an empty one merges with anything.
func withEID(res *resource.Resource, eid string) (*resource.Resource, error) {
	merged, err := resource.Merge(res, resource.NewSchemaless(
		attribute.String(keyOf(fact.ResourceEID), eid),
	))
	if err != nil {
		return nil, fmt.Errorf("set eid=%s on the resource: %w", eid, err)
	}
	return merged, nil
}

// keyOf is the registry lookup for an attribute name. fact.Of panics on a key
// with no row, which is the right failure: it happens at boot.
func keyOf(key fact.Key) string {
	return fact.Of(key).SpanKey
}
