# Examples

A worked publish and the discover requests that find it, against a local stack.

```
make run                # PostgreSQL + the service on :8080
make verify             # publish, then assert every retrieval path
make newman             # the same checks through the Postman collection
```

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

## The requests

| File | Exercises | Returns |
|---|---|---|
| `02-discover-text-search.json` | lexical retrieval | village + point, **not** the alert |
| `03-discover-schema-context.json` | `context.schemaContext` | all three |
| `04-discover-spatial-dwithin.json` | `S_DWITHIN`, 25 km of Dharwad | point + village |
| `05-discover-spatial-intersects.json` | `S_INTERSECTS`, inside Belagavi | village alone |
| `06-discover-filter-granularity.json` | jsonpath, resource-rooted | village alone |
| `07-discover-filter-cross-level.json` | jsonpath, **offer**-rooted | point alone |
| `08-discover-invalid-jsonpath.json` | the form gate | **400** |

Three of these are worth the extra sentence.

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
