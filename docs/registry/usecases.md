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

## Both hops are asynchronous

`/discover` and `/select` return an `Ack` echoing the request `messageId`. The payload arrives as
a separate inbound call to `/on_discover` or `/on_select`, correlated by `transactionId`. Model
`select` as request/response and you will restructure.

`discover` is answered by the **network node** from the published catalog — no fan-out to
providers — so the network node signs `on_discover` itself. `select` and `on_select` are between
the consumer node and the provider node.

---

## 1. Weather forecast for a point — `mausamgram`

*"पुढच्या पाच दिवसांत पाऊस पडेल का?"* Device location Nashik, `[73.7898, 19.9975]`.

Records: `SchemaRegistry` for `openagrinet:WeatherObservation`, `Participant` `mausamgram`,
binding `mausamgram|openagrinet:WeatherObservation`, plus the three nodes — all in
[examples.md](examples.md).

### 1.1 `discover`

```json
POST /discover
{
  "context": { "version": "2.0.0", "action": "discover",
    "bapId": "bap.kisan-app.openagrinet.gov.in",
    "bapUri": "https://bap.kisan-app.openagrinet.gov.in/beckn",
    "transactionId": "9f2c1a8e-4b70-4d31-9c55-6f2e0b1d7a44",
    "messageId": "1c0a55d7-8e64-4b19-9a2f-33b7c6e1d905",
    "timestamp": "2026-08-26T06:11:58.004Z" },
  "message": { "intent": {
    "textSearch": "weather forecast rain next five days",
    "filters": {
      "type": "jsonpath",
      "expression": "$.catalogs[*].resources[*] ? (@.resourceAttributes.\"@type\" == \"openagrinet:WeatherObservation\")"
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
```json
200 OK
{ "message": { "status": "ACK", "messageId": "1c0a55d7-8e64-4b19-9a2f-33b7c6e1d905" } }
```

### 1.2 `on_discover`

`Catalog` requires `id`, `descriptor` and `provider`, and allows nothing else. Two providers
match; which to pick is the app's call, not the registry's.

```json
POST https://bap.kisan-app.openagrinet.gov.in/beckn/on_discover
{
  "context": { "version": "2.0.0", "action": "on_discover",
    "bppId": "network.openagrinet.gov.in",
    "bppUri": "https://network.openagrinet.gov.in/beckn",
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
          "observationType": "Forecast",
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
          "observationType": "Forecast",
          "subjectCategories": ["Weather"]
        }
      }],
      "offers": [{ "id": "offer:imd-city-weather:open-data",
                   "resourceIds": ["res:imd-city-weather:station-forecast"] }]
    }
  ] }
}
```

`informationMode: OnDemand` with no values — that is the difference between the hops.

### 1.3 `select`

`DRAFT` and no price: nothing is being committed. For an open-data provider the quote is
zero-cost and the payload *is* the data. New `messageId`, same `transactionId`.

```json
POST /select
{
  "context": { "version": "2.0.0", "action": "select",
    "bapId": "bap.kisan-app.openagrinet.gov.in",
    "bapUri": "https://bap.kisan-app.openagrinet.gov.in/beckn",
    "bppId": "bpp.openagrinet.gov.in",
    "bppUri": "https://bpp.openagrinet.gov.in/beckn",
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

### 1.4 Resolve — two reads

```
offer.provider.id            → "mausamgram"
resourceAttributes["@type"]  → "openagrinet:WeatherObservation"
bindingKey                   = "mausamgram|openagrinet:WeatherObservation"
```

**Read 1** — the call plan: `ProviderSchema` by `bindingKey`, giving `GET /get-daily`,
`timeoutMs: 30000`, `retryMax: 3`. Those are per-provider registry fields, not service
constants — IMD is slow, and an operator changes this without a deploy.

**Read 2** — where it is and how to authenticate: `Participant` by the `participantId` **from row
1, never from the request**. A request that could name the participant could point a
credentialled call at a host of its choosing.

An empty result from either read is a hard failure — `BIZ_PROVIDER_NOT_FOUND`, not a fallback.

There is **no `SchemaRegistry` read**: a capability is vocabulary, not part of the call path. And
in production neither read is a request at all
([api.md](api.md#the-runtime-does-not-call-these-per-request)).

### 1.5 Call upstream

**GeoJSON is `[longitude, latitude]`** and mausamgram wants `lat`/`lon`, so the mapping swaps
them — getting it backwards returns a forecast for the Arabian Sea with no error.

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

### 1.6 `on_select`

`on_select` requires a `contract`, so resources travel inside a commitment. Two of five days:

```json
POST https://bap.kisan-app.openagrinet.gov.in/beckn/on_select
{
  "context": { "version": "2.0.0", "action": "on_select",
    "bapId": "bap.kisan-app.openagrinet.gov.in",
    "bapUri": "https://bap.kisan-app.openagrinet.gov.in/beckn",
    "bppId": "bpp.openagrinet.gov.in",
    "bppUri": "https://bpp.openagrinet.gov.in/beckn",
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
| 6 | *weather advisory* | — | **Not seeded.** It would be a second capability on `mausamgram`, and mapping paths carry no capability segment, so both would resolve to one filename needing two output shapes. Fix the convention first. |

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

```json
{ "message": { "status": "NACK" },
  "error": { "code": "BIZ_PROVIDER_NOT_FOUND",
             "message": "no active binding for mausamgram|openagrinet:MandiPrice" } }
```

One code for all three is an afternoon of auditing a correct registry.

## True in every use case

1. **Two hops, both asynchronous.** `discover` finds who; `select` gets what.
2. **`bindingKey` is the only route.** `<provider>|<capability>`, and the participant comes off
   the binding row, never off the request.
3. **The registry never appears in the request path.** Records load at boot; resolution is two map
   lookups.
