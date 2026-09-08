package config

import (
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/telemetry/fact"
)

// The exporter is the only setting in this file that a Phase 1 deployment
// actually changes, and the three tests below are the three things that go wrong
// when it is changed carelessly: a collector-less deployment that will not boot,
// an exporting deployment with no endpoint to export to, and an exporting
// deployment whose Resource cannot name the participant it belongs to.

// TestNoneBootsWithNothingElseSet is the acceptance criterion, and it is first
// because it is the case every existing deployment is in. OTEL_EXPORTER defaults
// to none (A5's reasoning applied to telemetry: the capability ships before the
// collector does), so a deployment that has never heard of this task must load
// with no telemetry variable set at all.
func TestNoneBootsWithNothingElseSet(t *testing.T) {
	cfg, err := load(repoCommonYAML, noInstance, baseEnv())
	if err != nil {
		t.Fatalf("load with no telemetry variables set: %v", err)
	}
	if cfg.OTel.Exporter != "none" {
		t.Errorf("otel.exporter = %q, want none: the default is what makes a collector-less deployment boot", cfg.OTel.Exporter)
	}
	if cfg.App.Subscriber != "" {
		t.Errorf("app.subscriber = %q, want empty: APP_SUBSCRIBER_ID has no default because nothing issues it yet", cfg.App.Subscriber)
	}
}

// TestOtlpRefusesTheBootWithoutAnIdentity is the other half of the same
// criterion. An exporter with no producer and no domain emits a stream the
// facilitator cannot attribute — every span lands under an empty participant
// id, which is worse than no stream because it looks like data.
func TestOtlpRefusesTheBootWithoutAnIdentity(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "no producer",
			env: map[string]string{
				"OTEL_EXPORTER":               "otlp",
				"OTEL_EXPORTER_OTLP_ENDPOINT": "localhost:4317",
				"APP_DOMAIN":                  "Agriculture",
			},
			want: "APP_SUBSCRIBER_ID",
		},
		{
			name: "no domain",
			env: map[string]string{
				"OTEL_EXPORTER":               "otlp",
				"OTEL_EXPORTER_OTLP_ENDPOINT": "localhost:4317",
				"APP_SUBSCRIBER_ID":           "discovery.oan.example.org",
				"APP_DOMAIN":                  "",
			},
			want: "APP_DOMAIN",
		},
		{
			name: "no endpoint",
			env: map[string]string{
				"OTEL_EXPORTER":     "otlp",
				"APP_SUBSCRIBER_ID": "discovery.oan.example.org",
				"APP_DOMAIN":        "Agriculture",
			},
			want: "OTEL_EXPORTER_OTLP_ENDPOINT",
		},
		{
			name: "unknown exporter",
			env: map[string]string{
				"OTEL_EXPORTER": "jaeger",
			},
			want: "OTEL_EXPORTER",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			environment := baseEnv()
			for key, value := range testCase.env {
				environment[key] = value
			}

			_, err := load(repoCommonYAML, noInstance, environment)
			if err == nil {
				t.Fatalf("load succeeded; want a refusal naming %s", testCase.want)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("refusal does not name %s, so an operator cannot act on it:\n%v", testCase.want, err)
			}
		})
	}
}

// TestOtlpBootsWithAnIdentity is the positive case, and it exists so the three
// refusals above cannot be satisfied by a validator that refuses everything.
func TestOtlpBootsWithAnIdentity(t *testing.T) {
	environment := baseEnv()
	environment["OTEL_EXPORTER"] = "otlp"
	environment["OTEL_EXPORTER_OTLP_ENDPOINT"] = "localhost:4317"
	environment["APP_SUBSCRIBER_ID"] = "discovery.oan.example.org"
	environment["APP_DOMAIN"] = "Agriculture"

	cfg, err := load(repoCommonYAML, noInstance, environment)
	if err != nil {
		t.Fatalf("load a complete otlp configuration: %v", err)
	}
	if cfg.App.Subscriber != "discovery.oan.example.org" {
		t.Errorf("app.subscriber = %q", cfg.App.Subscriber)
	}
	if cfg.App.Domain != "Agriculture" {
		t.Errorf("app.domain = %q", cfg.App.Domain)
	}
}

// TestTheDomainIsCheckedAgainstTheRegistry is the seam earning its keep on the
// first task that could use it.
//
// opentelemetry.md open question 2: `Agriculture` is a placeholder, and every
// OAN component must emit the identical string or grouping splits across the
// network. A typo therefore has to fail the boot, and the list of acceptable
// spellings has to live in exactly one place — the registry row that also
// spells the attribute key. Hard-coding "Agriculture" in this validator would
// make answering open question 2 a two-file edit with no failure if the second
// is missed, which is the specific thing telemetry-seam.md exists to prevent.
func TestTheDomainIsCheckedAgainstTheRegistry(t *testing.T) {
	environment := baseEnv()
	environment["OTEL_EXPORTER"] = "otlp"
	environment["OTEL_EXPORTER_OTLP_ENDPOINT"] = "localhost:4317"
	environment["APP_SUBSCRIBER_ID"] = "discovery.oan.example.org"
	environment["APP_DOMAIN"] = "Horticulture"

	_, err := load(repoCommonYAML, noInstance, environment)
	if err == nil {
		t.Fatal("a domain outside the registry's declared values loaded; want a refusal")
	}

	// The refusal has to quote the registry's own list, not a copy of it: an
	// operator reading "want one of [Agriculture]" can act, and the day the
	// list grows the message grows with it.
	for _, value := range fact.Of(fact.ResourceDomain).Values {
		if !strings.Contains(err.Error(), value) {
			t.Errorf("refusal does not offer %q, which the registry declares:\n%v", value, err)
		}
	}
}
