# Publish and Discover

The two APIs this service exists for, how they are put together, and what to
set to run it. If you want to try it first, start with
[`quickstart.md`](quickstart.md).

## Introduction

A **provider** — a weather agency, a seed supplier, an advisory service —
publishes a catalog describing what it offers. A **consumer** searches across
every catalog published to the network and gets back the parts that matched.
That is the whole of it:

| Route | Who calls it | What it does |
|---|---|---|
| `POST /publish` | a provider | creates or patches one or more catalogs |
| `POST /discover` | a consumer | searches across published catalogs |
| `GET /healthz` | the platform | is the process alive |
| `GET /readyz` | the platform | can it also reach PostgreSQL |

Four routes, no wildcard mount, no aliases. Both APIs are implemented against
the published Beckn v2.0.0 specification —
[`beckn/protocol-specifications-v2`](https://github.com/beckn/protocol-specifications-v2/blob/main/api/v2.0.0/beckn.yaml),
pinned in this repo as
[`tests/testdata/beckn-v2.0.0.yaml`](../tests/testdata/beckn-v2.0.0.yaml). That
document *is* the validator: the service loads it and validates every request
against it, rather than restating the protocol in Go.

Every request is an envelope of `context` (who is asking, about what, when)
plus `message` (the actual payload).

**Every transaction is synchronous.** The answer arrives in the body of the
same HTTP response — there is no callback, no callback URI to register, and
nothing is posted back to the caller later. The response carries the `on_`
action name the protocol gives the asynchronous form, and that naming is all
that survives of it. Two actions in each direction:

| Request | Response |
|---|---|
| `catalog/publish` | `catalog/on_publish` |
| `discover` | `on_discover` |

`publish` is accepted as a synonym for `catalog/publish` and answered in the
caller's own spelling.

The two parties are `context.senderId` and `context.receiverId`, and the
response **swaps** them — the legs reverse, so this service's `senderId` is
whatever the caller put in `receiverId`. They are DIDs, which resolve to the
document holding that party's verification keys, so one field answers both
*who* and *with what key*. Neither is verified today; signature verification
belongs to the adopter's layer.

The v1 participant fields are **not modelled** — not deprecated, absent. A
caller may still send them and gets a 200; they are accepted and ignored, and
they do not come back.

Two things about this service are worth knowing up front, because they explain
most of the design:

- **PostgreSQL is the only datastore.** No PostGIS, no Elasticsearch, no
  separate vector database. Geometry is answered with H3 cells in `BIGINT[]`
  columns and array operators; text with PostgreSQL's own full-text and trigram
  indexes. See [ADR-0005](adr/0005-spatial-index-h3-not-postgis.md) and
  [Geometry](#geometry-without-postgis).
- **Nothing is silently widened or silently dropped.** A constraint this
  service cannot honour is either a refusal that names it, or an answer with a
  header saying what was missing. A caller who filtered for one manufacturer
  and got every manufacturer has been actively misled, so that outcome does not
  exist here.

## High-level design

### The request path

Every protocol request goes through the same chain, outermost first:

```
RequestID → Trace → RequestLogger → Recover → Envelope
          → RateLimit → [Signature] → SchemaValidator → controller
```

- **Envelope** parses and validates the Beckn `context`, and caps the body.
- **SchemaValidator** checks the payload against the Beckn v2.0.0 schema (L1).
  The service loads that specification before it serves and refuses to start
  without it, whether or not L1 validation is switched on.
- **Signature** is a reserved slot with nothing in it. Request signing is not
  built, so `AUTH_ENABLE_SIGNATURE_VERIFICATION=true` refuses the boot rather
  than reporting a security control that is not running.
- **Recover** sits *inside* RequestLogger, so the 500 a recovered panic
  produces still leaves through the logger's response wrapper and gets counted.

The two probes get a much shorter chain — `RequestID → Recover` only. No rate
limit, because shedding a kubelet's probe is how a healthy pod gets restarted
by the mechanism meant to notice it was healthy.

### The packages

```
cmd/discovery-service/   the binary's main
src/app/                 composition root — builds and wires everything
src/beckn/               protocol types, actions, error codes
src/domain/              catalog, query, validity, merge-patch — no I/O
src/publish/             the publish request path
src/discover/            the discover request path
src/indexing/            H3 geometry covers, and the embedding seam
src/storage/             postgres/ and memory/, plus the conformance suite
                         both must pass
src/platform/            config, logging, errors, middleware, validation,
                         telemetry, jsonpath
```

The shape is the same on both paths: a **controller** decodes and answers, a
**service** decides, a **repository port** persists or retrieves. `src/domain`
holds the rules and touches no I/O, which is what lets the in-memory and
PostgreSQL backends be tested against one shared conformance suite.

### The data model

Four tables:

| Table | Holds |
|---|---|
| `catalogs` | one row per catalog, with the provider document |
| `resources` | one row per resource, plus its derived search columns |
| `resource_geometries` | one row per geometry found anywhere in the catalog |
| `offers` | one row per offer, with the resource ids it applies to |

The published JSON is stored as-is and the *searchable* parts are derived
beside it on write: a `tsvector` for lexical search, a trigram index on the
name for typo tolerance, a `filter_doc` for attribute filters, two H3 cell
arrays per geometry, and an embedding column for semantic search that ships
empty (see [What is deferred](#what-is-deferred)).

Deriving on write is what keeps read-time work small — a discover query is
index lookups and array overlaps, not JSON traversal.

## How publish works

### The request

```json
{
  "context": {
    "domain": "agriculture",
    "action": "catalog/publish",
    "version": "2.0.0",
    "senderId": "weather.karnataka.example.org",
    "receiverId": "discovery.local-network.oan",
    "transactionId": "3f9a1c62-4d5e-4a7b-9c8d-1e2f3a4b5c6d",
    "messageId": "8b7c6d5e-4f3a-42b1-a0c9-d8e7f6a5b4c3",
    "timestamp": "2026-08-27T06:30:00Z",
    "schemaContext": ["https://schemas.openagrinet.global/…#openagrinet:WeatherAdvisoryCapability"]
  },
  "message": {
    "catalogs": [
      {
        "id": "cat-ksndmc-weather-advisory",
        "provider": { "id": "prov-ksndmc", "descriptor": { … }, "availableAt": [ … ] },
        "resources": [ … ],
        "offers": [ … ]
      }
    ],
    "publishDirectives": [
      {
        "catalogId": "cat-ksndmc-weather-advisory",
        "catalogType": "REGULAR",
        "visibleTo": ["local-network"]
      }
    ]
  }
}
```

`publishDirectives` is optional, and so is every field in it — note that the
directive above names no `updateMode` and gets `MERGE`. An absent directive
means exactly what a directive naming only `catalogId` means: `REGULAR`,
`MERGE`, visible to the publishing network. The defaults are applied field by
field, so a single-network republish needs no boilerplate.

The full request is
[`examples/01-publish-weather-advisory.json`](../examples/01-publish-weather-advisory.json).

### The response

```
curl -s localhost:8080/publish -H 'Content-Type: application/json' \
  -d @examples/01-publish-weather-advisory.json | jq '.message.results'
```

```json
[
  {
    "catalogId": "cat-ksndmc-weather-advisory",
    "status": "ACCEPTED",
    "stats": { "itemCount": 3, "providerCount": 1, "categoryCount": 1 }
  }
]
```

One result per catalog, and `status` is one of three:

| Status | Means |
|---|---|
| `ACCEPTED` | it all landed |
| `PARTIAL` | it landed, minus something named in `errors` |
| `REJECTED` | nothing was written; `errors` says why |

`PARTIAL` is the interesting one. If one geometry out of forty cannot be read,
the other thirty-nine and the whole rest of the catalog land — but the
publisher is told, in `status` as well as in `errors`, because tooling branches
on the enum and not on an array it may never read.

`itemCount` counts the resources **in this request** that landed, not the
resources in the catalog. A `MERGE` carrying one resource into a forty-resource
catalog reports `1`.

### Two rules worth knowing

**One transaction per catalog, not per request.** A request carrying nine good
catalogs and one bad one lands the nine and rejects the one. Wrapping the whole
request would make one publisher's mistake another publisher's outage.

**`MERGE` is an RFC 7396 merge-patch; `FULL` is a replace.** Under `MERGE`, a
field the publisher omitted is a field left alone, and `null` is how you delete
one. Under `FULL`, resources the payload did not mention are deleted. This is
why the request is mapped to a *patch* rather than to a filled-in object —
a struct whose defaults are already populated cannot tell "the publisher
omitted `isActive`" from "the publisher sent `true`", and under `MERGE` that
ambiguity is data loss.

**Phase 1 accepts regular resources only.** `catalogType: MASTER`, or any
resource directive with `extends`, is rejected outright rather than partially
handled.

## How discover works

### The request

The envelope is the same; the work is in `message.intent`, which carries up to
three independent criteria:

```json
{
  "message": {
    "intent": {
      "textSearch": "weather advisory",
      "spatial": [{
        "op": "S_DWITHIN",
        "targets": "$.catalogs[*].resources[*].resourceAttributes.coverageAreas[*]",
        "geometry": { "type": "Point", "coordinates": [75.02, 15.47] },
        "distanceMeters": 25000
      }],
      "filters": {
        "type": "jsonpath",
        "expression": "$.catalogs[*].resources[*] ? (@.resourceAttributes.geographicGranularity == \"Village\")"
      }
    }
  }
}
```

- **`textSearch`** is free text. It runs lexical and fuzzy retrieval
  concurrently and fuses the two rankings.
- **`spatial`** is one CQL2 constraint: an operator, a GeoJSON geometry, and a
  JSONPath saying *which* published geometries to test it against.
- **`filters`** is a PostgreSQL SQL/JSON path expression over the response
  document, run verbatim.

`context.schemaContext`, if present, narrows the search to resources published
under those schema types. Omit `context.networkId` and the search spans every
network — unlike publish, where omitting it means "my own network".

### The response

```
curl -s localhost:8080/discover -H 'Content-Type: application/json' \
  -d @examples/06-discover-filter-granularity.json | jq '.message'
```

Abridged — `…` stands for a document reproduced as published:

```json
{
  "catalogs": [
    {
      "id": "cat-ksndmc-weather-advisory",
      "isActive": true,
      "provider": { "id": "…", "descriptor": { … }, "availableAt": [ … ] },
      "resources": [
        { "id": "res-wx-village-belagavi", "descriptor": { … }, "resourceAttributes": { … } }
      ],
      "offers": [
        {
          "id": "offer-wx-free-tier",
          "descriptor": { "code": "FREE-TIER", "name": "Free public advisory tier" },
          "resourceIds": ["res-wx-village-belagavi", "res-wx-alert-statewide"]
        }
      ]
    }
  ]
}
```

Catalogs, each carrying **only the resources that matched** and only the offers
that touch one of them (plus any catalog-wide offer). Offer validity is checked
here and nowhere else — a live catalog routinely carries last month's offer.

### Degradation is a header

```http
X-Beckn-Degraded: semantic
```

When a retrieval mode the intent wanted is unavailable, the request still
answers and the header names what was missing. In the local stack that header
appears on every text query, because the semantic mode is deferred; it is
absent on a purely spatial one, which wants no text mode at all.

It is a header rather than a body field because `OnDiscoverAction` declares
`additionalProperties: false` with `catalogs` as its only property — a
`degraded` key inside `message` would be a response that fails its own schema.

Set `SEARCH_FAIL_ON_UNAVAILABLE_MODE=true` to turn the same situation into a
400 instead. The one option never taken is silence.

### The examples, and what each one shows

Every one of these runs against the catalog published above.

| Example | Intent | Returns |
|---|---|---|
| `02-discover-text-search.json` | `textSearch: "irrigation spraying advisory"` | `res-wx-point-dharwad`, `res-wx-village-belagavi` |
| `04-discover-spatial-dwithin.json` | `S_DWITHIN` 25 km of (75.02, 15.47) | `res-wx-point-dharwad`, `res-wx-village-belagavi` |
| `06-discover-filter-granularity.json` | text + `geographicGranularity == "Village"` | `res-wx-village-belagavi` |
| `10-discover-text-and-geo.json` | `"cotton cyclone"` + the same radius | `res-wx-point-dharwad` |
| `13-discover-fuzzy-typos.json` | `"Vilage weathr advisery Belgavi"` | `res-wx-village-belagavi` |
| `08-discover-invalid-jsonpath.json` | a filter with no `?` test | HTTP 400, `SCH_INVALID_JSONPATH` |

The third resource, `res-wx-alert-statewide`, is deliberately geometry-free —
its coverage area is an ISO-3166-2 code and nothing else — so no spatial query
reaches it.
[`examples/README.md`](../examples/README.md) explains the fixture in full.

### Retrieval, in one picture

```
intent ──▶ mapper ──▶ SearchQuery ──▶ lexical  ─┐
             │                        fuzzy    ─┼─▶ RRF ─▶ page ─▶ hydrate
             │                        semantic ─┘           │
             └── faults ──▶ 400                             └─▶ count
```

Each enabled mode retrieves ranked ids concurrently under one deadline. A mode
that errors is recorded as degraded rather than failing the request — three
modes returning is a better answer than none. The rankings are fused with
Reciprocal Rank Fusion, the page is sliced, and only then are the ~20 rows on
that page hydrated into full documents.

Two numbers bound this, and both matter:

- **`SEARCH_MAX_CANDIDATES_PER_MODE`** (default 500) caps how many ids one mode
  may return into fusion. Without it, a broad query — and broad is the common
  case, since the text query ORs its terms — ships tens of thousands of ids to
  Go to be sorted and thrown away.
- That cap is therefore also the **reachable pagination depth**. `offset +
  limit` beyond it is a 400 naming the boundary, not an empty page that looks
  like the end of the results.

`Total` counts the whole candidate set, which for a broad text query is large.
It describes the pool, not the page — precision is the ranking's job.

### Geometry without PostGIS

H3 has no point-in-polygon and no intersects. Represent a geometry as a *set of
cells*, though, and the CQL2 operators become set algebra — which PostgreSQL
does natively on a GIN-indexed `BIGINT[]`.

Every stored geometry gets two covers:

| Column | Meaning |
|---|---|
| `cells_full` | cells lying **entirely inside** the geometry |
| `cells_cover` | cells touching the geometry **at all** |

which gives the invariant everything rests on:

```
cells_full  ⊆  the true geometry  ⊆  cells_cover
```

Two covers, because one proves positives and the other proves negatives:
`cells_full` is a guaranteed subset, so what it asserts is true; `cells_cover`
is a guaranteed superset, so what it rules out is really ruled out. Between
them is a band one cell wide where neither proof fires — and there the rule is
**a geometry that cannot be proven to fail is returned**. Results are a
superset of the truth, never a subset. A `S_DWITHIN` on two points is then
refined with an exact haversine distance.

Seven of the nine CQL2 operators are answered: `S_INTERSECTS`, `S_DISJOINT`,
`S_WITHIN`, `S_CONTAINS`, `S_OVERLAPS`, `S_EQUALS`, `S_DWITHIN`. `S_TOUCHES`
and `S_CROSSES` are refused with `SCH_TYPE_NOT_SUPPORTED` — not "not yet", but
"not approximable by a cell decomposition at any resolution". The message says
which, because a caller deciding whether to wait for a later release needs to
know it never arrives.

All three quantifiers work: `ANY` (the default), `ALL`, `NONE`.

### Refusals

A discover request that cannot be honoured as asked is a 400 with a NACK naming
the field, not an empty page:

```json
{
  "message": {
    "status": "NACK",
    "error": {
      "code": "SCH_INVALID_JSONPATH",
      "message": "the expression has no ? (...) filter, so it selects rather than tests …",
      "details": { "path": "$.message.intent.filters.expression" }
    }
  }
}
```

An unrecognised SRID, a radius over the configured maximum, a second spatial
constraint, an unparsable geometry, a filter rooted somewhere other than
`$.catalogs` — each is a fault, none is a skip. The reference implementation
skips constraints it cannot handle, which widens the result set: a caller who
asked for "within 5 km" gets the whole country, and a 200.

## Configuration

Four layers, lowest first:

```
struct defaults  →  config/common.yaml  →  config/instance.yaml  →  environment
```

`common.yaml` is committed and reviewed, so everything in it is public.
`instance.yaml` is mounted per deployment. **Secrets arrive only from the
environment** — that is exactly why it sits on top of both files. A key that
matches no field fails the boot, so a typo cannot silently do nothing.

### The settings

| Variable | Default | What it decides |
|---|---|---|
| `APP_NETWORK_ID` | — (required) | this node's network; publish's default `visibleTo` |
| `APP_SUBSCRIBER_ID` | — | this node's subscriber id |
| `APP_DOMAIN` | — | the Beckn domain served |
| `APP_DEFAULT_TIMEZONE` | `Asia/Kolkata` | the zone daily validity windows are read in |
| `DATABASE_URL` | — (required) | connection string; **environment only** |
| `DATABASE_MAX_CONNS` / `MIN_CONNS` | `32` / `4` | pool bounds |
| `DATABASE_AUTO_MIGRATE` | `false` | apply migrations on boot |
| `SERVER_PORT` | `8080` | listen port |
| `SERVER_SHUTDOWN_TIMEOUT` | `15s` | graceful drain |
| `SERVER_MAX_REQUEST_BODY_BYTES` | `10485760` | body cap |
| `SEARCH_DEFAULT_PAGE_SIZE` | `20` | page size when the request names none |
| `SEARCH_MAX_PAGE_SIZE` | `100` | ceiling a larger `limit` is clamped to |
| `SEARCH_MAX_CANDIDATES_PER_MODE` | `500` | ids per mode into fusion — and the pagination depth |
| `SEARCH_MAX_RADIUS_METERS` | `200000` | largest `S_DWITHIN` accepted |
| `SEARCH_READ_DEADLINE` | `2s` | one deadline across all retrieval modes |
| `SEARCH_FAIL_ON_UNAVAILABLE_MODE` | `false` | `true` turns a degradation into a 400 |
| `GEO_RESOLUTION_CELLS` | `8` | H3 resolution for both covers |
| `EMBEDDING_PROVIDER` | `noop` | `noop`, `hashing` or `ollama` |
| `EMBEDDING_MODEL` / `_ENDPOINT` / `_DIMENSIONS` | `nomic-embed-text` / `http://localhost:11434` / `768` | the Ollama seam |
| `RATE_LIMIT_RPS` / `_BURST` | `20` / `40` | per-client limit; `burst >= rps` is enforced |
| `VALIDATION_ENABLE_L1_SCHEMA` | `true` | Beckn schema validation of the payload |
| `VALIDATION_SPEC_URL` | — | where to fetch the spec if it is not cached |
| `VALIDATION_SPEC_CACHE_PATH` | `.cache/beckn/beckn.yaml` | where the spec is read from |
| `AUTH_ENABLE_SIGNATURE_VERIFICATION` | `false` | `true` refuses the boot — see below |
| `EXT_ALLOW_NETWORK_FETCH` | `false` | may a URL from a *request body* be fetched |
| `OTEL_EXPORTER` | `none` | `none` or `otlp` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | — | collector endpoint when exporting |
| `LOG_LEVEL` | `info` | zap level |
| `ERROR_INCLUDE_LEGACY_TYPE` | `false` | emit the v1 `type` field beside `code` |

Three of these are load-bearing enough to spell out:

- **`APP_NETWORK_ID` is publish's default and not discover's.** A publish with
  no `visibleTo` is visible to this network. A discover with no
  `context.networkId` searches *every* network — a different field answering a
  different question, and defaulting it here would quietly put discover back to
  single-network scoping under a name suggesting it is unscoped.
- **`EXT_ALLOW_NETWORK_FETCH=false`** is the whole statement that a configured
  registry URL is trusted and a URL arriving in a request body is not.
- **`AUTH_ENABLE_SIGNATURE_VERIFICATION=true` refuses the boot.** The Ed25519
  primitives are not built and the middleware is not written, so nothing sits
  behind the flag. Failing loudly beats reporting a control that is not
  running.

The `docker-compose.yml` in the repo root is a worked example of all of this.

## What is deferred

Named here so it is not mistaken for a defect:

- **Semantic search.** The embedding column, the HNSW index, the `Embedder`
  seam and the fusion all ship. The provider does not — `EMBEDDING_PROVIDER`
  defaults to `noop`, which is why text queries report `X-Beckn-Degraded:
  semantic`. `make test` pins `hashing` so the whole path is still exercised.
- **Request signing.** Only the slot in the middleware order exists.
- **Master catalogs and resource inheritance.** Rejected at intake, not
  partially handled.

## Where next

- [`quickstart.md`](quickstart.md) — run it locally in about five minutes
- [`telemetry.md`](telemetry.md) — traces, metrics and logs
- [`design/implementation-plan.md`](design/implementation-plan.md) — the
  binding specification: the DDL, the wire contracts, every acceptance scenario
  and the reasoning behind each schema decision
- [`adr/`](adr/) — why the significant technical choices went the way they did
