package errors

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/OpenAgriNet/discovery-service/src/beckn"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

// The same pinned fixture src/beckn walks. Read here too rather than exported
// from there: a test helper that crosses a package boundary is production API,
// and this one exists only so two tests can disagree with the same document.
const specFixture = "../../../tests/testdata/beckn-v2.0.0.yaml"

// C1's mapping, asserted against every member of the enum rather than against
// one example per prefix.
//
// The header is the only place the PRD's five categories survive — the spec
// closed Error with additionalProperties:false — so a code whose prefix this
// table does not know ships a blank X-Beckn-Error-Type, and a blank header on
// an error is worse than a wrong one: it is unattributable and looks like a
// bug in the sender.
func TestEveryEnumMemberMapsToAnErrorType(t *testing.T) {
	members := enumMembers(t)
	if len(members) != 76 {
		t.Fatalf("ErrorCode has %d members, want 76; the fixture is not the document C1 was written against", len(members))
	}

	want := map[string]string{
		"CTX": TypeContext,
		"AUT": TypeCore,
		"SCH": TypeDomain,
		"BIZ": TypeDomain,
		"POL": TypePolicy,
		"NET": TypeSystem,
	}
	for _, member := range members {
		prefix, _, _ := strings.Cut(member, "_")
		expected, known := want[prefix]
		if !known {
			t.Fatalf("%s carries prefix %q, which C1 does not map; the enum has grown a family", member, prefix)
		}
		if got := TypeOf(beckn.ErrorCode(member)); got != expected {
			t.Errorf("TypeOf(%s) = %q, want %q", member, got, expected)
		}
	}
}

// DOM_ has zero members in the enum and is therefore invisible to the walk
// above, but it is the prefix a code relayed from a downstream system arrives
// with — the one case Error's own description names as legitimately
// non-canonical. C1 maps it for exactly that reason.
func TestRelayedDomainCodeMapsToDomain(t *testing.T) {
	if got := TypeOf(beckn.ErrorCode("DOM_OCPI_SESSION_REJECTED")); got != TypeDomain {
		t.Errorf("TypeOf(DOM_...) = %q, want %q", got, TypeDomain)
	}
}

// A prefix from a spec version this build has never seen still has to answer
// something, and SYSTEM is the one category that is true of it: this receiver
// could not attribute the fault. Guessing DOMAIN would be a claim about the
// error; a blank header would be C1's own failure mode.
func TestAnUnknownPrefixIsAttributedToTheSystem(t *testing.T) {
	if got := TypeOf(beckn.ErrorCode("XYZ_FROM_THE_FUTURE")); got != TypeSystem {
		t.Errorf("TypeOf(XYZ_...) = %q, want %q", got, TypeSystem)
	}
}

// C7. `details` is closed to {path, cause}, so several faults cannot become a
// list — they become a chain, and the test that matters is that none is
// dropped and none loses its path on the way in.
func TestThreeFaultsBecomeAChainThreeDeep(t *testing.T) {
	chain := Chain(
		Schema(beckn.CodeSchemaValidationFailed, "resource id is required").At("$.message.catalogs[0].resources[0].id"),
		Schema(beckn.CodeSchemaInvalidFormat, "not a GeoJSON geometry").At("$.message.catalogs[0].resources[1].geometry"),
		Context(beckn.CodeContextActionMismatch, "unknown action").At("$.context.action"),
	)

	rendered := chain.Beckn(config.Errors{})

	wantPaths := []string{
		"$.message.catalogs[0].resources[0].id",
		"$.message.catalogs[0].resources[1].geometry",
		"$.context.action",
	}
	wantCodes := []beckn.ErrorCode{
		beckn.CodeSchemaValidationFailed,
		beckn.CodeSchemaInvalidFormat,
		beckn.CodeContextActionMismatch,
	}

	level := &rendered
	for i := range wantPaths {
		if level == nil {
			t.Fatalf("chain ended at level %d; want %d levels", i, len(wantPaths))
		}
		if level.Code != wantCodes[i] {
			t.Errorf("level %d code = %q, want %q", i, level.Code, wantCodes[i])
		}
		if level.Details == nil || level.Details.Path != wantPaths[i] {
			t.Errorf("level %d details = %+v, want path %q", i, level.Details, wantPaths[i])
		}
		if level.Details == nil {
			break
		}
		level = level.Details.Cause
	}
	if level != nil {
		t.Errorf("chain runs past level %d: %+v", len(wantPaths), level)
	}
}

// Chain copies rather than links, so a fault handed to it twice does not end up
// pointing at its own second chain — and a caller still holding the fault does
// not find a cause bolted onto it.
func TestChainDoesNotMutateItsFaults(t *testing.T) {
	first := Schema(beckn.CodeSchemaValidationFailed, "first")
	second := Schema(beckn.CodeSchemaInvalidFormat, "second")

	if chain := Chain(first, second); chain == nil {
		t.Fatal("Chain returned nil for two faults")
	}
	if first.Cause != nil {
		t.Errorf("Chain wrote a cause onto its argument: %+v", first.Cause)
	}

	again := Chain(first, second)
	if again.Cause == nil || again.Cause.Message != "second" {
		t.Errorf("second Chain over the same faults = %+v, want the same chain", again)
	}
}

// A chain of one is one Error with no details.cause, not an Error wrapping an
// empty one — and no faults at all is no error, which is what lets a caller
// hand Chain the result of a validation pass without checking it first.
func TestChainOfOneAndChainOfNone(t *testing.T) {
	if got := Chain(); got != nil {
		t.Errorf("Chain() = %+v, want nil", got)
	}
	one := Chain(Schema(beckn.CodeSchemaInvalidJSON, "unreadable")).Beckn(config.Errors{})
	if one.Details != nil {
		t.Errorf("Chain of one carries details %+v, want none", one.Details)
	}
}

// C1. The body is spec-conformant by default; the legacy key is a deliberate
// violation for v1-era clients and is therefore opt-in.
func TestLegacyTypeIsInjectedOnlyWhenConfigured(t *testing.T) {
	fault := Auth(beckn.CodeAuthSignatureMissing, "no signature header")

	if got := fault.Beckn(config.Errors{IncludeLegacyType: false}); got.Type != "" {
		t.Errorf("type = %q with the flag off, want it absent", got.Type)
	}
	if got := fault.Beckn(config.Errors{IncludeLegacyType: true}); got.Type != TypeCore {
		t.Errorf("type = %q with the flag on, want %q", got.Type, TypeCore)
	}
}

// The legacy key follows the chain or it is a header on the first fault only,
// and a v1 client reading the second would see a body it cannot categorise.
func TestLegacyTypeReachesEveryLevelOfTheChain(t *testing.T) {
	chain := Chain(
		Schema(beckn.CodeSchemaValidationFailed, "first"),
		Policy(beckn.ErrorCode("POL_CONSENT_REQUIRED"), "second"),
	).Beckn(config.Errors{IncludeLegacyType: true})

	if chain.Type != TypeDomain {
		t.Errorf("level 0 type = %q, want %q", chain.Type, TypeDomain)
	}
	if chain.Details == nil || chain.Details.Cause == nil {
		t.Fatalf("chain lost its cause: %+v", chain)
	}
	if got := chain.Details.Cause.Type; got != TypePolicy {
		t.Errorf("level 1 type = %q, want %q", got, TypePolicy)
	}
}

func enumMembers(t *testing.T) []string {
	t.Helper()

	body, err := os.ReadFile(specFixture)
	if err != nil {
		t.Fatalf("read %s: %v", specFixture, err)
	}
	var spec struct {
		Components struct {
			Schemas struct {
				ErrorCode struct {
					Enum []string `yaml:"enum"`
				} `yaml:"ErrorCode"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(body, &spec); err != nil {
		t.Fatalf("parse %s: %v", specFixture, err)
	}
	return spec.Components.Schemas.ErrorCode.Enum
}
