# ADR-0016 — `net/http.ServeMux` for HTTP routing, superseding chi v5

**Status:** Accepted
**Date:** 2026-08-27

Supersedes [ADR-0001](0001-router-chi-v5.md).

## Context

ADR-0001 chose chi v5. The service shipped on `net/http.ServeMux` and chi was
never added to `go.mod` — so for the whole of Phase 1 the accepted record named
a dependency the binary does not have, and `docs/design/discover-and-publish.md`
repeated it in its Tech Stack line and in D1.

This record is written after the fact. That is worth saying plainly rather than
dressing the sequence up as a decision that preceded the code: what follows is
the justification for keeping what shipped, checked against ADR-0001's own
requirements, not a reconstruction of a deliberation that happened.

ADR-0001 asked for four things: `net/http` types throughout, native
`context.Context` propagation, `httptest` in the acceptance suite, and
`otelhttp` without an adapter. It disqualified Fiber for failing the first two.
Every one of the four is a property of `net/http` itself, so chi could only ever
have satisfied them by being a thin layer over the thing that already did.

What chi added on top was routing: method matching and path parameters, which
`ServeMux` could not do before Go 1.22. This module is on **Go 1.25**, where
`mux.Handle("POST /publish", …)` is stdlib.

## Decision

`net/http.ServeMux`, two levels of it (`src/app/router.go`): an outer table that
decides what is a route at all, and an inner one each controller registers into
through the same `Register` call its own tests drive.

No third-party router. A dependency is added when it does something the standard
library does not.

## Alternatives considered

- **chi v5, as ADR-0001 accepted it.** Its routing advantage over `ServeMux`
  closed in Go 1.22, and the features that remain distinctive — nested routers,
  URL parameters, middleware groups — are unused here: there are four routes,
  all fixed paths, and the middleware chain is composed by hand in `chain(a)`
  precisely so its order is legible as a list. Adopting it now would mean adding
  a dependency to match a document rather than to solve a problem.
- **Fiber, Gin, Gorilla.** Unchanged from ADR-0001, and Fiber is still
  disqualified by the same property: fasthttp is not `net/http`, so
  `context.Context` propagation goes through `UserContext()` and every
  stdlib-shaped dependency needs an adapter.

## Consequences

`ServeMux` will not grow features on anyone's schedule but the Go team's. If
this service ever needs path parameters with typed extraction, per-subtree
middleware, or a route table large enough that ordering conflicts become hard to
see by eye, that is the point to revisit — and revisiting means a new record,
not an edit to this one.

The two-level mux is load-bearing and easy to mistake for redundancy. Collapsing
it into one table would put route membership and handler selection in the same
place, and the reason they are apart is that neither `router.go` nor a
controller should be able to add a route by itself.

`ServeMux` returns **405** with an `Allow` header where a wildcard mount would
have produced a 404, because a method pattern that matches the path but not the
method is a different answer from no pattern at all. That is the correct
distinction and it is not the one C2 is about: C2 concerns `/catalog/publish`,
a PATH that must read as absent, and it still 404s.
