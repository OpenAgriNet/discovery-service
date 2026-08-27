# Do the four v1 use cases fit the OAN DPG?

**Yes — all four. None needs a new schema pack, and none needs a registry change.**
But three of the four emit a `select` response that the pack **rejects** today, and the two
marked green are not equally green.

Judged against `docs/network-schema/OpenAgriNet domain schemas.docx` — the authoritative
OpenAPI 3.1 / JSON Schema contracts, v0.1 *review draft*, `schema-packs-v0.1`. Each payload
below is the one [usecases.md](usecases.md) actually emits at ⑥, run through the pack.

| | Use case | Registry fits? | Pack conformance |
|---|---|---|---|
| 1–2 | **Weather** 🟢 | yes | **conforms** — 0 violations |
| 3 | **Mandi prices** 🟢 | yes | **3 violations** |
| 4 | **Schemes** | yes | **6 violations** |
| 5 | **Crop & pest** | yes | **6 violations** |

The registry column is `yes` four times over: every violation is in a **`responseMapping`**
or a **capability code**, not in the schema that stores the call plan. Nothing here asks for
a new registry field.

---

## 1. Weather — conforms

`observationType`, `source`, `location`, `generatedAt`, `parameters` all present; each
`parameters` item carries `parameter`, `value`, `unit`; `Rainfall` and `Temperature` are both
in the governed enum. The 🟢 is earned.

One caveat, unchanged: `aggregation` (`Total`/`Minimum`/`Maximum`) is **not a pack field**.
The `parameters` item is not closed, so it validates and means nothing to any other
participant — *tomorrow's high is 30.6, low 22.1* still has no conformant expression. This is
a gap in the network vocabulary, not in our mapping.

## 2. Mandi prices — 3 violations, and a name that does not exist

```
(root)   'arrivalDate' is a required property
market   'marketName' is a required property
prices   is not of type 'object'
```

`prices` is the substantive one: we emit an **array** of `{priceType, unit, value}`; the pack
declares an **object** `{minimum, maximum, modal, currency, unit}` with `modal`, `currency`
and `unit` required. Note `currency` — ISO 4217, `^[A-Z]{3}$`. We emit no currency at all;
`INR/QUINTAL` in `unit` is one string doing two jobs.

The correct shape:

```json
"prices": { "minimum": 4200, "maximum": 4850, "modal": 4600,
            "currency": "INR", "unit": "QUINTAL" },
"arrivalDate": "2026-08-26",
"market": { "marketName": "Gondal", "district": "Rajkot", "state": "Gujarat" }
```

Also: `market.location` and `commodity` belong where the pack puts them — `location` inside
`market` (we emit it at the root), and `variety` as a **root** field, not inside `commodity`
(which is a Beckn `Descriptor`).

**And the type name is wrong.** The pack is `MandiPriceObservation`; we use
`openagrinet:MandiPrice`, which is not a pack. The docx is itself inconsistent here — the
pack definition says `MandiPriceObservation`, the *Information Modes* examples say
`MandiPrice` — so this needs one ruling from the network owners. Every mandi record and
filter we hold carries whichever loses.

## 3 & 4. Schemes and Crop & pest — same 6 violations

Both Advisory use cases emit the same shape, and both fail the same way:

```
(root)          'languages'       is a required property
(root)          'version'         is a required property
(root)          'lifecycleStatus' is a required property
(root)          'content'         is a required property
(root)          'provenance'      is a required property
knowledgeType   'SchemeInformation' is not one of
                ['Article','FAQ','Guide','Dataset','TrainingMaterial','Reference']
```

We are emitting **private synonyms of governed fields** and omitting the governed ones:

| we emit | the pack wants |
|---|---|
| `title`, `summary` | ungoverned — put the text in `content[].inlineContent` |
| `url` | `content[0].contentUri` **+** `mediaType` |
| `language` (singular) | `languages` (array, BCP 47) — required |
| `publisher` | `provenance.source.sourceName` |
| `source` at root | `provenance.source` |
| `generatedAt` | `provenance.publishedAt` |
| `knowledgeType: SchemeInformation` | one of six values — `Reference` or `Guide` |
| — | `version` (`^\d+\.\d+\.\d+$`), `lifecycleStatus` |

The pack is **open at the top level**, so `title`/`summary`/`url`/`publisher` validate while
meaning nothing to another participant. `content[]` items *are* closed — so the fields cannot
simply be moved inside one.

**`knowledgeType` for an advisory is `Guide`.** The docx settles it: its own *Weather Advisory
Output* example publishes generated advice as a `KnowledgeResource` with
`knowledgeType: "Guide"`, a `validity` window and `provenance.reviewedBy`. Crop & pest
advisory follows that precedent exactly — no new pack, no `AdvisoryCapability` needed on the
outcome.

**The real cost is `version` and `lifecycleStatus`.** Neither the Hasura content table nor the
OAN vector index carries them. A mapping can emit `"1.0.0"` / `"Published"` as constants, but
those are then two governed fields asserting something nobody checked. The honest fix is two
columns upstream; the expedient fix is constants, and it should be written down as such.
`provenance.publishedAt` has the same problem for the vector index, which stores chunks, not
publication dates.

**The pack explicitly blesses `oan-vector`'s model**, so Crop & pest is not a stretch:

> A Provider that keeps its corpus private and answers live questions publishes a retrieval
> capability rather than cataloging every source document.

---

## Two findings that land on discovery-service, not the registry

**① Our `discover` filters may match a conformant catalog zero times.** The DPG defines a
governed capability vocabulary with an interaction and an outcome:

| capability type | interaction | outcome |
|---|---|---|
| `KnowledgeRetrievalCapability` | Observe | `KnowledgeResource` |
| `MandiPriceCapability` | Observe | `MandiPriceObservation` |
| `WeatherObservationCapability` | Observe | `WeatherObservation` |

and `AgricultureCapability` says *"a Provider catalog entry declares AgricultureCapability
together with one governed capability type."* So a conformant provider may advertise
`@type: WeatherObservationCapability` — while every filter in `usecases.md` matches on the
**outcome** type, `@type == "openagrinet:WeatherObservation"`. Both advertisement styles are
sanctioned (an `OnDemand` outcome resource is the other), so a discovery service must match
**either**, or providers become invisible to it. This is the highest-value finding here and it
is ours to fix.

**② `informationMode` is "Proposed terminology".** It appears in **no** pack schema — only in
the *Information Modes* section's examples. `registry.md`'s gap list has this backwards: it
records `informationMode` as "`required` on every published resource". Against these packs it
is neither required nor declared. It still validates (open packs), and the OnDemand/Direct
distinction is still the reason two hops exist — but nothing enforces it and no filter can
rely on it.

---

## What to change, in cost order

| | change | where |
|---|---|---|
| 1 | `prices` array → object, add `arrivalDate`, `currency`, `market.marketName` | `mappings/agmarknet/select.response.jsonata` |
| 2 | Advisory: emit `content[]`, `provenance`, `languages`, `knowledgeType: Guide`/`Reference` | `mappings/{hasura-content,oan-vector}/select.response.jsonata` |
| 3 | Rule on `MandiPrice` vs `MandiPriceObservation`, then rename in 1 `Capability`, 1 binding, every filter | network owners, then registry |
| 4 | Match **capability** types as well as outcome types in `/discover` | discovery-service |
| 5 | Source `version` + `lifecycleStatus`, or record the constants as a known fiction | Hasura table, vector index |
| 6 | `aggregation` — take the min/max gap to the network | network-specs |

Not blocking: the registry schema itself. It stores the call plan for all four use cases,
and for five hypothetical newcomers, unchanged — see [registry.md §3](registry.md#3-the-schemas).
