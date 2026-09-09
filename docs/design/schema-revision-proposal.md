# Schema revision proposal — measured, not asserted

Status: **RESOLVED 2026-08-27.** The decisions were taken and folded into
`discover-and-publish.md` as **A18** (the `filter_doc` composite) and **A19**
(the count query removed); the geometry measurement became a note under
the `resource_geometries` schema. That plan is the spec — this file is kept only for the EVIDENCE,
which does not fit in an amendment row and which the next person to question one
of these numbers will want.

**What was accepted, and what was not:**

| Proposed here | Decision |
|---|---|
| `filter_doc` composite + its GIN index | **Adopted** (A18) |
| Count query removed | **Adopted** (A19) |
| Five tables, splitting a derived `resource_index` off `resources` | **Rejected.** Four tables stand; `filter_doc` is a column on `resources`. The split bought a rebuild story and a clearer storage/index boundary, not a measurement — retrieval scans one table either way, at the same speed. Not worth a 1:1 table split |
| `resource_geometry.resource_id NOT NULL`, catalog shapes copied down | **Rejected for now.** 70 ms to 1.1 ms on the selective query, against ~2x slower broad queries, 4x storage, and a real publish obligation to keep the copies in sync. The number is recorded under the `resource_geometries` schema so it can be revisited against evidence rather than re-derived |

This document exists because the Task 22 design was being argued from memory.
Everything below that says "fast" or "indexable" was measured on
`pgvector/pgvector:0.8.0-pg16` — the same image `make test` uses — over 300,000
synthetic resource rows. The probe scripts and raw `EXPLAIN (ANALYZE)` output are
reproduced in the Evidence section so the next reader can disagree with data
rather than with an opinion.

## The four requirements

1. Read performance is the top priority.
2. Lexical + fuzzy + semantic text search.
3. Spatial search targetable at **any** path in the document.
4. One PostgreSQL SQL/JSON path expression, mixing `&&` and `||` freely across
   catalog, resource and offer levels, **in any order**.

No backward compatibility is required. Every stored catalog will be republished.

## Evidence

### E1 — What the GIN index actually captures

`CREATE INDEX ... USING GIN (filter_doc jsonb_path_ops)`, 300k rows, operator `@?`.
"INDEX" means the plan used a Bitmap Index Scan; measured with `enable_seqscan=off`
to separate *cannot* from *chose not to*.

| # | Expression shape | Plan | Recheck |
|---|---|---|---|
| A | single level, `resources[*]...grade == "A"` | INDEX | 0 |
| B | `&&` catalog + resource | INDEX | 0 |
| C | `&&` catalog + resource + offer | INDEX | 0 |
| D | `\|\|` resource + offer | INDEX | 0 |
| E1 | `(a && b) \|\| c` | INDEX | 0 |
| E2 | `a && b \|\| c` (unparenthesised) | INDEX | 0 |
| E3 | `c \|\| (a && b)` | INDEX | 0 |
| E4 | `(a && b) \|\| (c && d)` | INDEX | 0 |
| E5 | `(a \|\| b) && c` | INDEX | 0 |
| P1–P4 | 3-way `&&`, all four orderings | INDEX | 0 |
| O1–O2 | 3-way `\|\|`, both orderings | INDEX | 0 |
| F | `exists(@.offers[*] ? (x && y))` — same-offer conjunction | INDEX | 0 |
| I | quoted colon field `@."schema:price" == 250` | INDEX (1.0 ms) | 0 |
| H | equality `&&` inequality | INDEX on the equality, recheck for the rest | 67,008 |
| G | inequality alone (`mrp > 900`) | seq scan | — |
| L | `like_regex` | seq scan | — |
| K | RFC 9535 `$[?(@.x == 'y')]` | **`ERROR: syntax error at or near "?"`** | — |
| J | wrong root (`$.resources[*]` instead of `$.catalogs[*]`) | **0 rows, silently** | — |

**Requirement 4 is met, with no caveat about operator or ordering.** This was the
open question and the answer is unambiguous: `jsonb_path_ops` extracts index keys
from `||` as well as `&&`, at any nesting depth, in any order. When the planner
picks a sequential scan it is a costing decision on a non-selective predicate, not
a capability limit — E1 seq-scanned at default settings and used the index when
seq scan was disabled, returning the same rows.

Two findings carry design consequences:

- **J is a trap.** A caller who roots the expression at the wrong level gets
  `200 OK` with zero results, not an error. Silence is the worst possible answer
  here. The root shape must be documented on the endpoint, and a wrong prefix
  should be rejected at the edge rather than returned as an empty page.
- **K confirms C10 mechanically.** RFC 9535 is a parse error in PostgreSQL, so the
  `400 SCH_INVALID_JSONPATH` is forced, not a policy choice. Note that the Beckn
  schema's own `Intent.filters.expression` example is RFC 9535 — the spec's example
  cannot be executed by the engine the spec's `type: jsonpath` implies.

### E2 — Geometry table shape

The single largest performance finding, and it has nothing to do with jsonpath.

Today catalog-level geometry rows carry `resource_id IS NULL` and the retriever
joins with `(g.resource_id IS NULL OR g.resource_id = r.id)`. That `OR` is inside
the join condition, so the planner cannot use it as an equality and cannot drive
from the geometry index. Three shapes measured:

| Shape | Selective cells ("near me", 1 catalog) | Broad cells (region-wide) | Size |
|---|---|---|---|
| **flat** — catalog shapes copied down, `resource_id NOT NULL` | **1.1 ms** | ~1010 ms | 70 MB |
| **or** — nullable `resource_id` (today) | 70 ms | ~465 ms | 18 MB |
| **split** — two tables, `EXISTS ... OR EXISTS ...` | 457 ms | ~897 ms | 18 MB |

Broad figures are the median of six warm runs; selective figures were stable.

The plans explain it. Flat:

```
Nested Loop
  ->  Bitmap Index Scan on geo_flat_cover   (500 rows)
  ->  Index Scan using ri_pkey on ri        (loops=500)
```

OR form:

```
Hash Semi Join   Join Filter: ((g.resource_id IS NULL) OR (g.resource_id = ri.resource_id))
  ->  Parallel Seq Scan on ri               (300,000 rows)
```

Flattening lets the spatial predicate *drive* the query — scan the few hundred
matching shapes, then hit `resource_index` by primary key. The OR form must scan
every resource in the corpus first, whatever the spatial predicate says.

The trade is real and stated plainly: flat is **64× faster** on selective spatial
predicates and **~2.2× slower** on broad ones, for 4× the geometry storage. That is
a good trade, because a spatial predicate covering most of the corpus is not a
filter — it is the absence of one — and the case worth optimising is the one users
actually issue.

### E3 — The full retrieval path

`resource_index` at 300k rows, warm, third run:

| Query | Time |
|---|---|
| retriever: gate + text + jsonpath + geo(flat), `LIMIT 200` | **1.5 ms** |
| retriever: gate + text + jsonpath + geo(OR form), `LIMIT 200` | 139.7 ms |
| retriever: gate + text + jsonpath, no geo, `LIMIT 200` | 104.8 ms |
| **counter: gate + text + jsonpath, no `LIMIT`** | **150.6 ms** |
| counter: gate + text + jsonpath + geo(flat) | 1.4 ms |

The last row of the retriever group and the bolded counter row are the two things
to take away. Retrieval with a selective predicate is low single-digit
milliseconds. **The unbounded count is the bottleneck**, and it is unbounded by
design — `CountCandidates` has no `LIMIT` because capping it would make `Total`
wrong in a way the caller cannot detect. 150 ms at 300k rows scales roughly
linearly; at 3M rows expect well over a second on every discover that carries a
non-selective text query. See Risk 1.

### E4 — `@?` vs `@@`: the operator/form trap

The single most dangerous finding in this document. PostgreSQL has two containment
operators for `jsonpath` and they take **different kinds of expression**:

- `@?` takes a **path** expression — one that returns items. Filter form:
  `$.catalogs[*] ? (@.isActive == true)`
- `@@` takes a **predicate** expression — one that returns a boolean. Predicate
  form: `$.catalogs[0].isActive == true`

Pair them the wrong way and there is **no error**. There is a wrong answer.
Ground truth over 100,000 rows: 85,715 have `isActive == true`, 14,285 have
`false`.

| Expression | Operator | Rows returned | |
|---|---|---|---|
| `$.catalogs[*] ? (@.isActive == true)` | `@?` | 85,715 | correct |
| `$.catalogs[*] ? (@.isActive == false)` | `@?` | 14,285 | correct |
| `$.catalogs[0].isActive == true` | `@?` | **100,000** | **every row** |
| `$.catalogs[0].isActive == false` | `@?` | **100,000** | **every row** |
| `$.catalogs[0].isActive == true` | `@@` | 85,715 | correct |
| `$.catalogs[0].isActive == false` | `@@` | 14,285 | correct |
| `$.catalogs[*] ? (@.isActive == true)` | `@@` | **0** | **nothing** |

Under `@?`, a predicate-form expression matches everything, because `@?` asks
"did the path yield any item at all" and a comparison always yields an item —
`false` is still an item. Under `@@`, a filter-form expression matches nothing.

Two consequences:

- **`path && path` is legal.** `$.catalogs[*].isActive == true && $.catalogs[*].id == "c1"`
  parses fine as a predicate expression and under `@?` returned all 100,000 rows.
  An earlier draft of this document asserted PostgreSQL rejects two roots. It does
  not. The danger is not a `400`; it is a filter that silently does nothing.
- **The form must be validated at the edge**, because both failure modes are
  silent and both are catastrophic — one returns the whole corpus, the other
  returns nothing, and neither logs anything.

Combined with finding J (a correct-form expression rooted at the wrong level
returns zero rows silently), the service has three distinct ways to accept a
filter and answer wrongly without complaint. All three are closed the same way:
pin the accepted form and the required root, reject anything else with a
`400 SCH_INVALID_JSONPATH` that says what was expected.

### E5 — Feature coverage

Every construct below returned **correct** results. This run used
`enable_seqscan=off` to test capability, so it carries no performance signal —
see E1 for which of these actually ride the index at default settings.

Working: equality, `!` negation, inequality, arithmetic, array member
(`tags[*] == "organic"`), array subscript (`tags[0]`, `tags[last]`),
`starts with`, `like_regex`, `.size()`, `.type()`, `.double()`, `is unknown`,
nested `exists()`, recursive descent (`.**`), explicit `strict` and `lax` mode.

`$var` references resolve to nothing, because the service binds no variables —
correctly, since binding caller-supplied variables would reopen the injection
surface that passing the expression as a single bound parameter closes.

## The schema

The central change is not a new column. It is that **`resources` is currently doing
two jobs** — it is both the stored Resource and the thing every retriever scans —
and separating them is what makes the rest legible.

```
SOURCE OF TRUTH            what a publisher sent; one object, one row, one home
  catalogs
  resources
  offers

DERIVED INDEX              rebuildable from the three above; TRUNCATE-and-reindex
  resource_index           one row per discoverable resource
  resource_geometry        one row per (resource, spatial path)
```

The test that keeps the boundary honest, and that should be an actual test:
**drop both derived tables, rebuild them from the three source tables, and every
discover returns byte-identical results.** If that ever stops holding, something
that is really storage has been put in the index.

### Source of truth

```sql
CREATE TABLE catalogs (
    id           TEXT PRIMARY KEY CHECK (id <> ''),
    document     JSONB NOT NULL DEFAULT '{}'::jsonb,   -- Catalog, resources/offers stripped
    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE resources (
    catalog_id TEXT NOT NULL REFERENCES catalogs (id) ON DELETE CASCADE,
    id         TEXT NOT NULL CHECK (id <> ''),
    document   JSONB NOT NULL DEFAULT '{}'::jsonb,     -- the whole Resource, verbatim
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (catalog_id, id)
);

CREATE TABLE offers (
    catalog_id   TEXT NOT NULL REFERENCES catalogs (id) ON DELETE CASCADE,
    id           TEXT NOT NULL CHECK (id <> ''),
    document     JSONB NOT NULL DEFAULT '{}'::jsonb,   -- the whole Offer, verbatim
    -- derived from THIS row's document; local, so it cannot drift the way a
    -- cross-row copy can. Empty resource_ids is catalog-wide, never "none yet".
    resource_ids    TEXT[] NOT NULL DEFAULT '{}',
    valid_from      TIMESTAMPTZ,
    valid_to        TIMESTAMPTZ,
    valid_time_from TIME,
    valid_time_to   TIME,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (catalog_id, id)
);
CREATE INDEX idx_offers_resource_ids ON offers USING GIN (resource_ids);
```

`catalogs` and `resources` lose every scalar they carry today. Nothing reads
`catalogs.active` or `resources.name` any more: retrieval reads `resource_index`,
and the publish path recomputes from `document`. A column derived from the *same
row's* document is fine to keep — `offers.resource_ids` is exactly that, and
`HydrateOffers` needs it. A column derived from *another* row's document is the
thing that belongs in the index, and that is the whole rule.

### The derived index

```sql
CREATE TABLE resource_index (
    catalog_id  TEXT NOT NULL,
    resource_id TEXT NOT NULL,

    -- The scope gate, resolved DOWN from the catalog at publish time. This is a
    -- cross-row copy and therefore lives here rather than on `resources`.
    visible_to      TEXT[]  NOT NULL DEFAULT '{}',
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    valid_from      TIMESTAMPTZ,
    valid_to        TIMESTAMPTZ,
    valid_time_from TIME,
    valid_time_to   TIME,

    -- ranking inputs
    name        TEXT     NOT NULL DEFAULT '',
    search_tsv  TSVECTOR NOT NULL DEFAULT ''::tsvector,
    embedding   VECTOR(768),
    embedding_source_hash BYTEA,

    schema_context TEXT NOT NULL DEFAULT '',
    schema_type    TEXT NOT NULL DEFAULT '',

    -- Requirement 4. One self-contained composite per resource: the catalog with
    -- `availableAt` stripped, this ONE resource under `resources`, and the offers
    -- that apply to it under `offers`. See "Why the composite" below.
    filter_doc JSONB NOT NULL DEFAULT '{}'::jsonb,

    PRIMARY KEY (catalog_id, resource_id),
    FOREIGN KEY (catalog_id, resource_id)
        REFERENCES resources (catalog_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_ri_visible_to ON resource_index USING GIN (visible_to) WITH (fastupdate = off);
CREATE INDEX idx_ri_search_tsv ON resource_index USING GIN (search_tsv) WHERE active;
CREATE INDEX idx_ri_name_trgm  ON resource_index USING GIN (name gin_trgm_ops) WHERE active;
CREATE INDEX idx_ri_schema     ON resource_index (schema_context, schema_type) WHERE active;
CREATE INDEX idx_ri_filter_doc ON resource_index USING GIN (filter_doc jsonb_path_ops);
CREATE INDEX idx_ri_embedding  ON resource_index
    USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
```

```sql
CREATE TABLE resource_geometry (
    catalog_id  TEXT NOT NULL,
    -- NOT NULL, and that is the point (E2). A catalog-level shape is copied down
    -- to every resource of its catalog at publish time, so this join is a plain
    -- equality and the planner can drive the query from the spatial index.
    resource_id TEXT NOT NULL,

    target_path TEXT NOT NULL,   -- the protocol path the caller filters on
    source_path TEXT NOT NULL,   -- where in the document it was found

    geojson     JSONB NOT NULL,
    cells_full  BIGINT[],
    cells_cover BIGINT[],

    min_lat DOUBLE PRECISION NOT NULL,
    max_lat DOUBLE PRECISION NOT NULL,
    min_lon DOUBLE PRECISION NOT NULL,
    max_lon DOUBLE PRECISION NOT NULL,

    CHECK ((cells_full IS NULL) = (cells_cover IS NULL)),
    CHECK (cells_cover IS NULL OR cardinality(cells_cover) > 0),
    CHECK (min_lat <= max_lat AND min_lon <= max_lon),

    PRIMARY KEY (catalog_id, resource_id, source_path),
    FOREIGN KEY (catalog_id, resource_id)
        REFERENCES resource_index (catalog_id, resource_id) ON DELETE CASCADE
);

CREATE INDEX idx_rg_cells_cover ON resource_geometry USING GIN (cells_cover) WITH (fastupdate = off);
CREATE INDEX idx_rg_cells_full  ON resource_geometry USING GIN (cells_full)  WITH (fastupdate = off);
CREATE INDEX idx_rg_target_path ON resource_geometry (catalog_id, resource_id, target_path);
```

`resource_id NOT NULL` also retires `uq_resource_geometries` and its
`COALESCE(resource_id, '')` key — the primary key now says the same thing
directly, which is why the `CHECK (id <> '')` on that column can go.

### The read path

Retrieval touches **one table**, plus one child table when the intent is spatial.
No source table is read until the page is decided.

```sql
SELECT ri.catalog_id, ri.resource_id
  FROM resource_index ri
 WHERE <scope gate>
   AND <schema gate>
   AND (@filter::text IS NULL OR ri.filter_doc @? @filter::jsonpath)
   AND (@spatial_op::text IS NULL OR @geo_negate::boolean <> EXISTS (
         SELECT 1 FROM resource_geometry g
          WHERE g.catalog_id  = ri.catalog_id
            AND g.resource_id = ri.resource_id      -- equality, no OR
            AND (@target_paths::text[] IS NULL OR g.target_path = ANY(@target_paths::text[]))
            AND <cell / bbox / distance predicate>))
   AND <this mode's text predicate>
 ORDER BY <this mode's rank>, ri.catalog_id, ri.resource_id
 LIMIT @row_limit::int;
```

The filter is a **bound `jsonpath` parameter**. Nothing is concatenated and
nothing is interpolated; PostgreSQL's own parser is the entire security boundary,
which is why the service must never decompose the caller's expression.

Hydration then reads the source tables by primary key, ~20 rows:
`resources.document`, `catalogs.document` once per catalog,
`resource_geometry` for the shapes, `offers` for the applicable offers.

## Why the composite

Requirement 4 forces it, and the forcing is worth spelling out because it is the
one place where a cheaper design is genuinely impossible rather than merely worse.

A `jsonpath` is evaluated by PostgreSQL against **one** `jsonb` value. There is no
form of `@?` that spans three tables. So a caller expression touching catalog,
resource and offer levels can only run if those three levels are present in a
single `jsonb` value at query time.

The alternative would be to parse the caller's expression, split it into a
per-level piece, run each against its own table and recombine. That fails twice.
It fails on `||`, which cannot be recombined per-table at all —
`catalog.x == 1 || offer.y == 2` is a row-level disjunction and no per-table
filter produces it. And it fails on principle: writing that splitter means owning
a `jsonpath` parser, which is the exact surface the "never interpolate, never
decompose" rule exists to avoid. The Java `beckn-discovr` reference took this road
and its `JsonPathConverter` is now regex-rewriting quotes and colon-bearing field
names in user input.

Grain follows from the same kind of argument. Discover retrieves, ranks and
paginates **resources**, and `CountCandidates` has no `LIMIT`. So the filter must
reduce to a per-resource boolean in the `WHERE` clause. One composite per resource
row is what that means.

Shape, per resource row:

```json
{"catalogs": [{
    "id": "c-hul", "isActive": true, "descriptor": {...}, "provider": {...},
    "resources": [ <this ONE resource> ],
    "offers":    [ <offers naming it, plus catalog-wide offers> ]
}]}
```

Three properties fall out of this shape, all of them useful:

- **Every documented path prefix still works.** `$.catalogs[*].resources[*]...` is
  what a publisher sent and what a filter names. There is no rebase, no prefix
  stripping, no per-predicate column routing.
- **`@.resources[*]` means "this resource" by construction.** The array holds
  exactly one element, so the existential quantifier is exact. Cross-resource
  false positives are impossible without a rule anyone has to remember.
- **Same-offer conjunction is available when wanted.**
  `@.offers[*].a == 1 && @.offers[*].b == 2` matches if *some* offer has `a` and
  *some* offer has `b`; `exists(@.offers[*] ? (@.a == 1 && @.b == 2))` requires one
  offer with both. Both are indexed (D and F above). This is the caller's choice
  to make and the service should document it rather than pick.

Offer applicability inside `filter_doc` mirrors `HydrateOffers` exactly — an offer
naming this resource, or a catalog-wide offer with empty `resourceIds`. If the two
rules drift, a caller filters on an offer that the response then does not contain.
That agreement should be pinned by a test, not by this paragraph.

`availableAt` is stripped from `filter_doc`. It is the largest thing in a catalog,
spatial filtering has its own dedicated path, and leaving it in would inflate every
row for a predicate no one issues through this door.

## Requirement-by-requirement

| Requirement | Where it is met | Measured |
|---|---|---|
| 1. Read performance | retrieval is single-table; spatial drives from its own index; source tables untouched until hydration | 1.5 ms retriever |
| 2. Lexical search | `search_tsv` + GIN, `discover_tsquery` | E3 |
| 2. Fuzzy search | `name` + `gin_trgm_ops` | E3 |
| 2. Semantic search | `embedding` + HNSW; RRF fusion in Go | (provider is `noop` in Phase 1, A5) |
| 3. Geo on any path | `resource_geometry.target_path`, one row per path | E2 |
| 4. JSONPath, any order, `&&`/`\|\|` | `filter_doc @? $1` on the composite | E1: 20/20 shapes |

## Risks, stated rather than buried

**Risk 1 — the unbounded count is the bottleneck (150 ms at 300k rows).**
`CountCandidates` has no `LIMIT` on purpose, so an exact `Total` costs a full pass
over the matching set on every request. This is the number that will decide whether
the design holds at 3M rows. Three ways out, in preference order: (a) accept it and
measure at real scale first; (b) make `Total` a bounded count with an explicit
"at least N" wire contract; (c) cache the count per (intent, page-zero) for a few
seconds. **(b) changes the wire shape and needs a decision, not an implementation.**

**Risk 2 — `filter_doc` write amplification.** A catalog with 500 resources means
500 copies of the catalog document. A MERGE touching `descriptor` rewrites all 500,
as does publishing one catalog-wide offer. Publishes are bulk and infrequent and
reads are hot, so the trade is right — but a publish of a large catalog will be
measurably slower than today and that should be measured, not assumed.

**Risk 3 — `filter_doc` storage.** ~850 bytes/row in the probe with a small catalog
document; real ones are larger. Mitigated by stripping `availableAt`, and by
`jsonb_path_ops` needing no heap recheck for equality predicates (recheck = 0 in
every equality case above), so the cost is disk rather than query CPU.

**Risk 4 — the silent wrong-root (finding J).** Must be closed at the edge, not
left to the caller.

**Risk 5 — broad spatial predicates.** ~1 s regardless of shape. Inherent: a cover
matching most of the corpus is not a filter. Worth a documented cap on query cover
size rather than a schema change.

## Decisions this proposal makes, and what it rejects

1. **Catalog-wide offers stay inside `filter_doc`.** The Java reference splits them
   into a `provider_offer` table resolved after filtering, which makes them
   permanently unfilterable. Keeping them in costs storage and buys correctness.
2. **Colon-bearing JSON-LD field names are the caller's job to quote** —
   `@."schema:price"`. Measured working and fast (finding I). The reference
   regex-rewrites them in user input; that is parser ownership by another name.
3. **One expression FORM is mandated and the other is rejected at the edge.**
   See E4 — this is not a style preference, it is the difference between a
   correct answer and silently matching the entire corpus. `path && path` is
   *not* a syntax error, which is what an earlier draft of this document
   claimed.
4. **Expired offers stay in `filter_doc`.** The SQL validity gate decides what is
   returned. Rebuilding the composite on every offer expiry is not viable.
5. **Cross-catalog offers are out of scope.** The reference supports them;
   `beckn.yaml` neither requires nor forbids them.

6. **A catalog must hold at least one resource AFTER the merge.** `beckn.yaml`
   permits offer-only catalogs (`anyOf: [required: [resources],
   required: [offers]]`), but every retriever selects from the resource index, so
   such a catalog is accepted and can never be discovered — the same class of
   defect A17 fixed. Rejected at publish.

   **The check is on the merged catalog, not the request body.** Publish is an
   RFC 7396 merge, so a publisher who already has `c-hul` → `r-atta` stored and
   now sends only an offer for `r-atta` is adding to a catalog that does hold a
   resource. Rejecting that would break the ordinary incremental publish. Only a
   catalog that would end up with zero resources is refused.

   | Case | |
   |---|---|
   | new catalog, offers only, no resources | reject |
   | stored catalog + resource, publish an offer naming it | accept |
   | stored catalog + resource, publish a catalog-wide offer (`resourceIds: []`) | accept |

   An offer with empty `resourceIds` stays legal and stays catalog-wide. It is
   not "no resources yet", `HydrateOffers` already depends on that reading, and
   `Offer` is `required: [id]` in the schema — so demanding `resourceIds` would
   reject schema-valid documents.

   The code is the existing `BIZ_ITEM_NOT_FOUND` with its own message; the
   `ErrorCode` enum is closed at 76 members and a new one cannot be invented.
   That member already answers the sibling case — an offer naming a resource the
   merged catalog does not hold — and its doc comment already reads "an item is
   what the spec calls a resource".

   Note for implementers: L1 schema validation runs BEFORE the merge and
   `Catalog` is `required: [id, descriptor, provider]`, so an incremental
   offer-only publish must still resend `descriptor` and `provider` or it is a
   400 before this rule is ever reached. Existing behaviour, not added here.

## Open, and needing a decision

- Risk 1's option (b) — bounded `Total` — is a wire-contract change. Note that
  `Total` is currently DROPPED at the HTTP boundary (`src/discover/service.go:153`
  — "OnDiscoverAction admits `catalogs` alone"), so the count query costs ~150 ms
  per discover to produce a number no caller receives. Cheapest first move is to
  stop issuing it until `Total` is actually on the wire.
