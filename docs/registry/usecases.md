# Use cases

Six farmer questions, traced from the question to the answer. For each one: the registry records
you need, the calls in order, the real payloads, and what to watch out for.

| | Question | Capability | Provider |
|---|---|---|---|
| [1](#1-weather-forecast-for-a-point--mausamgram) | Will it rain in the next five days? | `WeatherObservation` | `mausamgram` |
| [2](#2-weather-forecast-for-a-city--imd-city-weather) | Same question, city station | `WeatherObservation` | `imd-city-weather` |
| [3](#3-mandi-price--agmarknet) | What is tomato selling for? | `MandiPrice` | `agmarknet` |
| [4](#4-scheme-information--hasura-content) | What subsidy can I get for drip? | `KnowledgeResource` | `hasura-content` |
| [5](#5-crop--pest-advisory--oan-vector) | Yellow spots on my cotton leaves | `KnowledgeResource` | `oan-vector` |
| [6](#6-weather-advisory--not-seeded) | Should I spray tomorrow? | `WeatherAdvisory` | *not seeded — worked through anyway* |

Use case 1 shows every envelope in full. Use cases 2–6 show only what differs.

Related: [examples.md](examples.md) has the records ready to POST · [schemas.md](schemas.md) is
the field-by-field contract · [api.md](api.md) is the registry's own API.

---

## How a call flows

**Two hops, and only the second one touches the registry.** Every use case below is broken into
the same steps, numbered `<use case>.<step>`:

```
.0  the records the registry must hold

.1  POST /discover     who can answer this?           answered from the catalog — no registry read
.2  POST /on_discover  the provider list, as a callback

.3  POST /select       that provider, this data
.4  resolve            bindingKey → call plan + credential      ← the two registry reads
.5  call upstream      map the request, authenticate, call
.6  POST /on_select     the typed answer, as a callback

.7  does it conform to the OAN domain pack?
```

Between `.2` and `.3` the adopter has a `provider.id` and an `@type`. That pair *is* the registry
key:

```
bindingKey = "mausamgram" + "|" + "openagrinet:WeatherObservation"
```

So `.4` is **two reads and no join** — the binding by `bindingKey`, then the participant by the
`participantId` on the row you just read.

### Both hops are asynchronous

`/discover` and `/select` do **not** return the data. They return an acknowledgement, and the
answer arrives later as a separate call to your `/on_discover` or `/on_select` endpoint.

```json
200 OK
{ "message": { "status": "ACK", "messageId": "1c0a55d7-8e64-4b19-9a2f-33b7c6e1d905" } }
```

`messageId` echoes the request's — it is how you match the callback to the call.
`transactionId` stays the same across both hops, tying them to one farmer question.

If the request is rejected you get the same envelope with `NACK` and an error:

```json
400 Bad Request
{ "message": {
  "status": "NACK",
  "messageId": "1c0a55d7-8e64-4b19-9a2f-33b7c6e1d905",
  "error": { "code": "BIZ_PROVIDER_NOT_FOUND",
             "message": "no active binding for mausamgram|openagrinet:WeatherAdvisory" } } }
```

| code | means | who fixes it |
|---|---|---|
| `SCH_REQUIRED_FIELD_MISSING` | no `offer.provider`, or no `@type` | the caller |
| `CTX_ACTION_MISMATCH` | `context.action` is not `select` | the caller |
| `SCH_INVALID_JSONPATH` | the filter expression is the wrong dialect | the caller |
| `BIZ_PROVIDER_NOT_FOUND` | no `active` binding, or the participant is inactive | the operator |
| `BIZ_NO_RESULTS_FOUND` | upstream answered and had nothing | nobody — a real empty |
| `NET_TIMEOUT` / `NET_DOWNSTREAM_UNAVAILABLE` | upstream is slow or down | nobody — retry |

Full rules for what the adapter reads off a `select`, and why it validates before building the
key, are in [schemas.md § What select must supply](schemas.md#what-select-must-supply).

### Writing the `discover` filter

Use **PostgreSQL SQL/JSON path**, not RFC 9535. The field is typed `string`, so the wrong
dialect is accepted and silently matches nothing.

| | RFC 9535 | what actually runs |
|---|---|---|
| filter | `$.catalogs[?(...)]` | `$.catalogs[*] ? (...)` |
| quoting | `['@type']`, `'x'` | `."@type"`, `"x"` — double quotes |
| membership | `anyof [a, b]` | expand to `a \|\| b` |

Root at the **resource**, not the catalog — `resourceAttributes` only exists at
`$.catalogs[*].resources[*]`. Rooting one level up passes validation and returns nothing.

Spatial `targets` is compared as text, not evaluated, so it must start `$['catalogs'][*]`:

| `targets` | matches |
|---|---|
| `$['provider']['availableAt'][*]['geo']` | **no** — missing root |
| `$['catalogs'][*]['provider']['availableAt'][*]['geo']` | yes |
| `$.catalogs[*].provider.availableAt[*].geo` | yes — normalises to the above |

### Where these payloads come from

| | provenance |
|---|---|
| `mausamgram`, `imd-city-weather`, `agmarknet` responses | **captured** from `Network Information.xlsx` |
| Hasura and vector-search responses | **illustrative** — these providers are in no workbook row |
| Beckn envelopes | shaped against `tests/testdata/beckn-v2.0.0.yaml` |
| registry request bodies | corroborated; **response bodies are illustrative** ([api.md](api.md#what-is-not-verified)) |

---

## 1. Weather forecast for a point — `mausamgram`

*"पुढच्या पाच दिवसांत पाऊस पडेल का?"* Device location Nashik, `[73.7898, 19.9975]`.

### 1.0 Records you need

Seed in this order — the binding requires the other two to exist and be `active`.

| # | entity | key | the part that matters |
|---|---|---|---|
| 1 | `SchemaRegistry` | `openagrinet:WeatherObservation` | `version: v0.1`, pointing at the pack |
| 2 | `Participant` | `mausamgram` | `https://mausamgram.imd.gov.in/nwpapi`, HTTP Basic |
| 3 | `ProviderSchema` | `mausamgram\|openagrinet:WeatherObservation` | `GET /get-daily` |

```json
{ "ProviderSchema": {
  "bindingKey": "mausamgram|openagrinet:WeatherObservation",
  "participantId": "mausamgram",
  "capabilityCode": "openagrinet:WeatherObservation",
  "method": "GET",
  "path": "/get-daily",
  "requestMapping":  "mappings/mausamgram/select.request.jsonata",
  "responseMapping": "mappings/mausamgram/select.response.jsonata",
  "timeoutMs": 30000,
  "retryMax": 3,
  "status": "active"
} }
```

`timeoutMs` and `retryMax` are per-provider registry fields, not service constants — IMD is slow,
and an operator changes this without a deploy.

### 1.1 `discover` — who can answer

```json
POST /discover
{
  "context": { "version": "2.0.0", "action": "discover",
    "bapId": "vistaar.gov.in", "bapUri": "https://vistaar.gov.in/beckn",
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

### 1.2 `on_discover` — the answer, as a callback

`Catalog` requires `id`, `descriptor` and `provider`, and allows nothing else.

```json
POST https://vistaar.gov.in/beckn/on_discover
{
  "context": { "version": "2.0.0", "action": "on_discover",
    "bppId": "oan-adapter.gov.in", "bppUri": "https://oan-adapter.gov.in/beckn",
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

Two providers, **no registry read**, and `informationMode: OnDemand` with no values — that is the
difference between the hops. Which of the two to pick is the app's call, not the registry's.

### 1.3 `select` — that provider, this data

```json
POST /select
{
  "context": { "version": "2.0.0", "action": "select",
    "bapId": "vistaar.gov.in", "bapUri": "https://vistaar.gov.in/beckn",
    "bppId": "oan-adapter.gov.in", "bppUri": "https://oan-adapter.gov.in/beckn",
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

```json
200 OK
{ "message": { "status": "ACK", "messageId": "7d41b9e0-52a6-4c18-8b73-1e9f0a4c6d22" } }
```

`DRAFT` and no price: nothing is being committed. For an open-data provider the quote is
zero-cost and the payload *is* the data. New `messageId`, same `transactionId`.

### 1.4 Resolve — two registry reads

```
offer.provider.id            → "mausamgram"
resourceAttributes["@type"]  → "openagrinet:WeatherObservation"
bindingKey                   = "mausamgram|openagrinet:WeatherObservation"
```

**Read 1 — the call plan.**

```json
POST /api/v1/ProviderSchema/search
{ "filters": { "bindingKey": { "eq": "mausamgram|openagrinet:WeatherObservation" },
               "status":     { "eq": "active" } } }
```
```json
[ { "osid": "1-4c7d5e91-2a08-4f6b-8d13-77e0c9a4b521",
    "bindingKey": "mausamgram|openagrinet:WeatherObservation",
    "participantId": "mausamgram",
    "method": "GET", "path": "/get-daily",
    "requestMapping":  "mappings/mausamgram/select.request.jsonata",
    "responseMapping": "mappings/mausamgram/select.response.jsonata",
    "timeoutMs": 30000, "retryMax": 3, "status": "active" } ]
```

**Read 2 — where it is and how to authenticate.** `participantId` comes off the row above,
**never off the request** — a request that could name the participant could point a credentialled
call at a host of its choosing.

```json
POST /api/v1/Participant/search
{ "filters": { "participantId": { "eq": "mausamgram" }, "status": { "eq": "active" } } }
```
```json
[ { "osid": "1-8f2c4e7a-3b91-4d0e-9c55-2a1f6b8e0d34",
    "participantId": "mausamgram", "name": "IMD Mausamgram NWP",
    "roles": ["provider"],
    "baseUrl": "https://mausamgram.imd.gov.in/nwpapi", "status": "active",
    "auth": { "scheme": "basic",
              "secrets": { "username": "env://MAUSAMGRAM_USER",
                           "password": "env://MAUSAMGRAM_X_API_KEY" } } } ]
```

An empty result from either read is a hard failure — `BIZ_PROVIDER_NOT_FOUND`, not a fallback.

There is **no `SchemaRegistry` read**: a capability is vocabulary, not part of the call path.
And in production these two searches don't happen per request — all 13 records load at boot, so
resolution is two map lookups ([api.md](api.md#the-runtime-does-not-call-these-per-request)).

### 1.5 Call upstream

The adapter reads the point off the request. **GeoJSON is `[longitude, latitude]`** and
mausamgram wants `lat`/`lon`, so the mapping swaps them — getting it backwards returns a forecast
for the Arabian Sea with no error.

```
GET https://mausamgram.imd.gov.in/nwpapi/get-daily?lat=19.9975&lon=73.7898
Authorization: Basic <resolved from env://MAUSAMGRAM_USER + env://MAUSAMGRAM_X_API_KEY>
timeout 30000ms   retries 3
```

**Captured response**, two of its five day-blocks, values re-pointed to Nashik:

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

**Watch out:**

- **`lat_r`/`lon_r` is not `location`.** `lat_r`/`lon_r` is the model grid point the forecast was
  computed at; `location` echoes what you asked for. They differ — about 9 km in the captured
  sample. This walkthrough publishes the **requested** point and keeps the grid point as
  provenance. That is a decision, not a fact.
- **`abbreviation` is the only source of units.** The pack requires a `unit` on every parameter
  and the values carry none. Read them from here; don't guess.
- **`tmax` is bias-corrected, `tmax_raw` is not.** Use `tmax`.
- **`cloud_message` is misspelled upstream** (`"Generraly"`). Match on the numbers, not the text.

### 1.6 `on_select` — the typed answer

`on_select` requires a `contract`, so resources travel inside a commitment. Two of five days:

```json
POST https://vistaar.gov.in/beckn/on_select
{
  "context": { "version": "2.0.0", "action": "on_select",
    "bapId": "vistaar.gov.in", "bapUri": "https://vistaar.gov.in/beckn",
    "bppId": "oan-adapter.gov.in", "bppUri": "https://oan-adapter.gov.in/beckn",
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

Only `informationMode` changed between the hops — `OnDemand` → `Direct`. `@context` and `@type`
are the same. `@type` is a **single string**; the two-element array form some examples show fails
validation.

### 1.7 Conformance — 0 violations

`WeatherObservation` requires `observationType`, `source`, `location`, `generatedAt`,
`parameters`; each parameter requires `parameter`, `value`, `unit`. All present, and every
`parameter` name is in the governed enum.

One caveat: **`aggregation` is ours, not the pack's.** The parameter object is open so it
validates, but it means nothing to another participant — which leaves *"tomorrow's high is 39.24
and low is 32.8"* with no conformant expression. Every Indian weather API reports tmin/tmax, so
this needs fixing at pack level ([schemas.md § Known gaps](schemas.md#known-gaps)).

---

## 2. Weather forecast for a city — `imd-city-weather`

Same question, same capability, **a different call plan**. IMD's city service is keyed by station
id, and nothing in the Beckn body carries one.

### 2.0 Records you need

| # | entity | key | note |
|---|---|---|---|
| 1 | `SchemaRegistry` | `openagrinet:WeatherObservation` | **already seeded by use case 1** |
| 2 | `Participant` | `imd-city-weather` | `https://city.imd.gov.in` |
| 3 | `ProviderSchema` | `imd-city-weather\|openagrinet:WeatherObservation` | `GET /api/cityweather_loc.php` |

```json
{ "ProviderSchema": {
  "bindingKey": "imd-city-weather|openagrinet:WeatherObservation",
  "participantId": "imd-city-weather",
  "capabilityCode": "openagrinet:WeatherObservation",
  "method": "GET",
  "path": "/api/cityweather_loc.php",
  "requestMapping":  "mappings/imd-city-weather/select.request.jsonata",
  "responseMapping": "mappings/imd-city-weather/select.response.jsonata",
  "timeoutMs": 15000,
  "status": "active"
} }
```

**One capability, two participants, two bindings.** Adding this provider added one `Participant`
row and one `ProviderSchema` row. Nothing about the capability changed, and nothing about
`mausamgram` changed.

### 2.1–2.3 Same intent, same filter

`on_discover` returns **both** weather providers — that payload is [above](#12-on_discover--the-answer-as-a-callback),
second catalog. The `select` is [use case 1's](#13-select--that-provider-this-data) with two values
changed:

```json
"resources": [{
  "id": "res:imd-city-weather:station-forecast",
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/WeatherObservation/v0.1/context.jsonld",
    "@type": "openagrinet:WeatherObservation",
    "location": { "type": "Point", "coordinates": [73.7898, 19.9975] },
    "validity": { "startsAt": "2026-08-26", "endsAt": "2026-08-30" }
  }
}],
"offer": { "id": "offer:imd-city-weather:open-data",
           "resourceIds": ["res:imd-city-weather:station-forecast"],
           "provider": { "id": "imd-city-weather",
                         "descriptor": { "code": "IMD-CITY-01", "name": "IMD City Weather" } } }
```

### 2.4 Resolve

```
bindingKey = "imd-city-weather|openagrinet:WeatherObservation"
```

Same two reads, different filter values → `GET https://city.imd.gov.in/api/cityweather_loc.php`.
Same `@type`, different `provider.id`, different row, and **no branching in the adapter.**

### 2.5 Call upstream

First the adapter turns coordinates into a station id — a proximity query against a Postgres
table it owns:

```
nearestStation(19.9975, 73.7898) → { stationId: "42403", stationName: "Nashik", distanceKm: 4.1 }
```

**This transform is not in the registry, on purpose.** No JSONata expression can do a spatial
join, which is the bar for it not being a mapping. Putting it on the binding as a named plugin
would assert that every adapter on the network can run `nearestStation`, takes the same config,
and reaches the same database. None of the three is true of anyone but us.

Stations are tried in proximity order, so one with no IMD data behind it doesn't fail the request.

```
GET https://city.imd.gov.in/api/cityweather_loc.php?id=42403
timeout 15000ms   no retry
```

**Captured response** — note the outer array:

```json
[
  {
    "Date": "2026-08-26",
    "Latitude": "19.997500000", "Longitude": "73.789800000",
    "Station_Code": "42403", "Station_Name": "Nashik",
    "Sunrise_time": "06:18", "Sunset_time": "18:52",
    "Todays_Forecast": "Generally cloudy sky with one or two spells of rain or thundershowers",
    "Todays_Forecast_Max_Temp": "30.0", "Todays_Forecast_Min_temp": "22.0",
    "Today_Max_temp": null, "Today_Min_temp": "22.4",
    "Past_24_hrs_Rainfall": "NIL",
    "Previous_Day_Max_temp": "29.6",
    "Relative_Humidity_at_0830": "88", "Relative_Humidity_at_1730": null,
    "Today_Max_Departure_from_Normal": null, "Today_Min_Departure_from_Normal": "0.8",
    "Day_2_Forecast": "Generally cloudy sky with a few spells of rain",
    "Day_2_Max_Temp": "29.6", "Day_2_Min_temp": "22.8",
    "Day_3_Forecast": "Thunderstorm with rain",
    "Day_3_Max_Temp": "31.0", "Day_3_Min_temp": "23.0",
    "Day_7_Forecast": "Thunderstorm with rain",
    "Day_7_Max_Temp": "32.0", "Day_7_Min_temp": "26.0"
  }
]
```

**Watch out — this payload is a minefield and the pack has no tolerance for any of it:**

- **Numbers arrive as strings.** `"22.4"`, `"88"`. The pack requires `value` to be a number.
  Cast in the mapping.
- **`"NIL"` is a sentinel.** `Past_24_hrs_Rainfall: "NIL"` means no rain → `value: 0`. A naive
  cast gives `null` or `NaN`. This is the likeliest silent failure in this binding.
- **`null` is a real value.** `Today_Max_temp` and `Relative_Humidity_at_1730` are `null` here.
  **Omit** the parameter — `value: null` fails the type, and `0` is a lie about the weather.
- **Today's max is a forecast, not an observation.** `Today_Max_temp` stays `null` until the day
  is over; `Todays_Forecast_Max_Temp` is the number a farmer wants at 6 a.m. Different
  `observationType`.
- **Key casing is inconsistent upstream.** `Day_2_Max_Temp` but `Day_2_Min_temp`. JSONata is
  case-sensitive, so each day-block must be spelled out, not generated from an index.
- **Seven days, not five.** Use case 1 gives five. Nothing in the registry or the pack states a
  horizon.

### 2.6 `on_select`

Same envelope as [use case 1](#16-on_select--the-typed-answer); one resource of the seven:

```json
{
  "id": "res:imd-city-weather:forecast:42403:2026-08-26",
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/WeatherObservation/v0.1/context.jsonld",
    "@type": "openagrinet:WeatherObservation",
    "informationMode": "Direct",
    "observationType": "Forecast",
    "subjectCategories": ["Weather"],
    "source": { "sourceId": "imd-city-weather", "sourceName": "IMD City Weather" },
    "location": { "type": "Point", "coordinates": [73.7898, 19.9975] },
    "validity": { "startsAt": "2026-08-26T00:00:00Z", "endsAt": "2026-08-26T23:59:59Z" },
    "generatedAt": "2026-08-26T06:14:55.010Z",
    "parameters": [
      { "parameter": "Rainfall",    "aggregation": "Total",   "unit": "mm",  "value": 0 },
      { "parameter": "Temperature", "aggregation": "Maximum", "unit": "Cel", "value": 30.0 },
      { "parameter": "Temperature", "aggregation": "Minimum", "unit": "Cel", "value": 22.0 },
      { "parameter": "Humidity",    "aggregation": "Maximum", "unit": "%",   "value": 88 }
    ]
  }
}
```

`Rainfall: 0` is the mapped `"NIL"`. `Humidity` appears once, not twice — the 17:30 reading was
`null` and is omitted rather than invented.

### 2.7 Conformance — 0 violations

Same as use case 1, and the same `aggregation` caveat.

---

## 3. Mandi price — `agmarknet`

*"आज टमाटर का भाव क्या है?"* Tomato, markets near Nashik.

### 3.0 Records you need

| # | entity | key | note |
|---|---|---|---|
| 1 | `SchemaRegistry` | `openagrinet:MandiPrice` | **new capability** |
| 2 | `Participant` | `agmarknet` | API key in the query string |
| 3 | `ProviderSchema` | `agmarknet\|openagrinet:MandiPrice` | `GET /v1/fetch-agmarknet-vistaar-location` |

```json
{ "Participant": {
  "participantId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "roles": ["provider"],
  "baseUrl": "https://api.agmarknet.gov.in",
  "status": "active",
  "auth": { "scheme": "apiKeyQuery",
            "paramName": "token",
            "secrets": { "token": "env://MANDI_TOKEN" } }
} }
```

`apiKeyQuery` is the one scheme where the credential ends up in a URL, so that URL must never
be logged. `paramName` is a registry field because `token`, `api-key` and `key` are all real in
Indian government APIs and none of them is guessable. The schema forbids `valuePrefix` here — a
`Bearer ` prefix in a query string is meaningless.

### 3.1–3.2 `discover`

```
$.catalogs[*].resources[*] ? (@.resourceAttributes."@type" == "openagrinet:MandiPrice")
```

Same spatial constraint as use case 1. `on_discover` returns `agmarknet` only — no weather
provider advertises this type.

### 3.3 `select`

The [use case 1 envelope](#13-select--that-provider-this-data); this `resources` and `offer`:

```json
"resources": [{
  "id": "res:agmarknet:mandi-prices",
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/MandiPriceObservation/v0.1/context.jsonld",
    "@type": "openagrinet:MandiPrice",
    "commodity": { "name": "Tomato" },
    "location": { "type": "Point", "coordinates": [73.7898, 19.9975] },
    "validity": { "startsAt": "2026-08-26", "endsAt": "2026-08-26" }
  }
}],
"offer": { "id": "offer:agmarknet:open-data",
           "resourceIds": ["res:agmarknet:mandi-prices"],
           "provider": { "id": "agmarknet",
                         "descriptor": { "code": "AGMK-01", "name": "Agmarknet" } } }
```

`commodity.name` is a farmer-facing string, not a code. The mapping to Agmarknet's numeric
`commodity_id` is the adapter's problem.

### 3.4 Resolve

```
bindingKey = "agmarknet|openagrinet:MandiPrice"
```

### 3.5 Call upstream

```
commodityCode:  "Tomato" → 101   (adapter-owned lookup table)

GET https://api.agmarknet.gov.in/v1/fetch-agmarknet-vistaar-location
      ?commodity_id=101&lat=19.9975&long=73.7898&date=2026-08-26&token=<env://MANDI_TOKEN>
timeout 20000ms   retries 2
```

The request mapping must emit `lat`, `long`, `commodity_id` and a single `date`. An older
four-code endpoint takes market and state codes instead; that is not what production calls.

**Captured response**, one market of several:

```json
{
  "records": [
    {
      "State": "Maharashtra",
      "District": "Nashik",
      "Market": "Nashik",
      "Commodity": "Tomato",
      "Variety": "Local",
      "Arrival Date": "15-07-2026",
      "Min Price": "800",
      "Max Price": "1600",
      "Modal Price": "1200",
      "Price Unit": "Rs./Qtl"
    }
  ]
}
```

**Watch out:**

- **The keys are Title Case with spaces.** `"Max Price"`, not `max_price`. A mapping written
  against snake_case returns nothing at all — no error, just an empty result. This page used to
  document the snake_case form; it was wrong.
- **`"Rs./Qtl"` is two fields in one string.** The pack requires `currency` as an ISO 4217 code
  and `unit` separately: `"Rs./Qtl"` → `currency: "INR"`, `unit: "QUINTAL"`.
- **`"Arrival Date"` is `DD-MM-YYYY`.** The pack wants a date. `15-07-2026` parsed as ISO is
  either an error or, worse, a valid wrong date.
- **Prices are strings.** Cast them.

### 3.6 `on_select`

```json
{
  "id": "res:agmarknet:price:nashik:tomato:2026-07-15",
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/MandiPriceObservation/v0.1/context.jsonld",
    "@type": "openagrinet:MandiPrice",
    "informationMode": "Direct",
    "source": { "sourceId": "agmarknet", "sourceName": "Agmarknet" },
    "commodity": { "name": "Tomato", "variety": "Local" },
    "market": { "marketName": "Nashik", "district": "Nashik", "state": "Maharashtra" },
    "arrivalDate": "2026-07-15",
    "prices": { "minimum": 800, "maximum": 1600, "modal": 1200,
                "currency": "INR", "unit": "QUINTAL" },
    "generatedAt": "2026-08-26T07:02:11.884Z",
    "location": { "type": "Point", "coordinates": [73.7898, 19.9975] }
  }
}
```

### 3.7 Conformance — 3 violations to fix

`MandiPriceObservation` requires `source`, `commodity`, `market`, `arrivalDate`, `prices`,
`generatedAt`. The resource above satisfies all six. What does **not** conform is the shape this
page and the seeded records carried before:

| # | was | pack requires |
|---|---|---|
| 1 | `arrivalDate` absent | required |
| 2 | `market` a bare string | an object with `marketName` |
| 3 | `prices` an array of `{type, value}` | an object `{minimum, maximum, modal, currency, unit}` with `currency` as `^[A-Z]{3}$` |

There is also an unresolved naming conflict: the capability code is `openagrinet:MandiPrice` but
the pack directory is `MandiPriceObservation`, and the network spec contradicts itself. Needs a
network-owner ruling ([schemas.md § Known gaps](schemas.md#known-gaps)).

---

## 4. Scheme information — `hasura-content`

*"ठिबक सिंचनासाठी काय अनुदान मिळेल?"* What subsidy is available for drip irrigation?

### 4.0 Records you need

| # | entity | key | note |
|---|---|---|---|
| 1 | `SchemaRegistry` | `openagrinet:KnowledgeResource` | **new capability** |
| 2 | `Participant` | `hasura-content` | `apiKeyHeader` — admin secret in a header |
| 3 | `ProviderSchema` | `hasura-content\|openagrinet:KnowledgeResource` | `POST /v1/graphql` |

```json
{ "Participant": {
  "participantId": "hasura-content",
  "name": "Vistaar Knowledge Content (Hasura)",
  "roles": ["provider"],
  "baseUrl": "https://content.internal",
  "status": "active",
  "auth": { "scheme": "apiKeyHeader",
            "paramName": "x-hasura-admin-secret",
            "secrets": { "adminSecret": "env://HASURA_GRAPHQL_ADMIN_SECRET" } }
} }
```

### 4.1–4.2 `discover`

```
$.catalogs[*].resources[*] ? (@.resourceAttributes."@type" == "openagrinet:KnowledgeResource")
```

Returns **both** `hasura-content` and `oan-vector` — see [use case 5](#5-crop--pest-advisory--oan-vector).

### 4.3 `select`

```json
"resources": [{
  "id": "res:hasura-content:schemes",
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/KnowledgeResource/v0.1/context.jsonld",
    "@type": "openagrinet:KnowledgeResource",
    "knowledgeType": "Guide",
    "topics": ["Irrigation", "Subsidy"],
    "languages": ["mr"],
    "query": "drip irrigation subsidy"
  }
}],
"offer": { "id": "offer:hasura-content:open",
           "resourceIds": ["res:hasura-content:schemes"],
           "provider": { "id": "hasura-content",
                         "descriptor": { "code": "OAN-CONTENT-01", "name": "OAN Content Store" } } }
```

### 4.4 Resolve

```
bindingKey = "hasura-content|openagrinet:KnowledgeResource"
```

### 4.5 Call upstream

**`POST` with a body**, unlike use cases 1–3. The request mapping builds the GraphQL document
and its variables:

```
POST https://content.internal/v1/graphql
x-hasura-admin-secret: <env://HASURA_GRAPHQL_ADMIN_SECRET>
```

`paramName` carries the header name and `secrets` its value — the schema has one `apiKeyHeader`
scheme, not a separate `header` one, and it allows exactly one secret when `paramName` is used.
```json
{
  "query": "query Schemes($q: String!, $lang: String!) { schemes(where: {_and: [{title: {_ilike: $q}}, {language: {_eq: $lang}}]}) { id title summary language url published_at department } }",
  "variables": { "q": "%drip irrigation subsidy%", "lang": "mr" }
}
```

The query string is a constant inside the mapping; only `variables` is built from the request.
Building the query text itself from user input would be GraphQL injection.

**Illustrative response** — this provider is in no workbook row:

```json
{ "data": { "schemes": [
  { "id": "PMKSY-DRIP-2026",
    "title": "प्रधानमंत्री कृषी सिंचन योजना — ठिबक सिंचन अनुदान",
    "summary": "अल्प व अत्यल्प भूधारक शेतकऱ्यांना ५५% अनुदान; इतर शेतकऱ्यांना ४५%.",
    "language": "mr",
    "url": "https://content.internal/schemes/pmksy-drip-2026",
    "published_at": "2026-04-01T00:00:00Z",
    "department": "Department of Agriculture, Govt. of Maharashtra" }
] } }
```

**Watch out:**

- **GraphQL returns `200` with an `errors` array.** A transport-level check on the status code
  will treat a failed query as success and map an empty result. Check `data` and `errors`.
- **`_ilike` needs the `%` wildcards added by the mapping.** Without them the query is an exact
  match and finds nothing.

### 4.6 `on_select`

```json
{
  "id": "res:hasura-content:scheme:PMKSY-DRIP-2026",
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/KnowledgeResource/v0.1/context.jsonld",
    "@type": "openagrinet:KnowledgeResource",
    "informationMode": "Direct",
    "knowledgeType": "Guide",
    "topics": ["Irrigation", "Subsidy"],
    "languages": ["mr"],
    "version": "2026.1",
    "lifecycleStatus": "Published",
    "content": [{
      "mediaType": "text/html",
      "contentUri": "https://content.internal/schemes/pmksy-drip-2026",
      "language": "mr"
    }],
    "provenance": {
      "source": "Department of Agriculture, Govt. of Maharashtra",
      "publishedAt": "2026-04-01T00:00:00Z"
    }
  }
}
```

### 4.7 Conformance — 6 violations to fix

`KnowledgeResource` requires `knowledgeType`, `languages`, `version`, `lifecycleStatus`,
`content`, `provenance`. The resource above satisfies all six; the seeded records satisfy none of
the last five, and use `knowledgeType: "SchemeInformation"`, which is not in the enum
(`Article`, `FAQ`, `Guide`, `Dataset`, `TrainingMaterial`, `Reference`) — `Guide` is the right
value.

`version` and `lifecycleStatus` have **no upstream source**. Hasura carries neither, so they are
either synthesised by the mapping or added to the content store's own schema. The second is the
honest fix.

---

## 5. Crop & pest advisory — `oan-vector`

*"माझ्या कापसाच्या पानांवर पिवळे डाग आहेत"* — yellow spots on my cotton leaves. A free-text
symptom description, not a keyword.

### 5.0 Records you need

| # | entity | key | note |
|---|---|---|---|
| 1 | `SchemaRegistry` | `openagrinet:KnowledgeResource` | **already seeded by use case 4** |
| 2 | `Participant` | `oan-vector` | no auth |
| 3 | `ProviderSchema` | `oan-vector\|openagrinet:KnowledgeResource` | `POST /indexes/oan-index/search` |

```json
{ "Participant": {
  "participantId": "oan-vector",
  "name": "OAN Vector Index",
  "roles": ["provider"],
  "baseUrl": "http://3.6.146.174:8882",
  "status": "active",
  "auth": { "scheme": "none" }
} }
```

`scheme: "none"` is a deliberate value, not a missing field — `auth` is required, so an
unauthenticated provider has to say so. It is also the only reason this record validates: the
schema requires `https` for every other scheme, and this is a bare IP over plain HTTP. Nothing is
leaked because there is no credential, but it should be behind TLS before v1 carries real
traffic.

### 5.1–5.3 Same capability as use case 4

Same filter, same `on_discover` (both knowledge providers). The `select` differs only in the
provider and in what goes into `resourceAttributes`:

```json
"resources": [{
  "id": "res:oan-vector:advisory",
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/KnowledgeResource/v0.1/context.jsonld",
    "@type": "openagrinet:KnowledgeResource",
    "knowledgeType": "Reference",
    "topics": ["PestManagement"],
    "languages": ["mr"],
    "query": "माझ्या कापसाच्या पानांवर पिवळे डाग आहेत"
  }
}],
"offer": { "id": "offer:oan-vector:open",
           "resourceIds": ["res:oan-vector:advisory"],
           "provider": { "id": "oan-vector",
                         "descriptor": { "code": "OAN-VEC-01", "name": "OAN Advisory Vector Search" } } }
```

**Two providers, one capability, and the app chooses on the shape of the question.** Use case 4's
question is a keyword lookup; this one is a similarity search. The registry does not encode that
difference — both bindings advertise `KnowledgeResource` and both are equally valid answers to
hop ①.

### 5.4 Resolve

```
bindingKey = "oan-vector|openagrinet:KnowledgeResource"
```

### 5.5 Call upstream

```
POST http://3.6.146.174:8882/indexes/oan-index/search
```
```json
{ "query": "माझ्या कापसाच्या पानांवर पिवळे डाग आहेत",
  "language": "mr", "topK": 5, "minScore": 0.72 }
```

`topK` and `minScore` are constants in the request mapping, not registry fields — they are
retrieval tuning, and changing them changes the answer's quality, not the call's target.

**Illustrative response** — this provider is in no workbook row:

```json
{ "results": [
  { "id": "ADV-COTTON-YELLOW-001",
    "score": 0.89,
    "title": "कापसावरील पिवळा मोझॅक — ओळख व उपाय",
    "body": "पिवळे डाग व शिरांमधील पिवळेपणा हे पिवळ्या मोझॅक विषाणूचे लक्षण असू शकते...",
    "language": "mr",
    "crop": "Cotton",
    "topic": "PestManagement",
    "updated_at": "2026-06-18T00:00:00Z",
    "source": "ICAR — Central Institute for Cotton Research" }
] }
```

**Watch out:**

- **`score` has nowhere to go in the pack.** `KnowledgeResource` has no relevance field. Dropping
  it loses the ranking; inventing a field means only we can read it. For now the mapping preserves
  the array order and drops the number.
- **`minScore` can return zero results legitimately.** That is `BIZ_NO_RESULTS_FOUND`, not an
  error. A vector search that finds nothing above threshold is working correctly.

### 5.6 `on_select`

```json
{
  "id": "res:oan-vector:advisory:ADV-COTTON-YELLOW-001",
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/KnowledgeResource/v0.1/context.jsonld",
    "@type": "openagrinet:KnowledgeResource",
    "informationMode": "Direct",
    "knowledgeType": "Reference",
    "topics": ["PestManagement"],
    "languages": ["mr"],
    "version": "2026.2",
    "lifecycleStatus": "Published",
    "content": [{
      "mediaType": "text/plain",
      "inlineContent": "पिवळे डाग व शिरांमधील पिवळेपणा हे पिवळ्या मोझॅक विषाणूचे लक्षण असू शकते...",
      "language": "mr"
    }],
    "provenance": {
      "source": "ICAR — Central Institute for Cotton Research",
      "publishedAt": "2026-06-18T00:00:00Z"
    }
  }
}
```

`content` items require `mediaType` plus exactly one of `contentUri` or `inlineContent`. Use case
4 has a URL, so it uses `contentUri`; this one has the text, so it uses `inlineContent`. Both is
invalid.

### 5.7 Conformance — 6 violations to fix

The same six as use case 4, on the same records, for the same reason — and `knowledgeType` was
`CropAdvisory`, also not in the enum. `Reference` is the right value.

---

## 6. Weather advisory — *not seeded*

*"उद्या फवारणी करावी का?"* Should I spray tomorrow? This case is here because **adding a
capability is the operation this page gets opened for** — and because working it through is the
argument for *not* adding this one.

### What it would look like

| # | entity | key |
|---|---|---|
| 1 | `SchemaRegistry` | `openagrinet:WeatherAdvisory` |
| 2 | `Participant` | `mausamgram` — **already exists** |
| 3 | `ProviderSchema` | `mausamgram\|openagrinet:WeatherAdvisory` |

```json
{ "ProviderSchema": {
  "bindingKey": "mausamgram|openagrinet:WeatherAdvisory",
  "participantId": "mausamgram",
  "capabilityCode": "openagrinet:WeatherAdvisory",
  "method": "GET",
  "path": "/get-daily",
  "requestMapping":  "mappings/mausamgram/select.request.jsonata",
  "responseMapping": "mappings/mausamgram/select.response.jsonata",
  "status": "active"
} }
```

`method` and `path` are **identical to use case 1** on purpose: two capabilities can share a
transport completely and differ only in `responseMapping`. `capabilityCode` names the outcome,
not the URL.

The `discover` filter would be the same shape with `openagrinet:WeatherAdvisory` — **not
verified**, unlike use cases 1–5. Nothing of this type is published to test it against.

### Why it should not be seeded

`WeatherAdvisory` requires `topics`, `location`, `issuedAt`, `validity`, `recommendations`,
`source`. Three of those have **no upstream source at all** — `issuedAt`, `source`, and honest
`recommendations`. The nearest field mausamgram offers is:

```json
"weather_warning": "Generally Cloudy Sky"
```

That is a description of the sky. It validates as a `recommendations[].message` and it lies about
what it is: nothing in it advises anyone to do anything, and a farmer reading it under the
heading *advisory* is being misled by a conformant record.

**mausamgram is an observation provider, and `WeatherObservation` is its correct capability.** A
real advisory needs an Agromet bulletin as its source, and no such provider appears in the
network information workbook.

**The general rule this case exists to show:** a `capabilityCode` is a promise about the outcome,
and no schema can check a pack's required fields against a provider's actual payload. Nothing in
the registry would have refused the record above.

### And the mapping-path convention cannot express it

Mapping paths are `mappings/<participant>/<action>.<request|response>.jsonata` — there is **no
capability segment**. Both of mausamgram's bindings are the `select` action, so both resolve to
the same two filenames while needing different output shapes.

Nothing rejects this: the path is validated as a string pattern, and the records are valid. The
segment must also be lowercase, so `mappings/mausamgram/weatheradvisory.response.jsonata` is
legal and `WeatherAdvisory.response.jsonata` is not. Recorded in
[schemas.md § Known gaps](schemas.md#known-gaps).

---

## One capability, many providers

Six use cases, **four capabilities**, six bindings:

| capability | providers | what changes between them |
|---|---|---|
| `WeatherObservation` | `mausamgram`, `imd-city-weather` | the whole call plan — point vs station id, 5 days vs 7 |
| `MandiPrice` | `agmarknet` | — |
| `KnowledgeResource` | `hasura-content`, `oan-vector` | keyword `GET`-style lookup vs vector similarity |
| `WeatherAdvisory` | *none* | — |

**Adding a provider to an existing capability costs one `Participant` row and one
`ProviderSchema` row.** Nothing about the capability changes, nothing about the existing
providers changes, and no code is deployed. That is the whole point of splitting the three
entities the way [schemas.md](schemas.md) does.

**The capability does not tell you which provider to pick.** Both weather bindings answer the
same `@type`; so do both knowledge bindings. Choosing is the app's job at hop ①, and it has the
`descriptor` and the service area to choose on.

## Three things true in every use case

1. **Hop ① never reads the registry.** Discovery is answered from the published catalog. If your
   trace shows a registry read before `select`, it is wrong.
2. **`participantId` is never taken from the request.** It comes off the `ProviderSchema` row.
   A request that could name the participant could aim a credentialled call at any host.
3. **The enrich step is never in the registry.** Nearest-station lookup, commodity-code lookup,
   GraphQL query construction — all adapter-internal. Only the two JSONata mappings are registry
   fields. A named plugin in a shared registry is a claim about every other adapter on the
   network.

## What the captured payloads changed

This page was revised against real payloads from `Network Information.xlsx`. Two providers
disagreed with what it used to say:

| | was documented | actually returns |
|---|---|---|
| `agmarknet` | `min_price`, `max_price`, `modal_price`, `arrival_date` | `"Min Price"`, `"Max Price"`, `"Modal Price"`, `"Arrival Date"` — Title Case with spaces. **A mapping written against the old keys returns nothing.** |
| `imd-city-weather` | path `/citywx/city_weather_test.php`; hazard given as "units concatenated into the value" | path `/api/cityweather_loc.php`; an **array**; `"NIL"` sentinels, `null`s, numbers as strings. The documented hazard does not exist; three real ones went unmentioned. |
| `mausamgram` | response `location: {lat, lon}` | **both** `lat_r`/`lon_r` (the model grid point) and `location` (the requested point), and they differ. The old shape was incomplete, not wrong — but which one to publish is a real decision the page never stated. |
| `mausamgram` | units not addressed | an `abbreviation` block that is the authoritative unit source |

Two providers, `hasura-content` and `oan-vector`, appear in **no** workbook row. Their payloads
here are illustrative and marked as such at each site.

One disagreement is left unresolved: the workbook records 'API Key' as the auth for both
`imd-city-weather` and `mausamgram`, while the registry records `none` and `basic`. The registry
values are what the seeded records use.
