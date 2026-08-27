# Use case execution

One farmer's question, traced from the experience layer to IMD and back, with the real
payloads at every hop. The architecture this follows is in
[registry.md §1](registry.md#1-architecture); the records it resolves against are in
[examples.md](examples.md).

### ① Resolve meaning

*"पुढच्या पाच दिवसांत पाऊस पडेल का?"* — will it rain in the next five days? Device
location Nashik, `[73.7898, 19.9975]`. The experience layer turns that into an intent.

### ② `discover` — who can answer

```json
{
  "context": { "version": "2.0.0", "action": "discover",
    "transactionId": "9f2c1a8e-4b70-4d31-9c55-6f2e0b1d7a44",
    "messageId": "1c0a55d7-8e64-4b19-9a2f-33b7c6e1d905" },
  "message": { "intent": {
    "textSearch": "weather forecast rain next five days",
    "filters": {
      "type": "jsonpath",
      "expression": "$.catalogs[*] ? (@.resourceAttributes.\"@type\" == \"openagrinet:WeatherObservation\")"
    },
    "spatial": [{
      "op": "S_DWITHIN",
      "targets": "$['provider']['availableAt'][*]['geo']",
      "geometry": { "type": "Point", "coordinates": [73.7898, 19.9975] },
      "distanceMeters": 250000, "quantifier": "ANY"
    }]
  } }
}
```

**Write filters in PostgreSQL SQL/JSON path.** `beckn.yaml` says RFC 9535 and its own
example uses the Goessner form, but a discovery service on Postgres executes the
expression with `@?`, which takes a different grammar. `filters.expression` is declared
`type: string`, so a wrong dialect is schema-valid and fails only at query time.

| | RFC 9535 (the spec's example) | SQL/JSON path (what runs) |
|---|---|---|
| filter | `$.catalogs[?(...)]` | `$.catalogs[*] ? (...)` |
| quoting | `['@type']`, `'literal'` | `."@type"`, `"literal"` — double quotes |
| membership | `anyof [a, b]` | no such operator — expand to `a \|\| b` |

Root at `$.catalogs`. A filter starting `$[?(...)]` addresses the wrong node and matches
nothing — and nothing is indistinguishable from an honest empty result.

**The four v1 queries.** All verified against a live PostgreSQL and all GIN-indexable:

| Category | expression |
|---|---|
| Weather | `$.catalogs[*] ? (@.resourceAttributes."@type" == "openagrinet:WeatherObservation")` |
| Mandi | `$.catalogs[*] ? (@.resourceAttributes."@type" == "openagrinet:MandiPrice")` |
| Schemes | `$.catalogs[*] ? (@.resourceAttributes."@type" == "openagrinet:KnowledgeResource" && @.resourceAttributes.subjectCategories[*] == "Scheme")` |
| Crop & Pest | `$.catalogs[*] ? (@.resourceAttributes."@type" == "openagrinet:KnowledgeResource" && @.resourceAttributes.subjectCategories[*] == "Crop")` |

**None of the four constrain `informationMode`, deliberately.** Both modes are publishable
— an `OnDemand` advertisement and a `Direct` resource published straight to discovery are
both honest answers to *who can tell me about rain*. Filtering to `OnDemand` would hide
data already in the catalog; filtering to `Direct` would hide every provider that answers
on invocation. Add the constraint only when the caller genuinely wants one or the other,
and note that the experience layer must branch on it either way, because `OnDemand` means
a second call and `Direct` means it already has the answer.

`subjectCategories` is a **required** enum on the shared `AgricultureResource` field set —
`Crop` `Livestock` `Weather` `Market` `Scheme` `Knowledge`. It is the only thing that
separates the two Advisory categories, because both are `KnowledgeResource`. Narrow Crop &
Pest further with `agricultureSubjects[].subjectType` ∈ `{Pest, Disease}`.

Two things `Intent` cannot carry, and this is exactly why `select` exists: `Intent` is
`additionalProperties: false` over `{textSearch, filters, spatial, mediaSearch}`, so the
location has to be smuggled in as a spatial constraint on the *provider's service area*,
and there is no field for a validity window. The DS still filters validity server-side on
every retrieval mode — the caller just cannot ask for one.

### ③ `select` — name that provider, ask for the data

`resourceAttributes` is an open container, so it carries what `intent` could not:

```json
{
  "context": { "version": "2.0.0", "action": "select",
    "transactionId": "9f2c1a8e-4b70-4d31-9c55-6f2e0b1d7a44" },
  "message": { "contract": { "commitments": [{
    "status": { "descriptor": { "code": "DRAFT", "name": "Draft" } },
    "resources": [{
      "id": "res:mausamgram:point-forecast",
      "quantity": 1,
      "resourceAttributes": {
        "@context": "https://schemas.openagrinet.global/schema/WeatherObservation/v0.1/context.jsonld",
        "@type": "openagrinet:WeatherObservation",
        "location": { "type": "Point", "coordinates": [73.7898, 19.9975] },
        "validity": { "startsAt": "2026-08-26", "endsAt": "2026-08-30" }
      }
    }],
    "offer": {
      "id": "offer:mausamgram:open-data",
      "resourceIds": ["res:mausamgram:point-forecast"],
      "provider": { "id": "mausamgram",
                    "descriptor": { "code": "IMD-NWP-01", "name": "IMD Mausamgram NWP" } }
    }
  }] } }
}
```

`status: DRAFT`, no price. Nothing is being committed — for an open-data provider the
quote is zero-cost and the payload *is* the data.

`@context` and `@type` are the **same across all three envelopes** — advertisement, request
and result all name the `WeatherObservation` pack. Only `informationMode` moves, and only
between the advertisement and the result. If you find a trace where the *context* changes
between hops, it predates the pack index and is describing the capability-schema model
that was dropped.

**The request's `resourceAttributes` is not a pack instance, and is not validated as one.**
`INDEX.md` declares each pack's placement as *Provider catalog, Discovery result, or
Provider response*; a `select` request is none of those. Beckn types the field as
`Attributes` — an open container requiring only `@context` and `@type` — so what travels
here is the resource's identity plus the parameters of the invocation, not a restatement
of the advertisement. Hold a request against the pack and it fails on `informationMode`
and on whichever half of the contract you did not echo, which says nothing about the
request.

### ④ Resolve — build the key, read the plan

Everything needed is already on the request body:

```
offer.provider.id            → "mausamgram"
resourceAttributes["@type"]  → "openagrinet:WeatherObservation"
action                       → "select"

bindingKey = "mausamgram|openagrinet:WeatherObservation|select"
```

The `@type` is the **same string the advertisement carried** — one type spans both calls,
which is what makes the key derivable rather than looked up.

The adapter resolves this from its in-memory index rather than calling the registry per
request — see [registry.md §4](registry.md#4-registry-apis) for the reads, the boot load, and what to confirm
against RC v2.0.0 on first boot.

### ⑤ Enrich, map, authenticate, call

**Enrich** — the binding names a plugin, the registry does not implement it:

```
pointFromIntent:  resourceAttributes.location.coordinates → _local = {lat: 19.9975, lon: 73.7898}
```

**Map** — `requestMapping` runs over `{beckn, _local}`. **Authenticate** — resolve each
`env://` pointer and apply per `scheme`. **Call**:

```
GET https://mausamgram.imd.gov.in/nwpapi/get-daily?lat=19.9975&lon=73.7898
Authorization: Basic <resolved>
timeout 30000ms   retries 3
```

Timeout and retry are registry columns, not constants in a service class.

Provider answers in its native shape:

```json
{ "location": { "lat": 19.9975, "lon": 73.7898 },
  "fcstday1": { "date": "2026-08-26", "rain": 12.4, "tmin": 22.1, "tmax": 30.6,
                "wspd": 4.2, "weather_warning": "Heavy rainfall warning" } }
```

### ⑥ Map the response, send it back

`responseMapping` runs over `{request, response, _local}` — `_local` stays in scope so the
resolved point reaches the output. Five forecast days become five typed Beckn resources:

```json
{
  "id": "res:mausamgram:forecast:2026-08-26",
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/WeatherObservation/v0.1/context.jsonld",
    "@type": "openagrinet:WeatherObservation",
    "informationMode": "Direct",
    "observationType": "Forecast",
    "subjectCategories": ["Weather"],
    "source": { "sourceId": "mausamgram", "sourceName": "IMD Mausamgram NWP" },
    "location": { "type": "Point", "coordinates": [73.7898, 19.9975] },
    "validity": { "startsAt": "2026-08-26T00:00:00Z", "endsAt": "2026-08-26T23:59:59Z" },
    "generatedAt": "2026-08-26T06:12:04.201Z",
    "parameters": [
      { "parameter": "Rainfall",    "aggregation": "Total",   "unit": "mm",  "value": 12.4 },
      { "parameter": "Temperature", "aggregation": "Minimum", "unit": "Cel", "value": 22.1 },
      { "parameter": "Temperature", "aggregation": "Maximum", "unit": "Cel", "value": 30.6 }
    ]
  }
}
```

`@type` is a **single string**. Beckn core declares it `type: string`; the two-element
array form some examples show fails validation.

`aggregation` is **not a governed field** — it is not in the pack. It validates only
because the `parameters` item declares `required: [parameter, value, unit]` without
closing the object, so a private qualifier passes and means nothing to any other
participant. There is no conformant way to say *tomorrow's high is 30.6 and low is 22.1*,
and every Indian weather upstream reports `tmin`/`tmax`. Real gap, tracked as issue 3 of
[Open issues](imported/reference/open-issues.md), and it lands on Weather — a v1 category.


