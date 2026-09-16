// Package otelpipeline runs the repository's own otel/collector.yaml against a
// real collector and reads back what it exports.
//
// The five `metric.code` streams the network telemetry specification requires
// are produced entirely by that file — no Go, no instrument, no fact.Instrument
// row — so there is nothing in this repository for a unit test to hold. What
// they are derived from is spans, and the derivation is six processors deep
// with an ordering that is load-bearing in two places. A fake would assert only
// that this package's author read the YAML the same way twice.
//
// It caught a real defect on the way in. The config shipped once without
// `groupbyattrs`, and four discovers with one refusal exported
// discover_api_failure_percent TWICE, as 0% and 33.333333%, because the service
// sends spans in several OTLP requests and metricsgeneration divides within one
// ResourceMetrics. Both numbers look like an answer. Only sending spans the way
// the service actually sends them — several requests, not one — produces it,
// which is why SendSpans below uses a syncer rather than a batcher.
package otelpipeline

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// specMetricsDir and specMetricsFile are where the override tells the file
// exporter to write, and the three must agree — testdata/override.yaml spells
// the same path a third time because a collector config cannot reference a Go
// constant.
//
// A bind-mounted directory rather than a path inside the image, and not by
// preference: the collector image is distroless and has no /tmp, so the file
// exporter fails its own startup with "no such file or directory" and takes the
// whole collector down with it. Creating the directory by copying a placeholder
// into it does not help either — the copy lands owned by root and the collector
// runs as uid 10001. A bind mount of a host directory the test owns is the one
// arrangement that is both present and writable.
const (
	specMetricsDir  = "/spec"
	specMetricsFile = "metrics.json"
)

// Collector is a running collector plus the address to send spans to.
type Collector struct {
	container testcontainers.Container
	endpoint  string
	// output is the host side of the bind mount, so Export reads a normal file
	// rather than copying one out of a stopped container.
	output string
}

// Span is one request as the trace pipeline would see it: a route, and the two
// attributes the derivation reads.
//
// ErrorType empty means the request succeeded — the attribute is ABSENT rather
// than "none", because that is what middlewares/trace.go does and the
// connector's condition tests for nil. Writing "none" here would make the test
// pass against a config that counts every success as a failure.
//
// Empty is a *bool for the same reason one level down: result.empty is
// three-state. Absent on a publish and on a discover that errored, which is not
// the same as false.
type Span struct {
	Route     string
	ErrorType string
	Empty     *bool
}

// Start brings up the collector on the repository's own config, merged with
// testdata/override.yaml, and returns it ready to receive OTLP.
func Start(t *testing.T) *Collector {
	t.Helper()
	skipIfShort(t)

	ctx := context.Background()

	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate the repository root: %v", err)
	}

	// t.TempDir removes it on cleanup, so an export does not outlive the test
	// that produced it.
	output := t.TempDir()

	req, err := request(root, output)
	if err != nil {
		t.Fatalf("build the collector's container request: %v", err)
	}

	ctr, err := testcontainers.GenericContainer(ctx, req)
	if err != nil {
		t.Fatalf("start the collector: %v", err)
	}

	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("collector host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "4317/tcp")
	if err != nil {
		t.Fatalf("collector OTLP port: %v", err)
	}

	c := &Collector{container: ctr, endpoint: host + ":" + port.Port(), output: output}

	t.Cleanup(func() {
		// Terminate rather than Stop: Export below has already stopped it
		// gracefully, and terminating a stopped container is how it is removed.
		if err := ctr.Terminate(context.Background()); err != nil {
			t.Logf("terminate the collector: %v", err)
		}
	})

	return c
}

// request is the container the test runs: the repository's own config, the
// override beside it, and a bind mount to read the export back through.
//
// The container runs as the test's own uid rather than the image's 10001. The
// export lands in a directory this process owns, so a collector writing as
// anyone else could not create it — and the alternative, widening the directory
// to 0o777, is a permission this test has no business asking for and gosec is
// right to refuse.
func request(root, output string) (testcontainers.GenericContainerRequest, error) {
	image, err := collectorImage(root)
	if err != nil {
		return testcontainers.GenericContainerRequest{},
			fmt.Errorf("read the collector image from docker-compose.yml: %w", err)
	}

	return testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			Image: image,
			User:  strconv.Itoa(os.Getuid()),
			// Two --config files, deep-merged by the collector. The first is
			// the artefact under test, unmodified.
			Cmd: []string{
				"--config=/etc/otel/collector.yaml",
				"--config=/etc/otel/override.yaml",
			},
			Files: []testcontainers.ContainerFile{
				{
					HostFilePath:      filepath.Join(root, "otel", "collector.yaml"),
					ContainerFilePath: "/etc/otel/collector.yaml",
					FileMode:          0o644,
				},
				{
					HostFilePath:      filepath.Join(root, "tests", "otelpipeline", "testdata", "override.yaml"),
					ContainerFilePath: "/etc/otel/override.yaml",
					FileMode:          0o644,
				},
			},
			ExposedPorts: []string{"4317/tcp"},
			WaitingFor:   wait.ForLog("Everything is ready").WithStartupTimeout(90 * time.Second),
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.Binds = append(hc.Binds, output+":"+specMetricsDir)
			},
		},
	}, nil
}

// SendSpans exports one span per request, which is the point rather than a
// simplification.
//
// A syncer runs the exporter on End, so each span becomes its own OTLP request
// and arrives as its own ResourceSpans — exactly what a service under load does
// and exactly what fragmented the window before `groupbyattrs` was added. A
// batcher here would send one request, produce one ResourceSpans, and pass
// against a config that is broken in production.
func (c *Collector) SendSpans(t *testing.T, spans []Span) {
	t.Helper()
	ctx := context.Background()

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(c.endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		t.Fatalf("build the OTLP exporter: %v", err)
	}

	// The Resource this service would send. It matters that every span carries
	// the SAME one: groupbyattrs compacts on exact equality, so a test whose
	// spans differed would be testing the uncompacted path.
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", "discovery-service"),
			attribute.String("producer", "discovery-service.local-network.oan"),
			attribute.String("domain", "Agriculture"),
			attribute.String("network.id", "local-network"),
			attribute.String("eid", "API"),
		)),
	)

	tracer := provider.Tracer("discovery_service")
	for _, s := range spans {
		attrs := []attribute.KeyValue{attribute.String("http.route", s.Route)}
		if s.ErrorType != "" {
			attrs = append(attrs, attribute.String("error_type", s.ErrorType))
		}
		if s.Empty != nil {
			attrs = append(attrs, attribute.Bool("result.empty", *s.Empty))
		}

		_, span := tracer.Start(ctx, strings.TrimPrefix(s.Route, "/"),
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attrs...))
		span.End()
	}

	if err := provider.Shutdown(ctx); err != nil {
		t.Fatalf("flush the spans: %v", err)
	}
}

// Export stops the collector and returns what metrics/spec wrote.
//
// Stopping is how the window is closed, and it is deliberate. batch/window
// holds spans for 60 seconds in the shipped config; shortening that in the
// override would mean the test exercised a timeout the service never runs.
// A graceful stop flushes the batch instead, so the connector emits once, over
// every span sent — one complete window, on the real setting, in a second.
func (c *Collector) Export(t *testing.T) []Metric {
	t.Helper()
	ctx := context.Background()

	timeout := 30 * time.Second
	if err := c.container.Stop(ctx, &timeout); err != nil {
		t.Fatalf("stop the collector to flush its window: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(c.output, specMetricsFile))
	if err != nil {
		t.Fatalf("read %s — metrics/spec exported nothing at all: %v",
			filepath.Join(specMetricsDir, specMetricsFile), err)
	}

	metrics, err := parse(raw)
	if err != nil {
		t.Fatalf("%v\n\nraw export:\n%s", err, raw)
	}
	return metrics
}

// Metric is one exported stream, flattened to what the specification cares
// about. The nesting OTLP uses is not the thing under test.
type Metric struct {
	Name        string
	Unit        string
	Description string
	Monotonic   bool
	Temporality string
	Value       float64
	// Attributes are the datapoint's, which is where every spec-required
	// metric.* field lives.
	Attributes map[string]string
	// Resource and Scope are carried down per metric so an assertion about
	// eid or scope.name reads as one line rather than as a walk.
	Resource map[string]string
	Scope    string
	// Datapoints is the count before flattening. Anything but 1 means the
	// window fragmented — see the package comment.
	Datapoints int
}

// parse reads the file exporter's output: OTLP JSON, one export per line.
//
// Exactly one line and exactly one resourceMetrics are required rather than
// merely expected. Two of either is the fragmentation defect this package
// exists to catch, and taking the first would hide it behind a passing test.
func parse(raw []byte) ([]Metric, error) {
	line, err := soleLine(raw)
	if err != nil {
		return nil, err
	}

	var export struct {
		ResourceMetrics []resourceMetrics `json:"resourceMetrics"`
	}
	if err := json.Unmarshal(line, &export); err != nil {
		return nil, fmt.Errorf("decode the export: %w", err)
	}

	if len(export.ResourceMetrics) != 1 {
		return nil, fmt.Errorf("the export holds %d resourceMetrics, want exactly 1 — "+
			"identical resources were not compacted, so each fragment got its own "+
			"denominator", len(export.ResourceMetrics))
	}
	return metricsFrom(export.ResourceMetrics[0])
}

// soleLine returns the export, and fails if there is more than one of them.
func soleLine(raw []byte) ([]byte, error) {
	var lines [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		if line := bytes.TrimSpace(scanner.Bytes()); len(line) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan the export: %w", err)
	}
	if len(lines) != 1 {
		return nil, fmt.Errorf("metrics/spec wrote %d exports, want exactly 1 — "+
			"the window fragmented, so every percent was computed over part of it", len(lines))
	}
	return lines[0], nil
}

// metricsFrom flattens one ResourceMetrics, carrying its resource and scope
// down onto every stream.
func metricsFrom(rm resourceMetrics) ([]Metric, error) {
	res := flatten(rm.Resource.Attributes)

	var out []Metric
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Sum == nil {
				return nil, fmt.Errorf("%s is not a Sum (gauge: %s) — the METRIC "+
					"signal permits no other aggregation", m.Name, m.Gauge)
			}
			if len(m.Sum.DataPoints) == 0 {
				return nil, fmt.Errorf("%s has no datapoints", m.Name)
			}
			point := m.Sum.DataPoints[0]
			if point.AsDouble == nil {
				return nil, fmt.Errorf("%s's datapoint is not asDouble", m.Name)
			}

			out = append(out, Metric{
				Name:        m.Name,
				Unit:        m.Unit,
				Description: m.Description,
				Monotonic:   m.Sum.IsMonotonic,
				Temporality: string(m.Sum.AggregationTemporality),
				Value:       *point.AsDouble,
				Attributes:  flatten(point.Attributes),
				Resource:    res,
				Scope:       sm.Scope.Name + " " + sm.Scope.Version,
				Datapoints:  len(m.Sum.DataPoints),
			})
		}
	}
	return out, nil
}

// The OTLP JSON the file exporter writes, declared only as deep as the
// assertions reach. A field absent here is a field this package does not check.
type resourceMetrics struct {
	Resource struct {
		Attributes []keyValue `json:"attributes"`
	} `json:"resource"`
	ScopeMetrics []struct {
		Scope struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"scope"`
		Metrics []struct {
			Name        string          `json:"name"`
			Unit        string          `json:"unit"`
			Description string          `json:"description"`
			Gauge       json.RawMessage `json:"gauge"`
			Sum         *struct {
				// protojson omits both when they hold the proto default, so an
				// absent isMonotonic reads false — which is the value being
				// asserted.
				IsMonotonic            bool        `json:"isMonotonic"`
				AggregationTemporality temporality `json:"aggregationTemporality"`
				DataPoints             []struct {
					AsDouble   *float64   `json:"asDouble"`
					Attributes []keyValue `json:"attributes"`
				} `json:"dataPoints"`
			} `json:"sum"`
		} `json:"metrics"`
	} `json:"scopeMetrics"`
}

// temporality decodes aggregationTemporality in either spelling and reports the
// name.
//
// The file exporter writes enums as their proto NUMBER — a delta sum comes out
// as `"aggregationTemporality":1`, not as the name every OTLP document and the
// network telemetry specification use. That is a rendering choice of this
// exporter and not something the pipeline decides, so it must not become the
// thing a test asserts: pinning the number would make the assertion read
// "temporality = 1" and stop meaning anything to whoever next has to check it
// against the spec. Both spellings are accepted for the same reason — a
// collector release that switched to names would be a formatting change, and a
// formatting change must not fail a test about aggregation.
type temporality string

func (tp *temporality) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '"' {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			return err
		}
		*tp = temporality(name)
		return nil
	}

	var number int
	if err := json.Unmarshal(raw, &number); err != nil {
		return err
	}
	switch number {
	case 1:
		*tp = "AGGREGATION_TEMPORALITY_DELTA"
	case 2:
		*tp = "AGGREGATION_TEMPORALITY_CUMULATIVE"
	default:
		*tp = "AGGREGATION_TEMPORALITY_UNSPECIFIED"
	}
	return nil
}

type keyValue struct {
	Key   string `json:"key"`
	Value struct {
		StringValue *string  `json:"stringValue"`
		DoubleValue *float64 `json:"doubleValue"`
		IntValue    *string  `json:"intValue"`
		BoolValue   *bool    `json:"boolValue"`
	} `json:"value"`
}

// flatten renders every attribute as a string, and keeps the OTLP type visible
// in the rendering. `observedTimeUnixNano` must be a string and not an int, so
// a test that could not tell the two apart would not be testing it.
func flatten(attrs []keyValue) map[string]string {
	out := make(map[string]string, len(attrs))
	for _, a := range attrs {
		switch {
		case a.Value.StringValue != nil:
			out[a.Key] = *a.Value.StringValue
		case a.Value.IntValue != nil:
			out[a.Key] = "int:" + *a.Value.IntValue
		case a.Value.DoubleValue != nil:
			out[a.Key] = fmt.Sprintf("double:%v", *a.Value.DoubleValue)
		case a.Value.BoolValue != nil:
			out[a.Key] = fmt.Sprintf("bool:%v", *a.Value.BoolValue)
		}
	}
	return out
}

// repoRoot walks up from the test's directory to the go.mod, the same way
// dbtest finds the migrations. `go test` runs in the package directory, so a
// relative path would break the moment this package moved.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

var composeImage = regexp.MustCompile(`(?m)^\s*image:\s*(otel/opentelemetry-collector-contrib:\S+)`)

// collectorImage reads the tag out of docker-compose.yml rather than repeating
// it, on the same reasoning as tests/architecture/ldflags_test.go reading the
// Makefile as text: a pin duplicated in two files is a pin that holds until
// someone bumps one of them. The config under test is only meaningful against
// the collector version that will run it — `metricsgeneration` was
// `experimental_metricsgeneration` two releases ago.
func collectorImage(root string) (string, error) {
	// The path is built from the go.mod repoRoot found above, never from input.
	raw, err := os.ReadFile(filepath.Join(root, "docker-compose.yml")) //nolint:gosec // G304: the path is this repository's own, not a caller's
	if err != nil {
		return "", err
	}
	match := composeImage.FindSubmatch(raw)
	if match == nil {
		return "", fmt.Errorf("no otel/opentelemetry-collector-contrib image in docker-compose.yml")
	}
	return string(match[1]), nil
}

// skipIfShort mirrors dbtest: no Docker and no -short is a failure rather than
// a silent skip, because a suite that reported green having verified none of
// this is worse than one that did not run.
func skipIfShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker; skipped under -short")
	}
}
