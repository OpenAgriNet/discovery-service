# Examples

A worked publish and the discover requests that find it, against a local stack.

```
make run                # PostgreSQL + the service on :8080
make verify             # publish, then assert every retrieval path
make newman             # the same checks through the Postman collection
make audit              # check the results are RIGHT, not just stable
```

`verify.sh` and the collection both assert hard-coded id sets. That catches
regressions, but those sets were written by watching the service run — so if it
had been wrong that day, they would have faithfully frozen the wrong answer.
`audit.py` exists to close that hole. It does not trust the service at all:

- **oracle** — recomputes what each intent *should* match directly from
  `01-publish-…json`, using point-in-polygon and haversine code written from
  scratch rather than the service's H3 covering, and compares.
- **schema** — validates each response against `beckn.yaml`. What this service
  returns synchronously is the `/on_discover` and `/catalog/on_publish`
  *request-body* schemas, since those are the callback shapes.
- **fidelity** — every returned resource must be identical, member for member,
  to what was published. That is A17 as a test: one row holds the object
  verbatim and every other column is derived from it, so a response that
  quietly dropped a member of `resourceAttributes` would pass every id-set
  assertion here and still be wrong.

The schema step needs `jsonschema` and `pyyaml` (`pip install jsonschema
pyyaml`). Without them the other checks still run and the tally names the
skipped ones — "24 passed" and "16 passed" both read as success otherwise, and
the difference between them is every schema assertion in the run.

Both runners assert the **exact** set of resource ids each request returns, not
that it returned something. A filter that has quietly stopped filtering still
returns rows, and every "contains" assertion passes for it. The cases are built
so that each one *excludes* something another includes — if the predicates were
being ignored, all of them would return all three resources, and the
disagreements are what catch that.

## The fixture

`01-publish-weather-advisory.json` — one catalog from a fictional KSNDMC,
modelled on the OpenAgriNet **WeatherAdvisoryCapability** schema, which is what
a provider attaches to `resourceAttributes`. Three resources, chosen to differ
along every axis discovery can filter on:

| Resource | Granularity | Geometry | Text |
|---|---|---|---|
| `res-wx-village-belagavi` | Village | Polygon over Belagavi | irrigation, spraying, crop protection |
| `res-wx-point-dharwad` | Point | Point at 75.0078, 15.4589 | spraying, irrigation |
| `res-wx-alert-statewide` | District | **none** — ISO-3166-2 code only | severe weather alerting |

The statewide alert is deliberately geometry-free. It carries
`{"codeScheme": "ISO-3166-2", "areaCode": "IN-KA"}` and no coordinates, so
nothing is cell-indexed for it and **it cannot be found spatially at all**.
That is the designed behaviour — the index covers GeoJSON, and a governed area
code is not GeoJSON — and cases 04 and 05 assert it out loud rather than
leaving it as a surprise for whoever publishes a code-only catalog and wonders
where it went.

Two offers split the resources, which is what makes case 07 meaningful:
`offer-wx-free-tier` names the village and alert resources,
`offer-wx-subscription` names the point.

The two geometries also land on either side of the cell budget, which is
useful by accident and worth knowing about. The Dharwad point gets a single H3
cell and is matched through the cell index. The Belagavi polygon spans roughly
14,000 km², over `MaxIndexCoverCells` (~6,000 km² at resolution 8), so **both**
cell columns are stored nil and its bounding box decides alone. That is
deliberate: a cover truncated to fit the budget would make the shape
discoverable only in whichever corner the fill happened to reach, which is a
wrong answer wearing the shape of a right one. The practical consequence is
that matching against a large polygon is bounding-box coarse — it can return a
resource whose true geometry the query misses. Check
`array_length(cells_cover, 1)` in `resource_geometries` if a match looks too
generous.

## The requests

| File | Exercises | Returns |
|---|---|---|
| `02-discover-text-search.json` | lexical retrieval | village + point, **not** the alert |
| `09-discover-text-or.json` | lexical retrieval ORs its terms | all three |
| `03-discover-schema-context.json` | `context.schemaContext` | all three |
| `04-discover-spatial-dwithin.json` | `S_DWITHIN`, 25 km of Dharwad | point + village |
| `05-discover-spatial-intersects.json` | `S_INTERSECTS`, inside Belagavi | village alone |
| `06-discover-filter-granularity.json` | jsonpath, resource-rooted | village alone |
| `07-discover-filter-cross-level.json` | jsonpath, **offer**-rooted | point alone |
| `08-discover-invalid-jsonpath.json` | the form gate | **400** |
| `10-discover-text-and-geo.json` | text **and** geo | point alone |
| `11-discover-geo-and-filter.json` | geo **and** filter, no text at all | village alone |
| `14-discover-text-and-filter.json` | text **and** filter, no geometry | alert alone |
| `12-discover-text-geo-filter-empty.json` | all three, pairwise-overlapping | **empty** |
| `15-discover-text-geo-filter.json` | all three, non-empty | village alone |
| `13-discover-fuzzy-typos.json` | trigram mode, every term misspelled | village alone |

Several of these are worth the extra sentence.

**09 exists because 02 could not tell OR from AND.** Every term in 02's
query appears in both of the resources it matches, so the case passes
identically under either reading — it was pinning a result while leaving the
semantics assumed. 09 splits the terms: `irrigation` is only in the village
and point resources, `cyclone` only in the statewide alert. All three coming
back is OR; under AND none would. This gap was found by breaking the audit's
oracle on purpose — flipping it to AND changed nothing, which is how a test
tells you it is not testing what you thought.

**04 and 05 have to disagree.** Both are spatial, over the same target path.
04's circle reaches the Dharwad point; 05's coordinate sits deep inside the
Belagavi polygon and roughly 120 km from that point. A spatial predicate that
had degraded to matching the whole catalog would answer both identically.

**07 is rooted at the offer and yet narrows resources.** That is the single
composite `filter_doc` column doing its job (A18). Under the earlier design —
one filter column per table, routed by prefix — an offer-rooted predicate could
not select a resource at all, and a cross-level disjunction had no correct
decomposition. If 07 starts returning all three resources, that regressed.

**08 is a refusal, and the refusal is the feature.** It is case 06's intent
written without the `? (...)` filter:

```
$.catalogs[*].resources[*].resourceAttributes.geographicGranularity == "Village"
```

PostgreSQL runs that happily. `@?` expects a *path*; given a comparison it gets
an item back, `false` is an item, and so it answers true for every row. The
caller receives the entire corpus formatted as a filtered page with no error
anywhere — nothing in the response distinguishes it from an honest result. The
service rejects the shape before the cast instead.

## Constraints intersect, retrieval modes union

These are two different rules and cases 10–13 exist to separate them.

The formula, precisely:

```
( lexical_tsvector  OR  fuzzy_trigram  OR  semantic_vector )   <- modes UNION
      AND geo  AND jsonpath  AND schemaContext
      AND visibility  AND validity                             <- constraints AND
```

Look at any retriever in `discover.sql` and the shape is the same: **one shared
`WHERE`** carrying visibility, validity, `schemaContext`, the attribute filter
and the whole spatial predicate, and then a per-mode `ORDER BY`. So every
constraint is ANDed inside every mode — adding a geometry to a filtered intent
narrows it, it never widens it. Case 12 is the sharp end, and it earns that
by a **leave-one-out triangle**:

| | text | geo | filter | answer |
|---|---|---|---|---|
| case 10 | ✓ | ✓ | — | point |
| case 11 | — | ✓ | ✓ | village |
| case 14 | ✓ | — | ✓ | alert |
| **case 12** | ✓ | ✓ | ✓ | **empty** |

`cotton cyclone` reaches point+alert, the 25 km circle reaches point+village,
the FREE-TIER offer reaches village+alert. Every *pair* overlaps in exactly one
resource and all three share none — so removing **any** dimension changes the
answer to a different single resource. That is what says all three are applied
and ANDed, and it is a real improvement on the first version of this case,
which came out empty whichever of two dimensions you dropped and so only ever
proved that one of them mattered.

Case 15 is the same three dimensions with a text term reaching all three
resources, so the answer is non-empty. There the geometry and the filter are
each load-bearing and the text is not — with three resources and only two of
them geo-indexed, a non-empty three-way answer cannot make all three matter at
once, which is exactly why case 12 is the one that carries the proof.

The modes themselves are the opposite. `LexicalCandidates` gates on
`search_tsv @@ discover_tsquery(...)`; `FuzzyCandidates` gates on
`name % query_text` — trigram similarity against the resource **name**, not
against the searchable text. They are separate retrievers fused by RRF, so a
resource either one admits is in the answer. Case 13 is a query whose every
term is misspelled: no tsvector matches it, and the village resource comes back
anyway at similarity 0.56.

That distinction is easy to miss, and it was missed here. `audit.py`'s oracle
originally modelled the lexical mode alone and agreed with the service on all
eight single-dimension cases — not because it was right, but because no fixture
query happened to trip the fuzzy mode without also tripping the lexical one.
Case 13 is what exposed it. The oracle now reimplements pg_trgm (validated
against PostgreSQL's own `similarity()` over 24 pairs, exact to 1e-6) and
matches when **either** mode does.

The threshold is `pg_trgm.similarity_threshold`, which nothing in this service
sets — so it is PostgreSQL's 0.3 default, and a deployment that moves it moves
what the fuzzy mode admits.

## `schemaContext` is a context field

It goes in `context`, not in `message.intent`. `Intent` is
`additionalProperties: false` in the v2.0.0 schema, so a sender who puts it
under the intent produces a body that fails its own schema. The reference Java
implementation moves it; the schema wins.

## `X-Beckn-Degraded: semantic` is expected

The embedding provider defaults to `noop`, so semantic retrieval is genuinely
absent. Rather than fail, the service answers with the modes it does have and
names the missing one in a response header — `on_discover` declares
`additionalProperties: false` with `catalogs` as its only property, so the
degradation cannot travel in the body without producing an invalid response
(C11).

## Regenerating the Postman collection

The collection is generated so it cannot drift from the files `verify.sh` runs:

```
python3 examples/build-postman.py
```

Edit the `.json` requests or the expectations in `build-postman.py`, then
regenerate. Do not hand-edit the collection.
