// Package telemetry owns the OpenTelemetry SDK, and it is the only package in
// the service that links it.
//
// It still starts no span of its own. Provider hands out a tracer, span.go hands
// back projected attributes, and the Trace middleware is the one caller that puts
// the two together — which is what keeps the SDK behind a seam rather than merely
// behind an import.
//
// Start at provider.go. The files:
//
//	provider.go   boots it — Init, Provider, Tracer, MeterProvider, Shutdown,
//	              the two OTLP exporters, and the W3C trace context propagator
//	identity.go   who this process says it is — the producer/domain/network
//	              triple, the Resource they project onto, and the build stamp
//	span.go       one request's facts → span attributes and span events
//	metrics.go    the pool's counters → metric streams and their labels
//	fact/         the attribute table (see below)
//
// One test file per source file, with one forced exception:
// provider_internal_test.go is `package telemetry` and provider_test.go is
// `package telemetry_test`, so they cannot merge.
//
// testsupport.go is the one file the map above leaves out, because it is not a
// projection: it is test scaffolding deliberately in a non-test file, and it
// says at its top why the import boundary requires that.
//
// # Why fact/ is a separate package
//
// Not by taste. `fact` links no SDK — its imports are context, fmt, iter, slices,
// strings, sync and time — so a controller can name an attribute key without
// pulling an exporter into its build graph. A controller that linked one would
// break on an SDK release and would start a batch processor in its own test
// binary (A23).
//
// tests/architecture/boundary_test.go is what enforces it, not convention. It
// bans `go.opentelemetry.io` and this import path repo-wide, exempts the fact
// subpath from the ban, and allows the import back only in this package, in
// src/app/container.go, and in a short list of named FILES — files rather than
// packages, so that a seventh entry is as awkward to add as the existing ones
// were.
//
// That guard is also why the log projection is src/platform/logger/fields.go and
// cannot move here. Moving it would need an exemption for package logger, and the
// exemption list's own comment says that would permit exactly the SDK import
// which keeps request_logger.go free of a telemetry dependency.
//
// The seam this buys is documented in docs/design/telemetry-seam.md: renaming an
// attribute is one edit in fact/registry.go, and the span, log and metric
// projections follow it.
package telemetry
