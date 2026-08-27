# Use case execution

**All five v1 use cases, each traced end to end.** For every one: the records that must be
in the registry, the API calls the adopter makes, and the payload at each hop.

The schema these records satisfy is [registry.md §3](registry.md#3-the-schemas); the
records themselves, in write form, are [examples.md](examples.md). This page is
self-contained — it does not depend on anything under `archive/`.

| | Use case | Capability | Provider | Shape of the call |
|---|---|---|---|---|
| [1](#use-case-1--weather-point-forecast-mausamgram) | Weather — point forecast | `WeatherObservation` | `mausamgram` | `GET`, HTTP Basic, enricher reads the point off the intent |
| [2](#use-case-2--weather-citystation-imd-city-weather) | Weather — city/station | `WeatherObservation` | `imd-city-weather` | `GET`, no auth, enricher does a Postgres proximity lookup |
| [3](#use-case-3--mandi-price-agmarknet) | Mandi price | `MandiPrice` | `agmarknet` | `GET`, API key in the query, enricher maps a commodity name to a code |
| [4](#use-case-4--scheme-information-hasura-content) | Scheme information | `KnowledgeResource` | `hasura-content` | `POST` GraphQL, admin secret header |
| [5](#use-case-5--crop--pest-advisory-oan-vector) | Crop & pest advisory | `KnowledgeResource` | `oan-vector` | `POST` vector search, no auth |

Use cases 4 and 5 share one `capabilityCode`. That is not an oversight — see
[§ Two capabilities, five bindings](#two-capabilities-five-bindings) below.

---

## How every use case executes

Two network calls, and only the second one touches the registry.

| hop | the adopter calls | answered by | registry read? |
|---|---|---|---|
| ① | `POST /discover` | **discovery service**, from its indexed catalog | **no** |
| ② | `POST /select` | **the adapter**, which then calls the provider | **yes** — two lookups |

Between them the adopter has a `provider.id` and an `@type` and nothing else it needs.
That pair *is* the `bindingKey`, which is why hop ② can resolve a call plan without a
lookup table of its own.

Every use case below runs the same six steps:

```
①  resolve meaning       experience layer turns a question into an intent
②  discover              who can answer this?                    → provider ids
③  select                that provider, this data                → adapter
④  resolve               bindingKey → call plan + auth           ← registry
⑤  enrich · map · call   run the enricher, build the request, authenticate, call upstream
⑥  map the response      upstream's native shape → Beckn v2 resources
```

### Writing the `discover` filter

**Use PostgreSQL SQL/JSON path.** `beckn.yaml` says RFC 9535 and its own example uses the
Goessner form, but a discovery service on Postgres executes the expression with `@?`, which
takes a different grammar. `filters.expression` is declared `type: string`, so a wrong
dialect is schema-valid and fails only at query time — and a filter that matches nothing is
indistinguishable from an honest empty result.

| | RFC 9535 (the spec's example) | SQL/JSON path (what runs) |
|---|---|---|
| filter | `$.catalogs[?(...)]` | `$.catalogs[*] ? (...)` |
| quoting | `['@type']`, `'literal'` | `."@type"`, `"literal"` — double quotes |
| membership | `anyof [a, b]` | no such operator — expand to `a \|\| b` |

Root at `$.catalogs`. A filter starting `$[?(...)]` addresses the wrong node.

All five filters below were run against a live PostgreSQL and are GIN-indexable.

### What `Intent` cannot carry, and why `select` exists

`Intent` is `additionalProperties: false` over `{textSearch, filters, spatial, mediaSearch}`.
So at hop ① the farmer's location has to travel as a **spatial constraint on the provider's
service area**, and there is no field at all for a validity window. `resourceAttributes` at
hop ② is an open container, and that is where the actual parameters of the invocation go.

### `@context` and `@type` do not change between hops

Advertisement, request and result all name the same pack. Only `informationMode` moves —
`OnDemand` on the advertisement, `Direct` on the result. If you find a trace where the
*context* changes between hops, it predates the pack index and describes a model that was
dropped.

**The `select` request's `resourceAttributes` is not a pack instance and is not validated as
one.** network-specs declares each pack's placement as *provider catalog*, *discovery
result* or *provider response*; a request is none of those. Beckn types the field as
`Attributes` — open, requiring only `@context` and `@type` — so what travels there is the
resource's identity plus the parameters of the invocation.

---

## Use case 1 — Weather, point forecast (`mausamgram`)

*"पुढच्या पाच दिवसांत पाऊस पडेल का?"* — will it rain in the next five days? Device location
Nashik, `[73.7898, 19.9975]`.

### What must be in the registry

Three records. Seed them in this order — the binding's two integrity rules require the other
two to exist and be `active` first.

| # | entity | key | the part that matters here |
|---|---|---|---|
| 1 | `Capability` | `openagrinet:WeatherObservation` | `schemaUrl` → the `WeatherObservation/v0.1` pack, sha-pinned |
| 2 | `Provider` | `mausamgram` | `baseUrl` `https://mausamgram.imd.gov.in/nwpapi` · `auth.scheme: basic` |
| 3 | `ProviderCapability` | `mausamgram\|openagrinet:WeatherObservation` | `GET /get-daily` · `enricher: pointFromIntent` |

```json
{ "ProviderCapability": {
  "bindingKey": "mausamgram|openagrinet:WeatherObservation",
  "providerId": "mausamgram",
  "capabilityCode": "openagrinet:WeatherObservation",
  "method": "GET",
  "path": "/get-daily",
  "enricher": { "name": "pointFromIntent" },
  "requestMapping":  "mappings/mausamgram/select.request.jsonata",
  "responseMapping": "mappings/mausamgram/select.response.jsonata",
  "timeoutMs": 30000,
  "retryMax": 3,
  "status": "active"
} }
```

`timeoutMs` and `retryMax` are registry columns, not constants in a service class: IMD is
slow and flaky, and 30 s with 3 retries is a property of *this provider*, changed by an
operator without a deploy.

### ① Resolve meaning

The experience layer turns the question into an intent: weather, next five days, near
Nashik.

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

`on_discover` names the providers that advertise this type in that area — `mausamgram` and
`imd-city-weather` both match. **No registry was read to answer this.**

### ③ `select` — name that provider, ask for the data

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

`status: DRAFT`, no price. Nothing is being committed — for an open-data provider the quote
is zero-cost and the payload *is* the data.

### ④ Resolve — build the key, read the plan

Everything needed is already on the request body:

```
offer.provider.id            → "mausamgram"
resourceAttributes["@type"]  → "openagrinet:WeatherObservation"

bindingKey = "mausamgram|openagrinet:WeatherObservation"
```

Two values joined by a `|`, and no action. That key is the entire lookup — **two reads, no
join.** Shown here in full; use cases 2–5 show the same two calls in short form.

**Read 1 — the call plan.**

```http
POST /api/v1/ProviderCapability/search
Authorization: Bearer <read-token>
Content-Type: application/json
```
```json
{ "filters": { "bindingKey": { "eq": "mausamgram|openagrinet:WeatherObservation" },
               "status":     { "eq": "active" } } }
```
```json
200 OK
[ { "osid": "1-4c7d5e91-2a08-4f6b-8d13-77e0c9a4b521",
    "bindingKey": "mausamgram|openagrinet:WeatherObservation",
    "providerId": "mausamgram", "capabilityCode": "openagrinet:WeatherObservation",
    "method": "GET", "path": "/get-daily",
    "enricher": { "name": "pointFromIntent" },
    "requestMapping":  "mappings/mausamgram/select.request.jsonata",
    "responseMapping": "mappings/mausamgram/select.response.jsonata",
    "timeoutMs": 30000, "retryMax": 3, "status": "active" } ]
```

**Read 2 — where it is, and how to authenticate.** `providerId` comes off the row just read.

```http
POST /api/v1/Provider/search
Authorization: Bearer <read-token>
```
```json
{ "filters": { "providerId": { "eq": "mausamgram" },
               "status":     { "eq": "active" } } }
```
```json
200 OK
[ { "osid": "1-8f2c4e7a-3b91-4d0e-9c55-2a1f6b8e0d34",
    "providerId": "mausamgram", "name": "IMD Mausamgram NWP",
    "baseUrl": "https://mausamgram.imd.gov.in/nwpapi", "status": "active",
    "auth": { "scheme": "basic",
              "secrets": { "username": "env://MAUSAMGRAM_USER",
                           "password": "env://MAUSAMGRAM_X_API_KEY" } } } ]
```

Those two rows carry everything step ⑤ needs: `baseUrl + path`, the method, both mapping
paths, the enricher name, the timeout and retry budget, and the credential to resolve.
**No `Capability` read** — a capability is vocabulary, not part of the call path.

An empty result from either read is a hard failure, not a fallback: an inactive binding or
an inactive provider means this provider cannot answer, and `on_select` returns an error.

**In production these two calls do not happen per request.** All 13 records are loaded at
boot and indexed by `bindingKey` and `providerId`, so resolution is two map lookups —
[registry.md §5](registry.md#the-runtime-does-not-call-these-per-request).

### ⑤ Enrich, map, authenticate, call

```
pointFromIntent:  resourceAttributes.location.coordinates → _local = {lat: 19.9975, lon: 73.7898}
```

`requestMapping` runs over `{beckn, _local}`; each `env://` pointer is resolved from the
adapter's own environment and applied per `scheme`:

```
GET https://mausamgram.imd.gov.in/nwpapi/get-daily?lat=19.9975&lon=73.7898
Authorization: Basic <resolved from env://MAUSAMGRAM_USER + env://MAUSAMGRAM_X_API_KEY>
timeout 30000ms   retries 3
```

Provider answers in its native shape:

```json
{ "location": { "lat": 19.9975, "lon": 73.7898 },
  "fcstday1": { "date": "2026-08-26", "rain": 12.4, "tmin": 22.1, "tmax": 30.6,
                "wspd": 4.2, "weather_warning": "Heavy rainfall warning" } }
```

### ⑥ Map the response, send it back

`responseMapping` runs over `{request, response, _local}` — `_local` stays in scope so the
resolved point reaches the output. Five forecast days become five typed resources:

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

`@type` is a **single string**. Beckn core declares it `type: string`; the two-element array
form some examples show fails validation.

---

## Use case 2 — Weather, city/station (`imd-city-weather`)

Same question, same capability, a **different provider with a different call plan**. IMD's
city service is keyed by station id, not by coordinates — and nothing in the Beckn body
carries a station id.

### What must be in the registry

| # | entity | key | the part that matters here |
|---|---|---|---|
| 1 | `Capability` | `openagrinet:WeatherObservation` | **already seeded by use case 1** — capabilities are provider-independent |
| 2 | `Provider` | `imd-city-weather` | `baseUrl` `https://city.imd.gov.in` · `auth.scheme: none` |
| 3 | `ProviderCapability` | `imd-city-weather\|openagrinet:WeatherObservation` | `GET /citywx/city_weather_test.php` · enricher with `config` **and** `secrets` |

```json
{ "ProviderCapability": {
  "bindingKey": "imd-city-weather|openagrinet:WeatherObservation",
  "providerId": "imd-city-weather",
  "capabilityCode": "openagrinet:WeatherObservation",
  "method": "GET",
  "path": "/citywx/city_weather_test.php",
  "enricher": { "name": "nearestStation",
                "config": { "maxDistanceKm": 50, "maxStationAttempts": 5 },
                "secrets": { "dsn": "env://IMD_DB_DSN" } },
  "requestMapping":  "mappings/imd-city-weather/select.request.jsonata",
  "responseMapping": "mappings/imd-city-weather/select.response.jsonata",
  "timeoutMs": 15000,
  "status": "active"
} }
```

**This is what an enricher is for.** Turning `[73.7898, 19.9975]` into a station id is a
proximity query against a Postgres table the adapter owns. No JSONata expression can do it —
which is the bar. The registry *names and bounds* the plugin (`maxDistanceKm`,
`maxStationAttempts`) and does not implement it; the DSN is an `env://` pointer resolved at
call time, never stored.

**One `Capability`, two `Provider`s, two bindings.** Adding this provider adds exactly one
`Provider` row and one `ProviderCapability` row. Nothing about `openagrinet:WeatherObservation`
changes, and nothing about `mausamgram` changes.

### ① – ② Same intent, same filter

```
$.catalogs[*] ? (@.resourceAttributes."@type" == "openagrinet:WeatherObservation")
```

`on_discover` returns **both** weather providers. Choosing between them is the experience
layer's call — a station-based service is better for a named city, a point forecast for
arbitrary coordinates — and the registry has no opinion.

### ③ `select`

Identical to use case 1 except for the provider:

```json
"offer": { "id": "offer:imd-city-weather:open-data",
           "resourceIds": ["res:imd-city-weather:station-forecast"],
           "provider": { "id": "imd-city-weather",
                         "descriptor": { "code": "IMD-CITY-01", "name": "IMD City Weather" } } }
```

### ④ Resolve

```
bindingKey = "imd-city-weather|openagrinet:WeatherObservation"
```

Two reads, same shape as [use case 1](#use-case-1--weather-point-forecast-mausamgram) —
only the two filter values change.

```http
POST /api/v1/ProviderCapability/search
```
```json
{ "filters": { "bindingKey": { "eq": "imd-city-weather|openagrinet:WeatherObservation" },
               "status":     { "eq": "active" } } }
```
```http
POST /api/v1/Provider/search
```
```json
{ "filters": { "providerId": { "eq": "imd-city-weather" },
               "status":     { "eq": "active" } } }
```

Together they give `GET https://city.imd.gov.in/citywx/city_weather_test.php`,
`auth.scheme: none`, and `enricher: nearestStation`.

Same `@type`, different `provider.id` — a different row, a different call plan, no branching
in the adapter.

### ⑤ Enrich, map, call

```
nearestStation:  SELECT * FROM find_nearby_stations(19.9975, 73.7898, 5) ORDER BY distance_km
                 → _local = { stationId: "42403", stationName: "Nashik",
                              district: "Nashik", state: "Maharashtra", distanceKm: 4.1 }
```

Stations are tried in order of proximity, up to `maxStationAttempts`, so a station in the
table with no IMD data behind it does not fail the request.

```
GET https://city.imd.gov.in/citywx/city_weather_test.php?id=42403
timeout 15000ms   no retry   no credential
```

```json
{ "Station_Code": "42403", "Station_Name": "Nashik",
  "Past_24_hrs_Rainfall": "11.2",
  "Today_Min_temp": "22.4", "Today_Max_temp": "30.1",
  "Relative_Humidity_at_0830": "88", "Relative_Humidity_at_1730": "61",
  "Todays_Forecast": "Generally cloudy sky with moderate rain",
  "Day_2_Min_temp": "22.8", "Day_2_Max_Temp": "29.6" }
```

### ⑥ `on_select`

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
      { "parameter": "Rainfall",    "aggregation": "Total",   "unit": "mm",  "value": 11.2 },
      { "parameter": "Temperature", "aggregation": "Minimum", "unit": "Cel", "value": 22.4 },
      { "parameter": "Temperature", "aggregation": "Maximum", "unit": "Cel", "value": 30.1 },
      { "parameter": "Humidity",    "aggregation": "Minimum", "unit": "%",   "value": 61 },
      { "parameter": "Humidity",    "aggregation": "Maximum", "unit": "%",   "value": 88 }
    ]
  }
}
```

The upstream concatenates units into the value (`"28 °C"`). The `responseMapping` splits
them back out — `value` is a number and `unit` is a UCUM code, because that is what the pack
requires and what a consumer can compute with.

---

## Use case 3 — Mandi price (`agmarknet`)

*"आज सोयाबीनचा भाव काय आहे?"* — what is soybean selling for today? Device location
`[70.458, 21.522]`.

### What must be in the registry

| # | entity | key | the part that matters here |
|---|---|---|---|
| 1 | `Capability` | `openagrinet:MandiPrice` | `schemaUrl` → the `MandiPrice/v0.1` pack |
| 2 | `Provider` | `agmarknet` | `auth.scheme: apiKeyQuery` · `paramName: token` |
| 3 | `ProviderCapability` | `agmarknet\|openagrinet:MandiPrice` | `GET /v1/fetch-agmarknet-vistaar-location` |

```json
{ "Provider": {
  "providerId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "baseUrl": "https://api.agmarknet.gov.in",
  "status": "active",
  "auth": { "scheme": "apiKeyQuery",
            "paramName": "token",
            "secrets": { "token": "env://MANDI_TOKEN" } }
} }
```

```json
{ "ProviderCapability": {
  "bindingKey": "agmarknet|openagrinet:MandiPrice",
  "providerId": "agmarknet",
  "capabilityCode": "openagrinet:MandiPrice",
  "method": "GET",
  "path": "/v1/fetch-agmarknet-vistaar-location",
  "enricher": { "name": "marketAndCommodityCodes" },
  "requestMapping":  "mappings/agmarknet/select.request.jsonata",
  "responseMapping": "mappings/agmarknet/select.response.jsonata",
  "timeoutMs": 20000,
  "retryMax": 2,
  "status": "active"
} }
```

`apiKeyQuery` is the one scheme that puts a credential in the URL. The **credential-implies-TLS
clause** in [§3.1](registry.md#31-provider) is what stops
that URL being built over plaintext — the record would be rejected at write time.

### ① Resolve meaning

Commodity: soybean. Date: today. Location: the device's point.

### ② `discover`

```json
"filters": {
  "type": "jsonpath",
  "expression": "$.catalogs[*] ? (@.resourceAttributes.\"@type\" == \"openagrinet:MandiPrice\")"
}
```

with the same `S_DWITHIN` spatial constraint on the provider's service area.

### ③ `select`

```json
"resources": [{
  "id": "res:agmarknet:price",
  "quantity": 1,
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/MandiPrice/v0.1/context.jsonld",
    "@type": "openagrinet:MandiPrice",
    "commodity": { "name": "Soybean" },
    "location": { "type": "Point", "coordinates": [70.458, 21.522] },
    "validity": { "startsAt": "2026-08-26", "endsAt": "2026-08-26" }
  }
}],
"offer": { "id": "offer:agmarknet:open-data",
           "resourceIds": ["res:agmarknet:price"],
           "provider": { "id": "agmarknet",
                         "descriptor": { "code": "AGMK-01", "name": "Agmarknet Vistaar" } } }
```

### ④ Resolve

```
bindingKey = "agmarknet|openagrinet:MandiPrice"
```

Two reads, same shape as [use case 1](#use-case-1--weather-point-forecast-mausamgram) —
only the two filter values change.

```http
POST /api/v1/ProviderCapability/search
```
```json
{ "filters": { "bindingKey": { "eq": "agmarknet|openagrinet:MandiPrice" },
               "status":     { "eq": "active" } } }
```
```http
POST /api/v1/Provider/search
```
```json
{ "filters": { "providerId": { "eq": "agmarknet" },
               "status":     { "eq": "active" } } }
```

Together they give `GET https://api.agmarknet.gov.in/v1/fetch-agmarknet-vistaar-location`,
`auth.scheme: apiKeyQuery` with `paramName: token`, and
`enricher: marketAndCommodityCodes`.

### ⑤ Enrich, map, call

```
marketAndCommodityCodes:  "Soybean" → _local = { commodityId: 1 }
```

Agmarknet's commodity ids are a **private namespace** — nothing in the Beckn body carries
one, and no expression can derive one from the name. That is the second legitimate reason
for an enricher.

```
GET https://api.agmarknet.gov.in/v1/fetch-agmarknet-vistaar-location
    ?commodity_id=1&date=26-08-2026&lat=21.522&long=70.458&token=<resolved from env://MANDI_TOKEN>
timeout 20000ms   retries 2
```

One `date`, not a range — the location endpoint takes a single day.

```json
[ { "market": "Gondal", "commodity": "Soybean", "variety": "Yellow",
    "min_price": "4200", "max_price": "4850", "modal_price": "4600",
    "arrival_date": "26-08-2026", "district": "Rajkot", "state": "Gujarat" } ]
```

### ⑥ `on_select`

```json
{
  "id": "res:agmarknet:price:gondal:soybean:2026-08-26",
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/MandiPrice/v0.1/context.jsonld",
    "@type": "openagrinet:MandiPrice",
    "informationMode": "Direct",
    "subjectCategories": ["Market"],
    "source": { "sourceId": "agmarknet", "sourceName": "Agmarknet Vistaar" },
    "market": { "name": "Gondal", "district": "Rajkot", "state": "Gujarat" },
    "commodity": { "name": "Soybean", "variety": "Yellow" },
    "location": { "type": "Point", "coordinates": [70.458, 21.522] },
    "validity": { "startsAt": "2026-08-26T00:00:00Z", "endsAt": "2026-08-26T23:59:59Z" },
    "generatedAt": "2026-08-26T09:30:12.774Z",
    "prices": [
      { "priceType": "Minimum", "unit": "INR/QUINTAL", "value": 4200 },
      { "priceType": "Maximum", "unit": "INR/QUINTAL", "value": 4850 },
      { "priceType": "Modal",   "unit": "INR/QUINTAL", "value": 4600 }
    ]
  }
}
```

---

## Use case 4 — Scheme information (`hasura-content`)

*"PM-Kisan साठी मी पात्र आहे का?"* — am I eligible for PM-Kisan? A **content** question, not
a transaction: nothing is being applied for, and no farmer identity is sent.

### What must be in the registry

| # | entity | key | the part that matters here |
|---|---|---|---|
| 1 | `Capability` | `openagrinet:KnowledgeResource` | `schemaUrl` → the `KnowledgeResource/v0.1` pack |
| 2 | `Provider` | `hasura-content` | `auth.scheme: apiKeyHeader` · `paramName: x-hasura-admin-secret` |
| 3 | `ProviderCapability` | `hasura-content\|openagrinet:KnowledgeResource` | `POST /v1/graphql` |

```json
{ "ProviderCapability": {
  "bindingKey": "hasura-content|openagrinet:KnowledgeResource",
  "providerId": "hasura-content",
  "capabilityCode": "openagrinet:KnowledgeResource",
  "method": "POST",
  "path": "/v1/graphql",
  "enricher": { "name": "knowledgeQueryParams" },
  "requestMapping":  "mappings/hasura-content/select.request.jsonata",
  "responseMapping": "mappings/hasura-content/select.response.jsonata",
  "timeoutMs": 15000,
  "retryMax": 0,
  "status": "active"
} }
```

**GraphQL needs nothing new in the schema.** It is `POST` to one path with the query in a
body the `requestMapping` builds — which is why
[§3.3](registry.md#33-providercapability) carries no transport discriminator.

**The query is built with GraphQL *variables*, not string concatenation.** The
`requestMapping` emits `{query, variables}` with the filter values in `variables`; nothing
from the network is interpolated into the query text. Interpolating it is a live injection
path, and it is the reason this is stated here rather than left to the mapping author.

### ② `discover` — the filter that separates the two Advisory categories

```
$.catalogs[*] ? (@.resourceAttributes."@type" == "openagrinet:KnowledgeResource"
              && @.resourceAttributes.subjectCategories[*] == "Scheme")
```

`subjectCategories` is a **required** enum on the shared `AgricultureResource` field set —
`Crop` `Livestock` `Weather` `Market` `Scheme` `Knowledge`. It is a property of the
**published resource**, so the discovery service answers *who serves schemes* from its
catalog. The registry never has to.

### ③ `select`

```json
"resources": [{
  "id": "res:hasura-content:scheme",
  "quantity": 1,
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/KnowledgeResource/v0.1/context.jsonld",
    "@type": "openagrinet:KnowledgeResource",
    "subjectCategories": ["Scheme"],
    "query": "PM-Kisan eligibility",
    "language": "mr"
  }
}],
"offer": { "id": "offer:hasura-content:content",
           "resourceIds": ["res:hasura-content:scheme"],
           "provider": { "id": "hasura-content",
                         "descriptor": { "code": "VSTR-CNT-01", "name": "Vistaar Knowledge Content" } } }
```

### ④ Resolve

```
bindingKey = "hasura-content|openagrinet:KnowledgeResource"
```

Two reads, same shape as [use case 1](#use-case-1--weather-point-forecast-mausamgram) —
only the two filter values change.

```http
POST /api/v1/ProviderCapability/search
```
```json
{ "filters": { "bindingKey": { "eq": "hasura-content|openagrinet:KnowledgeResource" },
               "status":     { "eq": "active" } } }
```
```http
POST /api/v1/Provider/search
```
```json
{ "filters": { "providerId": { "eq": "hasura-content" },
               "status":     { "eq": "active" } } }
```

Together they give `POST https://content.internal/v1/graphql`,
`auth.scheme: apiKeyHeader` with `paramName: x-hasura-admin-secret`, and
`enricher: knowledgeQueryParams`.

### ⑤ Enrich, map, call

```
knowledgeQueryParams:  → _local = { usecase: "schemes-agri", schemeId: "pm-kisan", language: "mr", limit: 5 }
```

```
POST https://content.internal/v1/graphql
x-hasura-admin-secret: <resolved from env://HASURA_GRAPHQL_ADMIN_SECRET>
timeout 15000ms   no retry
```
```json
{ "query": "query Content($usecase: String!, $schemeId: String!, $limit: Int!) { Content(where: {usecase: {_ilike: $usecase}, scheme_id: {_ilike: $schemeId}}, limit: $limit) { content_id title description url language scheme_intro scheme_benefits scheme_eligibility scheme_application publisher } }",
  "variables": { "usecase": "schemes-agri", "schemeId": "pm-kisan", "limit": 5 } }
```

```json
{ "data": { "Content": [ {
  "content_id": "sch-pmkisan-001",
  "title": "PM-Kisan Samman Nidhi",
  "description": "Income support of ₹6,000 per year to eligible landholding farmer families.",
  "url": "https://vistaar.gov.in/schemes/pm-kisan",
  "language": "mr",
  "scheme_eligibility": "All landholding farmer families, subject to exclusion criteria.",
  "scheme_application": "Apply at a CSC or on the PM-Kisan portal with Aadhaar and land records.",
  "publisher": "Ministry of Agriculture & Farmers Welfare"
} ] } }
```

### ⑥ `on_select`

```json
{
  "id": "res:hasura-content:sch-pmkisan-001",
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/KnowledgeResource/v0.1/context.jsonld",
    "@type": "openagrinet:KnowledgeResource",
    "informationMode": "Direct",
    "knowledgeType": "SchemeInformation",
    "subjectCategories": ["Scheme"],
    "source": { "sourceId": "hasura-content", "sourceName": "Vistaar Knowledge Content" },
    "title": "PM-Kisan Samman Nidhi",
    "summary": "Income support of ₹6,000 per year to eligible landholding farmer families.",
    "language": "mr",
    "url": "https://vistaar.gov.in/schemes/pm-kisan",
    "publisher": "Ministry of Agriculture & Farmers Welfare",
    "generatedAt": "2026-08-26T09:41:02.118Z"
  }
}
```

---

## Use case 5 — Crop & pest advisory (`oan-vector`)

*"माझ्या कापसाच्या पिकावर पांढरी माशी आहे, काय करू?"* — whitefly on my cotton, what do I do?

### What must be in the registry

| # | entity | key | the part that matters here |
|---|---|---|---|
| 1 | `Capability` | `openagrinet:KnowledgeResource` | **already seeded by use case 4** — same outcome type |
| 2 | `Provider` | `oan-vector` | `baseUrl` `http://3.6.146.174:8882` · `auth.scheme: none` |
| 3 | `ProviderCapability` | `oan-vector\|openagrinet:KnowledgeResource` | `POST /indexes/oan-index/search` |

```json
{ "ProviderCapability": {
  "bindingKey": "oan-vector|openagrinet:KnowledgeResource",
  "providerId": "oan-vector",
  "capabilityCode": "openagrinet:KnowledgeResource",
  "method": "POST",
  "path": "/indexes/oan-index/search",
  "enricher": { "name": "knowledgeQueryParams" },
  "requestMapping":  "mappings/oan-vector/select.request.jsonata",
  "responseMapping": "mappings/oan-vector/select.response.jsonata",
  "timeoutMs": 15000,
  "status": "active"
} }
```

> **`oan-vector` is a bare IP over plain HTTP.** That is legal *only* because
> `auth.scheme: "none"` — no secret is leaked by it. The schema decides the part about
> secrets; it does not claim an unauthenticated internal service on a routable IP is good.
> Moving it behind TLS with a real hostname is onboarding work, not a schema change, and
> should happen before v1 carries real traffic.

**Both `KnowledgeResource` bindings name the same enricher, `knowledgeQueryParams`.** Their
*request mappings* do not: one shapes a GraphQL `variables` block, the other a vector-search
body. The enricher extracts the query intent; the mapping shapes it for one upstream.

### ② `discover` — same type, different category

```
$.catalogs[*] ? (@.resourceAttributes."@type" == "openagrinet:KnowledgeResource"
              && @.resourceAttributes.subjectCategories[*] == "Crop")
```

Narrow further with `agricultureSubjects[].subjectType` ∈ `{Pest, Disease}` when the
question is specifically about a pest.

### ③ `select`

```json
"resources": [{
  "id": "res:oan-vector:advisory",
  "quantity": 1,
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/KnowledgeResource/v0.1/context.jsonld",
    "@type": "openagrinet:KnowledgeResource",
    "subjectCategories": ["Crop"],
    "query": "whitefly management in cotton",
    "language": "mr"
  }
}],
"offer": { "id": "offer:oan-vector:index",
           "resourceIds": ["res:oan-vector:advisory"],
           "provider": { "id": "oan-vector",
                         "descriptor": { "code": "OAN-VEC-01", "name": "OAN Vector Index" } } }
```

### ④ Resolve

```
bindingKey = "oan-vector|openagrinet:KnowledgeResource"
```

Two reads, same shape as [use case 1](#use-case-1--weather-point-forecast-mausamgram) —
only the two filter values change.

```http
POST /api/v1/ProviderCapability/search
```
```json
{ "filters": { "bindingKey": { "eq": "oan-vector|openagrinet:KnowledgeResource" },
               "status":     { "eq": "active" } } }
```
```http
POST /api/v1/Provider/search
```
```json
{ "filters": { "providerId": { "eq": "oan-vector" },
               "status":     { "eq": "active" } } }
```

Together they give `POST http://3.6.146.174:8882/indexes/oan-index/search`,
`auth.scheme: none`, and `enricher: knowledgeQueryParams`.

Same `capabilityCode` as use case 4, different `providerId` — a different row. This is the
whole point of a two-segment key: the pair is the identity.

### ⑤ Enrich, map, call

```
knowledgeQueryParams:  → _local = { q: "whitefly management in cotton", limit: 5, language: "mr" }
```

```
POST http://3.6.146.174:8882/indexes/oan-index/search
timeout 15000ms   no retry   no credential
```
```json
{ "q": "whitefly management in cotton",
  "limit": 5,
  "filter": "type:document",
  "searchMethod": "HYBRID",
  "hybridParameters": { "retrievalMethod": "disjunction", "rankingMethod": "rrf",
                        "alpha": 0.5, "rrfK": 60 } }
```

The tuning block is a **constant in the request mapping, not a caller-supplied value.**
Exposing `alpha` and `rrfK` to the network lets a caller change what the network means by
relevance, which is not theirs to set.

```json
{ "hits": [ {
  "_id": "icar-cotton-whitefly-01",
  "title": "Integrated management of whitefly in cotton",
  "content": "Install yellow sticky traps at 12 per acre. Spray neem oil 1500 ppm at 5 ml/l...",
  "source": "ICAR-CICR", "language": "mr",
  "url": "https://vistaar.gov.in/advisory/cotton-whitefly",
  "_score": 0.8123 } ] }
```

### ⑥ `on_select`

```json
{
  "id": "res:oan-vector:icar-cotton-whitefly-01",
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/KnowledgeResource/v0.1/context.jsonld",
    "@type": "openagrinet:KnowledgeResource",
    "informationMode": "Direct",
    "knowledgeType": "Advisory",
    "subjectCategories": ["Crop"],
    "agricultureSubjects": [{ "subjectType": "Pest", "subjectName": "Whitefly" },
                            { "subjectType": "Crop", "subjectName": "Cotton" }],
    "source": { "sourceId": "oan-vector", "sourceName": "OAN Vector Index" },
    "title": "Integrated management of whitefly in cotton",
    "summary": "Install yellow sticky traps at 12 per acre. Spray neem oil 1500 ppm at 5 ml/l...",
    "language": "mr",
    "url": "https://vistaar.gov.in/advisory/cotton-whitefly",
    "publisher": "ICAR-CICR",
    "generatedAt": "2026-08-26T10:02:47.633Z"
  }
}
```

---

## Two capabilities, five bindings

Use cases 4 and 5 are **one `capabilityCode` served by two providers**, and use cases 1 and
2 are the same. That is the design working, not a shortcut.

| | how many |
|---|---|
| `Capability` records | 3 — `WeatherObservation`, `MandiPrice`, `KnowledgeResource` |
| `Provider` records | 5 |
| `ProviderCapability` records | 5 — one per (provider, capability) pair actually served |

`capabilityCode` is the **outcome type**: what the caller gets back. Schemes and crop
advisory both come back as a `KnowledgeResource`; they are told apart by
`subjectCategories`, which is a property of the **published resource**. Splitting the
capability so the registry could answer *who serves schemes* would break the one rule
holding this vocabulary together — and the registry never needs to answer it, because
`/discover` already did, from the catalog.

## Three things true across all five

**No filter constrains `informationMode`, deliberately.** Both modes are publishable — an
`OnDemand` advertisement and a `Direct` resource published straight to discovery are both
honest answers to *who can tell me about rain*. Filtering to `OnDemand` would hide data
already in the catalog; filtering to `Direct` would hide every provider that answers on
invocation. Add the constraint only when the caller genuinely wants one or the other, and
note the experience layer must branch on it either way: `OnDemand` means a second call,
`Direct` means it already has the answer.

**Timeout and retry are per binding, and are registry columns.** IMD gets 30 s and 3
retries; Hasura gets 15 s and none. Those are properties of the upstream, changed by an
operator, not constants compiled into a service class.

**`aggregation` is not a governed field.** The `WeatherObservation` pack's `parameters` item
declares `required: [parameter, value, unit]` without closing the object, so a private
qualifier validates and means nothing to any other participant. **There is no conformant way
to say *tomorrow's high is 30.6 and low is 22.1*** — and every Indian weather upstream
reports `tmin`/`tmax`. This is a real gap in the network vocabulary, it lands on Weather
which is a v1 category, and it is tracked in
[registry.md § Known gaps](registry.md#known-gaps-for-v1).
