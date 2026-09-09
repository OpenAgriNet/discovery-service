package telemetry

import (
	"context"
	"fmt"
	"runtime/debug"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/OpenAgriNet/discovery-service/src/platform/config"
	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// Identity is who this deployment says it is, read off config once at boot.
//
// A struct rather than three parameters because the Resource is the one place
// all three appear and they are all strings: projectResource(id, build) cannot
// be called with domain and network the wrong way round, and
// projectResource(a, b, c) can.
type Identity struct {
	// Producer is the registered subscriber id, an FQDN. Never service.name.
	Producer string

	// Domain is the sector. Already checked against the registry's declared
	// values by validateOTel, which is why nothing re-checks it here.
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
	// eidAPI is the entity id for the TRACE signal. The LOG/AUDIT signal's is
	// AUDIT and the metric stream's is METRIC — the eid is the one Resource
	// attribute the three projections vary, which is why it is a registry row
	// with three declared values and not a literal in three places.
	eidAPI = "API"

	// serviceName is what software this is, and it does not vary by deployment.
	// That is the whole difference from Identity.Producer, which does and says
	// which participant this is.
	serviceName = "discovery-service"
)

// version is injected at link time; docs/design/opentelemetry.md, "Build
// identity", says why it is the one attribute that cannot read the toolchain's
// build stamp. Do not restate that reasoning here — it was wrong at four sites
// until it was measured.
//
// `dev` rather than "" so an unset value differs from a dropped one. Pinned by
// tests/architecture/ldflags_test.go, because `go build -X` on a symbol that
// does not exist succeeds silently.
var version = "dev"

// Build is what -ldflags and the toolchain's VCS stamp know between them about
// the binary that is running.
type Build struct {
	Version   string
	Commit    string
	TreeState string
	Date      string
}

// The values the three VCS-derived attributes take when the binary carries no
// stamp — every `go test` binary, and every build from an exported tree
// including the release image. Named because `unknown` appearing as a bare
// literal twice invites someone to make one of them "".
const (
	unknownRevision  = "unknown"
	unknownTreeState = "unknown"

	// zeroTime is what build.date says with no stamp. Not `unknown`: this one is
	// a timestamp everywhere else, and a consumer parsing it would have to
	// special-case a word. The zero instant is unambiguous and parses.
	zeroTime = "1970-01-01T00:00:00Z"
)

// readBuild assembles the four build attributes, reporting an absence as a value
// rather than as an error — the same choice main.go's vcsRevision made. A binary
// with no VCS stamp is a normal thing to be, and a Resource that refused to
// build over it is a service that cannot boot in a test.
func readBuild() Build {
	build := Build{
		Version:   version,
		Commit:    unknownRevision,
		TreeState: unknownTreeState,
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return build
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			build.Commit = setting.Value
		case "vcs.time":
			// The COMMIT's timestamp, not the moment the compiler ran. onix's
			// onix.build.date is the latter; this is the reproducible half and
			// the one that answers which change is deployed.
			build.Date = setting.Value
		case "vcs.modified":
			build.TreeState = treeState(setting.Value)
		}
	}

	if build.Date == "" {
		build.Date = zeroTime
	}
	return build
}

// treeState maps debug.BuildSetting's "true"/"false" onto the registry's
// clean/dirty/unknown. Translated here rather than at the call site because
// `dirty` on a production Resource is a finding, and it must not be confusable
// with a missing stamp.
func treeState(modified string) string {
	switch modified {
	case "true":
		return "dirty"
	case "false":
		return "clean"
	default:
		return unknownTreeState
	}
}

// projectResource is the Resource half of the seam: every attribute name comes
// off the registry and none is a literal here. Worth the indirection because a
// Resource is built once at boot, so a wrong key is wrong on every signal the
// process ever emits and no test of any single signal catches it.
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
		// zeroTime when the binary carries no VCS stamp, which is the case in the
		// release image. See "Build identity" in opentelemetry.md.
		attribute.String(keyOf(fact.ResourceBuildDate), build.Date),
	}

	// WithFromEnv first, ours second: resource.New merges in order and the last
	// writer wins. The env detector carries pod identity via
	// OTEL_RESOURCE_ATTRIBUTES (opentelemetry.md:479), and the order matters the
	// other way too — an operator can put a `producer` in that variable, and
	// letting it win would quietly undo the boot refusal validateOTel just
	// performed.
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

// keyOf is the registry lookup for an attribute name, named short because it
// appears nine times above. fact.Of panics on a key with no row, which is the
// right failure: it happens at boot.
func keyOf(key fact.Key) string {
	return fact.Of(key).SpanKey
}
