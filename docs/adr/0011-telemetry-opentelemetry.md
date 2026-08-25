# ADR-0011 — OpenTelemetry for traces and metrics

**Status:** Accepted
**Date:** 2026-08-25

## Context

TRD §6 and §7 want a request followable across a network hop, and RED metrics
per route. This service sits in the middle of a Beckn chain, so the hop is the
normal case rather than the exception.

## Decision

OpenTelemetry traces and metrics, `otelhttp` instrumentation, W3C Trace Context
propagated in and out, OTLP exporter defaulting to `none`. zap carries
structured logs alongside, correlated by trace id.

## Alternatives considered

- **zap alone** — structured logs correlate within one process. Only a
  propagation standard makes one request followable across the chain, and the
  chain is the point.

## Consequences

The exporter defaults to `none` so a collector-less deploy still boots — a
telemetry dependency that prevents startup is a telemetry dependency that gets
removed. Dashboards and analytics over the exported data are out of scope for
this service (an add-on, e.g. Obsrv, owns them).
