# Use cases

Six farmer questions. One is shown in full; the other five differ only in the call plan.

| # | question | capability | provider |
|---|---|---|---|
| 1 | will it rain in the next five days, here? | `openagrinet:WeatherObservation` | `mausamgram` |
| 2 | …and for my city? | `openagrinet:WeatherObservation` | `imd-city-weather` |
| 3 | what is today's mandi price? | `openagrinet:MandiPrice` | `agmarknet` |
| 4 | which scheme am I eligible for? | `openagrinet:KnowledgeResource` | `hasura-content` |
| 5 | what is eating my crop? | `openagrinet:KnowledgeResource` | `oan-vector` |
| 6 | should I spray this week? | *weather advisory* | **not seeded** |

## Both hops are synchronous — the spec has not caught up

BV executes every transaction synchronously. `/discover` returns the catalogs and `/select`
returns the data, each on the connection the caller is holding open. `on_discover` and
`on_select` are response **bodies** here, not inbound calls.

`beckn.yaml` v2.0.0 does not describe that. `/discover` and `/select` declare `200` → `Ack`,
whose own description reads:

> The implementer has authenticated the request, accepted it for processing, and **MUST deliver
> the business outcome asynchronously via the corresponding callback endpoint.**

`202` → `AckNoCallback`, whose body is an `Error` explaining why no callback will follow — not a
payload either. Of the 27 paths in the document exactly two are synchronous,
`/catalog/subscription` and `/catalog/search`, and both are Cataloging Service fabric endpoints
rather than the CN-facing transaction path. `context.try` is not an escape hatch: it is
`update`/`cancel` only, and receiving actors "MUST ignore it if present" everywhere else.

So today this is a deviation on a MUST — but it is a **decided** one, not an accident. Sync is the
transport for every transaction here, and `beckn.yaml` is to be amended to describe it; until that
lands, a reader comparing this page against the schema will find the schema says otherwise, and the
schema is the thing that is behind. Four consequences hold either way, and are worth writing down
rather than discovering:

| | |
|---|---|
| **Config** | `targetType: "url"` on every route. `bppTxnCaller` and `bapTxnReceiver` never fire — half the ONIX config is dormant by design, not misconfigured. |
| **`bapUri` / `bppUri` still matter** | They are how the counterparty is *resolved* — `context.networkId` names the subnet registry, which returns `subscriber_url` and key material. Nothing is delivered to them. |
| **The retry budget becomes the farmer's wait** | `mausamgram`'s `select` entry is seeded `timeoutMs: 30000, retryMax: 3` — up to **120 s** on an open connection. Async hides retries; sync bills them to the farmer. Whether to cap the sync path at one attempt is open. |
| **Interop** | Until the amendment lands, a CN built to the published schema waits for a callback that never arrives. Sync holds while both ends are BV-operated; a third party calling in needs the amended spec, not a note in this folder. |

Errors are NACK-only on the open connection: no partial results, no silent empties.

`discover` is answered by the **network node** from the published catalog — no fan-out to
providers. `select` goes from the consumer node **straight to the provider node**, using the
`bppUri` that `on_discover` returned; the network node is not in that path.

## The three adapters, in Bharat Vistaar

| adapter | `participantId` | role |
|---|---|---|
| consumer node | `seeker-network-vistaar.da.gov.in` | BAP — signs `discover` and `select` |
| network node | `discovery-network-vistaar.da.gov.in` | NETWORK — **proposed**; BV has no third subscriber today |
| provider node | `provider-network-vistaar.da.gov.in` | BPP — verifies, resolves, calls IMD |

`networkId` is `da.gov.in/vistaar`, the spec's RECOMMENDED `namespace_id/registry_id` shape. BV
production today is **Beckn v1.1.0 with `search`**, domain `schemes:vistaar`; v2.0.0 has no
`domain` field at all, and a single `search` has nowhere to carry the point and validity window
that `select` carries below. Moving BV to v2.0.0 is a network-owner decision, not a service one.

| hop | signed by | verified by |
|---|---|---|
| `discover` | consumer node | network node |
| `on_discover` — the 200 body | network node | consumer node |
| `select` | consumer node | provider node |
| `on_select` — the 200 body | provider node | consumer node |

Verification is ONIX's, never discovery-service's — `AUTH_ENABLE_SIGNATURE_VERIFICATION=false`,
and `true` refuses to boot. That is precisely why discovery-service has no route from outside:
`context.bapId` is trustworthy only downstream of the verifier.

---

## 1. Weather forecast for a point — `mausamgram`

*"पुढच्या पाच दिवसांत पाऊस पडेल का?"* Device location Nashik, `[73.7898, 19.9975]`.

Records: `SchemaRegistry` for `openagrinet:WeatherObservation`, `Participant` `mausamgram`,
binding `mausamgram|openagrinet:WeatherObservation`, plus the three nodes — all in
[examples.md](examples.md).

**The capability is `WeatherObservation`, not `WeatherAdvisory`.** A forecast carrying values is
an observation with `observationType: "Forecast"`. `WeatherAdvisory` is a real pack — it is what
use case 6 needs — and nothing seeds it; it appears below only as a second `@type` the filter
will also match.

### 1.0 The experience layer resolves meaning

Before any Beckn call, the utterance becomes concepts:

| from | concept |
|---|---|
| "पाऊस" | subject area `Weather`, parameter `Rainfall` |
| device GPS | point `[73.7898, 19.9975]` |
| "पुढच्या पाच दिवसांत" | validity `2026-08-26 .. 2026-08-30` |

This is the boundary the design rests on: **the experience layer owns meaning, the adapter owns
encoding.** The experience layer does not know that IMD calls this `fcstday1..fcstday5`, and it
must not. It hands the consumer node a plain HTTP request; the consumer node signs it.

### 1.1 `discover` — and its answer, on the same connection

```json
POST /discover
{
  "context": { "version": "2.0.0", "action": "discover",
    "networkId": "da.gov.in/vistaar",
    "bapId": "seeker-network-vistaar.da.gov.in",
    "bapUri": "https://seeker-network-vistaar.da.gov.in/beckn",
    "transactionId": "9f2c1a8e-4b70-4d31-9c55-6f2e0b1d7a44",
    "messageId": "1c0a55d7-8e64-4b19-9a2f-33b7c6e1d905",
    "timestamp": "2026-08-26T06:11:58.004Z" },
  "message": { "intent": {
    "textSearch": "weather forecast rain next five days",
    "filters": {
      "type": "jsonpath",
      "expression": "$.catalogs[*].resources[*] ? (@.resourceAttributes.\"@type\" == \"openagrinet:WeatherObservation\" || @.resourceAttributes.\"@type\" == \"openagrinet:WeatherAdvisory\")"
    },
    "spatial": [{
      "op": "S_DWITHIN",
      "targets": "$['catalogs'][*]['provider']['availableAt'][*]['geo']",
      "geometry": { "type": "Point", "coordinates": [73.7898, 19.9975] },
      "distanceMeters": 250000, "quantifier": "ANY"
    }]
  } }
}
```

`Authorization` carries the consumer node's signature; the network node resolves
`seeker-network-vistaar.da.gov.in` against the subnet registry named by `networkId` to get the
key that verifies it. Only then is `bapId` worth reading.

### 1.2 The `200` — `on_discover` as a body

Sync, so this **is** the response to the call above, not a fresh POST to `bapUri`. `context` is
merged rather than rebuilt, so `transactionId`, `messageId` and `timestamp` survive:

```jsonata
"context": request.context ~> |$|{ "action": "on_discover" }|
```

`Catalog` requires `id`, `descriptor` and `provider`, and allows nothing else. Two providers
match; which to pick is the app's call, not the registry's.

```json
200 OK
{
  "context": { "version": "2.0.0", "action": "on_discover",
    "networkId": "da.gov.in/vistaar",
    "bapId": "seeker-network-vistaar.da.gov.in",
    "bapUri": "https://seeker-network-vistaar.da.gov.in/beckn",
    "bppId": "discovery-network-vistaar.da.gov.in",
    "bppUri": "https://discovery-network-vistaar.da.gov.in/beckn",
    "transactionId": "9f2c1a8e-4b70-4d31-9c55-6f2e0b1d7a44",
    "messageId": "1c0a55d7-8e64-4b19-9a2f-33b7c6e1d905",
    "timestamp": "2026-08-26T06:11:58.612Z" },
  "message": { "catalogs": [
    {
      "id": "cat:mausamgram:weather",
      "descriptor": { "code": "IMD-NWP-01", "name": "IMD Mausamgram NWP" },
      "provider": {
        "id": "mausamgram",
        "descriptor": { "code": "IMD-NWP-01", "name": "IMD Mausamgram NWP" },
        "availableAt": [{ "geo": { "type": "Polygon", "coordinates": [[[68.1,8.0],[97.4,8.0],[97.4,37.1],[68.1,37.1],[68.1,8.0]]] } }]
      },
      "resources": [{
        "id": "res:mausamgram:point-forecast",
        "descriptor": { "name": "Five-day point forecast" },
        "resourceAttributes": {
          "@context": "https://schemas.openagrinet.global/schema/WeatherObservation/v0.1/context.jsonld",
          "@type": "openagrinet:WeatherObservation",
          "informationMode": "OnDemand",
          "supportedObservationTypes": ["Forecast"],
          "supportedParameters": ["Rainfall", "Temperature", "Humidity", "WindSpeed", "WindDirection"],
          "geographicGranularities": ["Point"],
          "forecastHorizon": "P5D",
          "updateFrequency": "PT12H",
          "subjectCategories": ["Weather"]
        }
      }],
      "offers": [{ "id": "offer:mausamgram:open-data",
                   "resourceIds": ["res:mausamgram:point-forecast"] }]
    },
    {
      "id": "cat:imd-city-weather:weather",
      "descriptor": { "code": "IMD-CITY-01", "name": "IMD City Weather" },
      "provider": {
        "id": "imd-city-weather",
        "descriptor": { "code": "IMD-CITY-01", "name": "IMD City Weather" },
        "availableAt": [{ "geo": { "type": "Polygon", "coordinates": [[[68.1,8.0],[97.4,8.0],[97.4,37.1],[68.1,37.1],[68.1,8.0]]] } }]
      },
      "resources": [{
        "id": "res:imd-city-weather:station-forecast",
        "descriptor": { "name": "Station forecast" },
        "resourceAttributes": {
          "@context": "https://schemas.openagrinet.global/schema/WeatherObservation/v0.1/context.jsonld",
          "@type": "openagrinet:WeatherObservation",
          "informationMode": "OnDemand",
          "supportedObservationTypes": ["Forecast"],
          "supportedParameters": ["Rainfall", "Temperature", "Humidity"],
          "geographicGranularities": ["District"],
          "forecastHorizon": "P7D",
          "updateFrequency": "PT24H",
          "subjectCategories": ["Weather"]
        }
      }],
      "offers": [{ "id": "offer:imd-city-weather:open-data",
                   "resourceIds": ["res:imd-city-weather:station-forecast"] }]
    }
  ] }
}
```

`informationMode: OnDemand` and no values — that is the difference between the hops, and the
pack enforces it: `OnDemand` **requires** `supportedObservationTypes`, `supportedParameters` and
`geographicGranularities`, and carries `not: {required: [parameters]}`. An advertisement that
leaked a value would be rejected. `observationType` is the outcome field and does not belong
here; the advertisement says what forms it *can* return.

### 1.3 `select` — consumer node straight to the provider node

The **consumer node** builds this, not the network node, and posts it to the `bppUri` that
`on_discover` returned. The network node has no part in this hop.

`DRAFT` and no price: nothing is being committed. For an open-data provider the quote is
zero-cost and the payload *is* the data. New `messageId`, same `transactionId`.

```json
POST https://provider-network-vistaar.da.gov.in/beckn/select
{
  "context": { "version": "2.0.0", "action": "select",
    "networkId": "da.gov.in/vistaar",
    "bapId": "seeker-network-vistaar.da.gov.in",
    "bapUri": "https://seeker-network-vistaar.da.gov.in/beckn",
    "bppId": "provider-network-vistaar.da.gov.in",
    "bppUri": "https://provider-network-vistaar.da.gov.in/beckn",
    "transactionId": "9f2c1a8e-4b70-4d31-9c55-6f2e0b1d7a44",
    "messageId": "7d41b9e0-52a6-4c18-8b73-1e9f0a4c6d22",
    "timestamp": "2026-08-26T06:12:01.330Z" },
  "message": { "contract": { "commitments": [{
    "status": { "descriptor": { "code": "DRAFT", "name": "Draft" } },
    "resources": [{
      "id": "res:mausamgram:point-forecast",
      "resourceAttributes": {
        "@context": "https://schemas.openagrinet.global/schema/WeatherObservation/v0.1/context.jsonld",
        "@type": "openagrinet:WeatherObservation",
        "subjectCategories": ["Weather"],
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

### 1.4 Resolve — the provider node reads the registry, twice

Two registries, and they are not the same store. The provider node has already read the **subnet
registry** to verify the signature — `networkId` → registry URL → lookup on `bapId` → key
material. What follows is the **capability registry**, these three tables, and it is read on the
**provider side only**. If the consumer side resolved capabilities it would need the upstream
credentials, which is the whole thing this split exists to prevent: the consumer must never learn
that `mausamgram` means `https://mausamgram.imd.gov.in`.

```
offer.provider.id            → "mausamgram"
resourceAttributes["@type"]  → "openagrinet:WeatherObservation"
bindingKey                   = "mausamgram|openagrinet:WeatherObservation"
context.action               → "select"                    → actions[] entry
```

**Read 1** — the call plan: `ProviderSchema` by `bindingKey`, then the `actions[]` entry whose
`action` is `select`, giving `GET /nwpapi/get-daily`, `timeoutMs: 30000`, `retryMax: 3`. Those are
per-action registry fields, not service constants — IMD is slow, and an operator changes this
without a deploy. An entry whose own `status` is `inactive` is not a match: the capability can be
live while one of its actions is not.

**Read 2** — where it is and how to authenticate: `Participant` by the `participantId` **from row
1, never from the request**. `baseUrl` is the host and the entry's `path` is appended to it, so the
credential can only ever reach the host its own record names. A request that could name the participant could point a
credentialled call at a host of its choosing. That row is `type: "upstream"`, so it carries a
`baseUrl` and an `auth`; a binding naming a node instead would resolve to a call that cannot be
made, which is why `verify/records.py` refuses one at seeding time.

An empty result from either read is a hard failure — `BIZ_PROVIDER_NOT_FOUND`, not a fallback.

There is **no `SchemaRegistry` read**: a capability is vocabulary, not part of the call path. And
in production neither read is a request at all
([api.md](api.md#the-runtime-does-not-call-these-per-request)).

### 1.5 Enrich, map the request, authenticate, call

**Enrich first.** The binding names `enricher: { "name": "pointFromIntent" }` — declared in the
registry, implemented in Go — and it runs **before** the request mapping, because the mapping is
evaluated over `{ request, _local }` and `_local` is what the enricher produces:

```
resourceAttributes.location.coordinates  →  _local = { "lat": 19.9975, "lon": 73.7898 }
```

Trivial here, because mausamgram takes a raw point. **It is not trivial in general, and this use
case is the wrong place to look for the interesting case.** A station resolver is use case 2
(`imd-city-weather` wants a station id); market and commodity codes are use case 3 — both PostGIS
lookups off the point, both carrying `enricher.secrets.dsn`. The registry holds the **name**; Go
holds the behaviour. Config that tried to hold the behaviour would become a programming language,
and nothing here checks that the name resolves to an implementation.

**Then map.** The `request:` half of `mappings/mausamgram/weather-observation.select.yaml`, over
`{ request, _local }`:

```jsonata
{ "lat": $string(_local.lat), "lon": $string(_local.lon) }
```

**GeoJSON is `[longitude, latitude]`** and mausamgram wants `lat`/`lon`, so the mapping swaps
them — getting it backwards returns a forecast for the Arabian Sea with no error. `$string()` is
not cosmetic either; the endpoint rejects unquoted numerics.

```
GET https://mausamgram.imd.gov.in/nwpapi/get-daily?lat=19.9975&lon=73.7898
Authorization: Basic <resolved from env://MAUSAMGRAM_USER + env://MAUSAMGRAM_X_API_KEY>
timeout 30000ms   retries 3
```

Captured response, two of its five day-blocks, values re-pointed to Nashik:

```json
{
  "lat_r": 19.875,
  "lon_r": 73.875,
  "fcstday1": {
    "date": "2026-08-26", "rain": 0.84, "tmax": 39.24, "tmin": 32.8,
    "wdir": 273.39, "wind": ["W", "Westerly"], "wspd": 2.83, "cloud": 74.44,
    "rhmax": 58.67, "rhmin": 40.81, "tmax_raw": 37.13, "tmin_raw": 32.92,
    "rain_message": "Light Rain", "cloud_message": "Generraly Cloudy Sky",
    "weather_warning": "Generally Cloudy Sky"
  },
  "fcstday2": {
    "date": "2026-08-27", "rain": 0.84, "tmax": 40.04, "tmin": 31.5,
    "wdir": 265.61, "wind": ["W", "Westerly"], "wspd": 3.27, "cloud": 29,
    "rhmax": 58.23, "rhmin": 37.86, "tmax_raw": 38.3, "tmin_raw": 31.63,
    "rain_message": "Light Rain", "cloud_message": "Mainly Clear Sky",
    "weather_warning": "Partly Cloudy Sky"
  },
  "location": { "lat": 19.9975, "lon": 73.7898 },
  "abbreviation": {
    "rain": "Rainfall (mm)",
    "tmax": "Maximum Temperatue (Celsius) - Real Time Bias Corrected",
    "tmin": "Minimum Temperatue (Celsius) - Real Time Bias Corrected",
    "wdir": "Wind Direction (degree)", "wspd": "Wind Speed (m/s)",
    "cloud": "Total Cloud Cover (%)",
    "rhmax": "Maximum Relative Humidity (%)", "rhmin": "Minimum Relative Humidity (%)"
  }
}
```

- **`lat_r`/`lon_r` is not `location`.** `lat_r`/`lon_r` is the model grid point the forecast was
  computed at; `location` echoes what was asked for. They differ by about 9 km here. This
  walkthrough publishes the **requested** point — a decision, not a fact.
- **`abbreviation` is the only source of units.** The pack requires a `unit` on every parameter
  and the values carry none. Read them from here; don't guess.
- **`tmax` is bias-corrected, `tmax_raw` is not.** Use `tmax`.
- **`cloud_message` is misspelled upstream** (`"Generraly"`). Match on numbers, not text.

### 1.6 The `200` — `on_select` as a body

The same file's `response:` half runs over `{ request, response, _local }`; `_local` stays in scope so the
resolved point reaches the output. Then the provider node signs the result and returns it on the
still-open connection — no POST to `bapUri`.

`on_select` requires a `contract`, so resources travel inside a commitment. Two of five days:

```json
200 OK
{
  "context": { "version": "2.0.0", "action": "on_select",
    "networkId": "da.gov.in/vistaar",
    "bapId": "seeker-network-vistaar.da.gov.in",
    "bapUri": "https://seeker-network-vistaar.da.gov.in/beckn",
    "bppId": "provider-network-vistaar.da.gov.in",
    "bppUri": "https://provider-network-vistaar.da.gov.in/beckn",
    "transactionId": "9f2c1a8e-4b70-4d31-9c55-6f2e0b1d7a44",
    "messageId": "7d41b9e0-52a6-4c18-8b73-1e9f0a4c6d22",
    "timestamp": "2026-08-26T06:12:04.918Z" },
  "message": { "contract": { "commitments": [{
    "status": { "descriptor": { "code": "QUOTED", "name": "Quoted" } },
    "offer": {
      "id": "offer:mausamgram:open-data",
      "resourceIds": ["res:mausamgram:forecast:2026-08-26", "res:mausamgram:forecast:2026-08-27"],
      "provider": { "id": "mausamgram",
                    "descriptor": { "code": "IMD-NWP-01", "name": "IMD Mausamgram NWP" } }
    },
    "resources": [
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
            { "parameter": "Rainfall",      "aggregation": "Total",   "unit": "mm",  "value": 0.84 },
            { "parameter": "Temperature",   "aggregation": "Maximum", "unit": "Cel", "value": 39.24 },
            { "parameter": "Temperature",   "aggregation": "Minimum", "unit": "Cel", "value": 32.8 },
            { "parameter": "Humidity",      "aggregation": "Maximum", "unit": "%",   "value": 58.67 },
            { "parameter": "Humidity",      "aggregation": "Minimum", "unit": "%",   "value": 40.81 },
            { "parameter": "WindSpeed",     "aggregation": "Mean",    "unit": "m/s", "value": 2.83 },
            { "parameter": "WindDirection", "aggregation": "Mean",    "unit": "deg", "value": 273.39 }
          ]
        }
      },
      {
        "id": "res:mausamgram:forecast:2026-08-27",
        "resourceAttributes": {
          "@context": "https://schemas.openagrinet.global/schema/WeatherObservation/v0.1/context.jsonld",
          "@type": "openagrinet:WeatherObservation",
          "informationMode": "Direct",
          "observationType": "Forecast",
          "subjectCategories": ["Weather"],
          "source": { "sourceId": "mausamgram", "sourceName": "IMD Mausamgram NWP" },
          "location": { "type": "Point", "coordinates": [73.7898, 19.9975] },
          "validity": { "startsAt": "2026-08-27T00:00:00Z", "endsAt": "2026-08-27T23:59:59Z" },
          "generatedAt": "2026-08-26T06:12:04.201Z",
          "parameters": [
            { "parameter": "Rainfall",    "aggregation": "Total",   "unit": "mm",  "value": 0.84 },
            { "parameter": "Temperature", "aggregation": "Maximum", "unit": "Cel", "value": 40.04 },
            { "parameter": "Temperature", "aggregation": "Minimum", "unit": "Cel", "value": 31.5 }
          ]
        }
      }
    ]
  }] } }
}
```

Only `informationMode` changed between the hops — `OnDemand` → `Direct`. `@type` is a **single
string**; the two-element array form some examples show fails validation.

---

## The other five

Same two hops, same envelope. What differs is the call plan and the upstream's quirks.

| # | provider | call | the thing that bites |
|---|---|---|---|
| 2 | `imd-city-weather` | `GET /api/cityweather_loc.php?id=<station>` | Keyed by station id, which no Beckn field carries — the adapter owns the nearest-station table. Returns an **array**, `"NIL"` as a rainfall sentinel, `null`s, and every number as a **string**. Live path is not the documented `/citywx/city_weather_test.php`. |
| 3 | `agmarknet` | `GET /v1/fetch-agmarknet-vistaar-location` | Needs `lat`, `long`, `commodity_id`, one `date` — market and commodity codes in its own namespace. Returns **Title Case keys with spaces** (`"Max Price"`, `"Modal Price"`, `"Arrival Date"`, `"Price Unit": "Rs./Qtl"`); a mapping written against snake_case returns nothing at all, with no error. |
| 4 | `hasura-content` | `POST /v1/graphql` | GraphQL `variables` block built by the adapter. Illustrative payload — this provider appears in no captured workbook row. |
| 5 | `oan-vector` | `POST /indexes/oan-index/search` | Same capability as 4, different request shape: a vector search body, not a GraphQL one. Same query-parameter step, different request mapping. Illustrative payload. |
| 6 | *weather advisory* | — | **Not seeded**, but now seedable: a second capability on `mausamgram` is a second row, and `mappings/mausamgram/weather-advisory.select.yaml` no longer collides with the forecast's file. What is missing is the mapping and the pack reading, not a convention. |

## Conformance

| provider | violations |
|---|---|
| `mausamgram` | **0.** All required pack fields present, every `parameter` in the governed enum. Caveat: `aggregation` is ours, not the pack's — it validates because the parameter object is open, and means nothing to another participant, which leaves *"tomorrow's high is 39.24, low 32.8"* with no conformant expression. |
| `imd-city-weather` | **0.** |
| `agmarknet` | **3** — omits three required fields. |
| `hasura-content` | **6** — omits five required fields and uses `knowledgeType` values not in the enum. |
| `oan-vector` | **6** — the same six. |

These are response-mapping bugs, not registry ones, and seeding is unaffected. They were found by
reading; **nothing validates a mapping's output against the pack it claims to produce**, so the
counts above are a floor.

## Errors

| code | means | who fixes it |
|---|---|---|
| `SCH_REQUIRED_FIELD_MISSING` | the request is malformed | the caller |
| `BIZ_PROVIDER_NOT_FOUND` | no `active` binding or participant | the registry operator |
| `NET_*` | the upstream is down or timed out | nobody — it is a fact |

Sync changes where these land: a NACK is the response to the call that caused it, so the caller
always sees it. It also means the farmer waits out `timeoutMs` × attempts before seeing `NET_*`.

```json
{ "message": { "status": "NACK" },
  "error": { "code": "BIZ_PROVIDER_NOT_FOUND",
             "message": "no active binding for mausamgram|openagrinet:MandiPrice" } }
```

One code for all three is an afternoon of auditing a correct registry.

## True in every use case

1. **Two hops, both synchronous.** `discover` finds who; `select` gets what; each answer comes
   back on the connection that asked. Synchronous is BV's decision and the spec's `Ack` says
   otherwise — see [above](#both-hops-are-synchronous--the-spec-has-not-caught-up).
2. **`bindingKey` plus the action is the only route.** `<provider>|<capability>`, then the
   `actions[]` entry matching `context.action` — and the participant comes off the binding row,
   never off the request.
3. **The registry never appears in the request path.** Records load at boot; resolution is two map
   lookups.
