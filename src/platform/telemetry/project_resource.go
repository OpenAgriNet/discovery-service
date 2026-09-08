package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// The two Resource values that are constants rather than configuration.
const (
	// eidAPI is the entity id for the TRACE signal. The LOG/AUDIT signal's is
	// AUDIT and the metric stream's is METRIC — the eid is the one Resource
	// attribute the three projections vary, which is why it is a registry row
	// with three declared values and not a literal in three places.
	eidAPI = "API"

	// serviceName is what software this is, and it does not vary by
	// deployment. That is the whole difference from Identity.Producer, which
	// varies by deployment and says which participant this is.
	serviceName = "discovery-service"
)

// projectResource is the Resource half of the seam: the one place a Resource
// attribute is spelled, reading every key off the registry.
//
// Nothing here writes an attribute name as a literal. That is the property
// telemetry-seam.md exists for — renaming `producer` is an edit in registry.go
// and the Resource follows it — and it is worth the indirection precisely
// because a Resource is built once at boot, so a wrong key here is a wrong key
// on every signal the process ever emits and no test of any single signal
// catches it.
func projectResource(ctx context.Context, id Identity, build Build) (*resource.Resource, error) {
	attributes := []attribute.KeyValue{
		attribute.String(keyOf(fact.ResourceEID), eidAPI),
		attribute.String(keyOf(fact.ResourceProducer), id.Producer),
		attribute.String(keyOf(fact.ResourceDomain), id.Domain),
		attribute.String(keyOf(fact.ResourceServiceName), serviceName),
		attribute.String(keyOf(fact.ResourceNetworkID), id.NetworkID),

		attribute.String(keyOf(fact.ResourceServiceVersion), build.Version),
		attribute.String(keyOf(fact.ResourceBuildCommit), build.Commit),
		attribute.String(keyOf(fact.ResourceBuildTreeState), build.TreeState),
		attribute.String(keyOf(fact.ResourceBuildDate), build.Date),
	}

	// WithFromEnv first, ours second, because resource.New merges in order and
	// the last writer wins.
	//
	// The env detector is not decoration: opentelemetry.md:479 drops onix's
	// parent_id and puts pod identity here instead, through
	// OTEL_RESOURCE_ATTRIBUTES=k8s.pod.name=$(POD_NAME) from the chart's
	// downward API — standard semconv, no code, and it lands on spans, logs and
	// metrics at once rather than being copied onto every span.
	//
	// The order matters the other way too. An operator can put anything in that
	// variable, including a `producer` — and letting it win would undo the boot
	// refusal validateOTel just performed, quietly, in an environment variable
	// nobody diffs.
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(attributes...),
	)
	if err != nil {
		// resource.New returns a usable Resource alongside a partial-detection
		// error, but not here: the env detector is the only one running and its
		// error means OTEL_RESOURCE_ATTRIBUTES is malformed. Booting on a
		// half-parsed operator intent is how a pod ends up unattributed.
		return nil, fmt.Errorf("assemble the resource: %w", err)
	}
	return res, nil
}

// keyOf is the registry lookup, named short because it appears nine times
// above and the alternative is nine lines that each read as machinery rather
// than as the attribute they set.
//
// fact.Of panics on a key with no row, which is the right failure: it happens
// at boot, on every deployment, on the first test that builds a Resource.
func keyOf(key fact.Key) string {
	return fact.Of(key).SpanKey
}
