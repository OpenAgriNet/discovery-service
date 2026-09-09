# Stripped rationale

Staging, not a design document. The godoc sweep (#10) cut long explanatory
comments out of the source in favour of one-line godoc. Most of what it cut was
already written down — the A19 measurement, A14, A18's row-level disjunction, the
refused spatial operators, the daily-window wrap — and was simply a second copy.

What is below is the residue: reasoning that existed **only** in a comment and is
recorded here so a later design-doc pass can place it properly. When a fact from
here lands in `implementation-plan.md` or an ADR, delete its section. When the
file is empty, delete the file.

Each entry names the symbol it came from, so `git log -S` finds the original
wording if the summary here loses something.

---

## 1. `ResourceKey` uses NUL as its separator, and nothing else is safe

`src/domain/key.go`

Catalog and resource ids are publisher-supplied text. Every printable separator —
`:`, `/`, `|` — appears in real ids, and an id containing the separator splits
into the wrong pair and hydrates a **different** resource, silently. PostgreSQL
rejects NUL inside a `TEXT` value outright, so no stored id can contain one; the
same property makes it unusable in JSON, which is where these ids arrive from.

The retrieval ports carry flat `[]string` rather than the pair because a
`Retriever` ranks and RRF fuses — both set operations over an opaque identity.
The pair is only needed again at hydration.

## 2. `TouchedSet` is a set beside a slice, deliberately, and the cost is measured

`src/domain/mergepatch.go`

`touched` crosses `DeriveFunc` and the repository as a slice and stays one: the
geometry clear and `PropagateGate` send it whole as a PostgreSQL array. Every
other consumer asks only for membership, once per resource in the merged catalog
— quadratic against a slice.

A full-mode publish touches every resource, so a 500-resource catalog costs
**~250k string comparisons per pass**, and `derive`, `coverGeometries` and
`writeResources` each make a pass. That is the whole reason both shapes exist.

## 3. A14's pointer has a specific silent failure mode

`src/domain/catalog_repository.go`

A14 says `DeriveFunc` takes `*Catalog`. What the comment recorded and the
amendment does not is *how a value parameter fails*: everything `derive` computes
it delivers by writing onto the merged catalog, and against a value parameter
that works for `merged.Resources[k].Field` — which reaches through the shared
backing array — and silently does not work for `merged.Geometries`, which is a
field assignment on a copy.

The result is a catalog stored with no catalog-level geometry rows, no error
anywhere, and every provider location unfindable.

## 4. `ErrRetrievalDepth` is the guard behind the guard

`src/domain/search_repository.go`

The plan makes the discover mapper the owner of this refusal, and the mapper
checks the same bound against the same `config.Search`, so a request over HTTP is
turned away before a query runs. The sentinel exists for the paths that do not
come through the mapper.

Both sentinels sit on the port rather than in the adapter that raises them for an
import-graph reason: `tests/architecture` forbids `src/discover` from importing
`src/storage/postgres`, so an error declared there is one the request path cannot
match — and an error it cannot match is an error it reports as a 500.

## 5. `PruneOfferReferences` is the only defence that catches a resource that never existed

`src/domain/gate.go`

Three defences cover dangling `resource_ids`. The delete-then-prune pair on a
FULL republish cannot distinguish a first-publish typo from a correct array; the
prune can, because it checks against the merged catalog. It also checks **every**
offer on the merged catalog, not only the ones the patch named — the invariant
worth holding is "every offer ends this transaction referencing resources that
exist", not "every offer we happened to look at".

`resource_ids` carries no foreign key because PostgreSQL cannot declare one into
an array, which is why any of this is needed.

## 6. `Capability.Ranked()` decides two things neither backend would remember

`src/domain/query.go`

Spatial and jsonpath are filters: they are carried by the predicate each ranked
mode already applies, so a backend satisfies them by running the search at all.
The split then decides

- that an **applied** filter is never reported in `X-Beckn-Degraded`, and
- that an intent naming only filters is still a query, rather than a request for
  nothing.

It lives in the package both backends import because a copy in each is a copy
that drifts, and the drift shows up as an empty page with nothing to explain it.

## 7. The plan's `NewCatalogRepository` signature is one parameter short

`src/storage/postgres/catalog_repository.go`

`discover-and-publish.md:4366` says `postgres.NewCatalogRepository(pool) →
domain.CatalogRepository`. The constructor is
`NewCatalogRepository(pool *pgxpool.Pool, resolutionCells int)`: the H3 cover is
computed on the write path, so the repository needs the resolution, and reading
it from config inside the adapter would put a config dependency behind the port.

A deviation from the plan, recorded here because the comment that recorded it was
the only record. The plan is binding, so it is the plan that needs the edit.

## 8. `pruneOrphanedOffers` never fires today, and the statement order is a bet on tomorrow

`src/storage/postgres/catalog_repository.go`

The plan's "the delete runs before the prune" (`discover-and-publish.md:4434`) is
**unobservable through the ports** as the code stands: the domain-side
`PruneOfferReferences` has already fixed every offer the transaction can see, so
the SQL prune finds nothing to do regardless of when it runs. The order is kept
because it is the order that stays correct if the domain-side prune is ever
removed — not because a test can tell the two apart.

## 9. A geometry with no bounding box is undiscoverable, not degraded

`src/storage/postgres/catalog_repository.go`

The box columns are NOT NULL, so `errUnboundedGeometry` refuses the write rather
than storing a row without one. What makes that a refusal rather than a
degradation: for a shape whose cover truncated, the box is not a pre-filter, it
is the **entire** spatial predicate (`discover-and-publish.md:2523`). A row
carrying no box therefore matches no spatial query at all, and nothing in the
response would say so.

## 10. `callersFilter` must not key on `PgError.Routine`

`src/storage/postgres/search_repository.go`

The two SQLSTATEs it matches — 42601 and 2201B — are raised from
`jsonpath_yyerror` and `makeItemLikeRegex`, and keying on those names would
narrow the match to exactly the jsonpath parser. It is deliberately not done:
they are internal PostgreSQL symbols with no compatibility promise, and a rename
between minor versions would turn a 400 back into a 500 silently. The SQLSTATE is
the documented contract.

## 11. `application_name` is a literal and must not become `config.App.Subscriber`

`src/storage/postgres/pool.go`

PostgreSQL truncates `application_name` at 63 bytes. The subscriber id is an
FQDN, so binding the two would silently cut long ones — and a truncated value is
worse than a fixed one here, because `pg_stat_activity` grouped by this column is
the whole reason Task 25 ships no pool-utilisation gauge
(`opentelemetry.md:1246`).

## 12. The limiter's eviction is amortised, and `horizon` is derived rather than configured

`src/platform/middlewares/ratelimit.go`

The plan says only "evicts idle buckets so the map is not a leak"
(`discover-and-publish.md:3673`). Two decisions behind that are not written down.

**`sweep` runs at most once per horizon, from inside `allow`.** A walk of the map
on every request is O(callers) on the hot path, and what is being prevented is
unbounded growth over hours rather than a transient. A background goroutine is
the other answer and is worse: a second lifetime to manage and something to shut
down, for a map only ever read under one mutex.

**`horizon` is `burst / rps`, and there is no knob for it.** That is the time an
empty bucket takes to refill to full, past which a bucket holds exactly what a
new one would — so eviction is unobservable to a caller rather than a second,
hidden allowance. It has exactly one correct value given the other two, and a
knob no scenario sets is not shipped.

## 13. `Trace` no longer stamps `X-Beckn-Chain`, and the plan still says it does

`src/platform/middlewares/recover.go`, `trace.go`

The plan describes `Trace` as a pass-through whose only side effect is appending
`trace` to `X-Beckn-Chain`, so that Task 20's order test has something to observe
at its slot (`discover-and-publish.md:3688`, `:3718`, `:5030`). 23c gave `Trace` a
side effect of its own — the server span — and the marker went with it; the
constant moved to `recover.go`, which is now its only writer.

`Recover` still stamps, so the header is still the order oracle for the one link
that has no other observable placement. The plan is binding and it is the plan
that needs the edit, in all three places.

## 14. Per-resource geometry was the reference implementation's shape

`src/storage/conformance/publish.go`

`discover-and-publish.md:1924-1926` carries the measurement — three provider
shapes on a 40-resource catalog become 120 rows and 120 H3 fills if attached to
each resource — but not where the rejected shape came from. It is the Java
`beckn-discovr` layout, which is why the conformance case exists at all: the
suite pins the catalog-level answer against the design the reader is most likely
to have seen first.

## 15. Declining `bapId`/`bppId` costs nothing, and that is a schema fact

`src/beckn/types.go`, `Context.SenderID`

A24 (`discover-and-publish.md:149`) gives every reason the four legacy
participant fields went, but not the one that makes the removal safe rather than
merely preferred. The spec's PROSE says the context "MUST include at minimum …
`bapId` or `bppId`" — and `Context` carries no `required`, no
`additionalProperties` and no `oneOf`/`anyOf`, so the demand is unenforceable
and an envelope carrying only `senderId`/`receiverId` validates clean.
`senderId` and `receiverId` are declared properties of that same schema, which
makes A24 a SELECTION from the spec's property list rather than a deviation from
it. `Catalog` is the opposite case and is why `Catalog.BppID` stays: it closes
with `additionalProperties: false`, so `bppId` is the only spelling validation
there will accept.
