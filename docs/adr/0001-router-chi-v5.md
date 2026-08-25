# ADR-0001 — chi v5 for HTTP routing

**Status:** Accepted
**Date:** 2026-08-25

## Context

The service exposes two synchronous protocol routes behind a fixed middleware
chain, and it must carry a W3C trace context through every layer (ADR-0011),
run under `httptest` in the acceptance suite, and accept `otelhttp` without an
adapter.

## Decision

chi v5 (MIT). Routing, middleware and handlers are `net/http` types throughout.

## Alternatives considered

- **Fiber** — fasthttp does not implement `net/http`, so `context.Context`
  propagation goes through a `UserContext()` dance and every stdlib-shaped
  dependency needs an adapter. The propagation standard in ADR-0011 is only
  worth adopting if it survives the router.
- **Gin, Gorilla** — no disqualifying property; chi was preferred for being the
  thinnest of the three over `http.Handler`.

## Consequences

`net/http` performance rather than fasthttp's. The 20 ms budget is spent in
PostgreSQL, not in the router, so this is not the constraint that binds.
Middleware is ordinary `func(http.Handler) http.Handler`, which is what lets
the fixed chain in the design document be read as a list of standard
decorators.
