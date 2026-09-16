package otelpipeline_test

import (
	"math"
	"sort"
	"testing"

	"github.com/OpenAgriNet/discovery-service/tests/otelpipeline"
)

// registered is the allow-list from otel/collector.yaml and the registry table
// in docs/design/opentelemetry.md, restated as data.
//
// Restated deliberately, and this is the only duplication in this package that
// earns its keep: a test that read the list out of the YAML would pass whatever
// the YAML said. Adding a code means editing three places, and that is the
// intended cost — a code that exists only in the collector is unregistered on
// the network however correct it looks on a scrape.
var registered = map[string]struct {
	unit  string
	value float64
}{
	"discover_api_total_count":          {unit: "1", value: 4},
	"publish_api_total_count":           {unit: "1", value: 1},
	"discover_api_failure_percent":      {unit: "%", value: 25},
	"publish_api_failure_percent":       {unit: "%", value: 0},
	"discover_api_empty_result_percent": {unit: "%", value: 25},
}

// traffic is one window: one publish that succeeded, and four discovers of
// which one was refused and one matched nothing.
//
// The numbers are chosen so that no two expected values collide. 4 discovers
// with 1 failure is 25%, and 1 publish with 0 failures is 0% — swap the
// numerator and denominator anywhere in metricsgeneration and at least one
// assertion moves. An earlier draft used 2 and 1, where several wrong wirings
// all produce 50%.
func traffic() []otelpipeline.Span {
	empty, notEmpty := true, false
	return []otelpipeline.Span{
		{Route: "/publish"},
		{Route: "/discover", Empty: &notEmpty},
		{Route: "/discover", Empty: &notEmpty},
		{Route: "/discover", Empty: &empty},
		// Refused. No result.empty at all, because a discover that errored
		// produced no result to be empty — the three-state case.
		{Route: "/discover", ErrorType: "DOMAIN"},
	}
}

// TestTheFiveRegisteredCodesAreExportedAndCorrect is the whole pin on a
// derivation that lives in YAML.
func TestTheFiveRegisteredCodesAreExportedAndCorrect(t *testing.T) {
	collector := otelpipeline.Start(t)
	collector.SendSpans(t, traffic())
	metrics := collector.Export(t)

	byName := make(map[string]otelpipeline.Metric, len(metrics))
	for _, m := range metrics {
		if _, seen := byName[m.Name]; seen {
			t.Fatalf("%s was exported twice in one window", m.Name)
		}
		byName[m.Name] = m
	}

	t.Run("exactly the registered codes, and nothing else", func(t *testing.T) {
		var got []string
		for name := range byName {
			got = append(got, name)
		}
		sort.Strings(got)

		for _, name := range got {
			if _, ok := registered[name]; !ok {
				t.Errorf("%s reached the export and is not a registered code — "+
					"filter/registered is an allow-list and this got past it; a "+
					"facilitator cannot interpret an unregistered stream", name)
			}
		}
		for name := range registered {
			if _, ok := byName[name]; !ok {
				t.Errorf("%s is registered and was not exported (got %v)", name, got)
			}
		}
	})

	for name, want := range registered {
		t.Run(name, func(t *testing.T) {
			m, ok := byName[name]
			if !ok {
				t.Skip("absent; reported by the allow-list subtest")
			}

			if m.Datapoints != 1 {
				t.Errorf("%d datapoints, want 1 — the window fragmented and this "+
					"value covers only part of it", m.Datapoints)
			}
			if math.Abs(m.Value-want.value) > 0.0001 {
				t.Errorf("value = %v, want %v", m.Value, want.value)
			}
			if m.Unit != want.unit {
				t.Errorf("unit = %q, want %q — the spec marks it Required", m.Unit, want.unit)
			}
			if m.Description == "" {
				t.Error("description is empty; the spec marks it Required and " +
					"metricsgeneration copies none")
			}

			// The only aggregation the METRIC signal permits.
			if m.Monotonic {
				t.Error("isMonotonic = true, want false")
			}
			if m.Temporality != "AGGREGATION_TEMPORALITY_DELTA" {
				t.Errorf("temporality = %q, want AGGREGATION_TEMPORALITY_DELTA", m.Temporality)
			}

			if got := m.Resource["eid"]; got != "METRIC" {
				t.Errorf("resource eid = %q, want METRIC — these are the METRIC "+
					"signal and the connector inherits the trace Resource, which "+
					"says API", got)
			}
			if got := m.Scope; got != "discovery_service 1.0" {
				t.Errorf("scope = %q, want %q — the connector supplies its own "+
					"module path and no version", got, "discovery_service 1.0")
			}

			if got := m.Attributes["metric.code"]; got != name {
				t.Errorf("metric.code = %q, want %q", got, name)
			}
			for _, required := range []struct{ key, want string }{
				{"metric.category", "Discovery"},
				{"metric.granularity", "minute"},
				{"metric.frequency", "minute"},
			} {
				if got := m.Attributes[required.key]; got != required.want {
					t.Errorf("%s = %q, want %q", required.key, got, required.want)
				}
			}

			// A string of unix nanos, not an int. flatten prefixes anything
			// else with its OTLP type, so an int shows up as "int:...".
			observed := m.Attributes["observedTimeUnixNano"]
			if observed == "" {
				t.Error("observedTimeUnixNano is absent")
			} else if len(observed) != 19 {
				t.Errorf("observedTimeUnixNano = %q, want 19 digits as a STRING — "+
					"onix and our own spans both carry unix nanos as a string, and "+
					"an int here is a divergence a consumer has to special-case",
					observed)
			}

			// A UUID per datapoint, which onix declares and never sets.
			if got := m.Attributes["metric_uuid"]; len(got) != 36 {
				t.Errorf("metric_uuid = %q, want a UUID", got)
			}
		})
	}
}

// TestEveryPercentIsPresentWhenNothingFailed is gap 5, and the reason the
// numerators are a `sum` over a 0/1 flag rather than a `count`.
//
// A `count` connector emits a stream only when something matched it, so a
// window of entirely successful traffic produced NO failure numerator, no
// ratio, and no code at all. A consumer cannot tell that apart from the
// exporter being down — the metric goes quiet exactly when the service is
// healthiest, which is when someone is most likely to be looking at a dashboard
// to confirm it.
//
// Separate from the test above because it is the one case that test cannot
// cover: it needs a window with no failures in it at all.
func TestEveryPercentIsPresentWhenNothingFailed(t *testing.T) {
	collector := otelpipeline.Start(t)

	notEmpty := false
	collector.SendSpans(t, []otelpipeline.Span{
		{Route: "/publish"},
		{Route: "/discover", Empty: &notEmpty},
		{Route: "/discover", Empty: &notEmpty},
	})

	byName := make(map[string]float64)
	for _, m := range collector.Export(t) {
		byName[m.Name] = m.Value
	}

	for _, name := range []string{
		"discover_api_failure_percent",
		"publish_api_failure_percent",
		"discover_api_empty_result_percent",
	} {
		value, present := byName[name]
		if !present {
			t.Errorf("%s is ABSENT from a window in which nothing failed, and it "+
				"must read 0 — an absent health metric is indistinguishable from a "+
				"dead exporter. This is what a `count` connector does and why the "+
				"numerators are a `sum` over a 0/1 flag", name)
			continue
		}
		if value != 0 {
			t.Errorf("%s = %v, want 0", name, value)
		}
	}
}
