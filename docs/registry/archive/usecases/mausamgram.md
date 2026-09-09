# mausamgram — gram-panchayat weather

**Shape: simple.** One Beckn action, one upstream call, no session state. This is the
baseline every other use case is a variation on, and the only one traced end to end.

*[Use cases](README.md) · [Registry schema](../02-registry-schema.md) · [Overview](../01-overview.md) · [docs home](../README.md)*

| | |
|---|---|
| Provider | `mausamgram` — IMD Mausamgram NWP |
| Capability | `openagrinet:WeatherObservation` |
| Action | `select` |
| Auth | HTTP Basic, two `env://` pointers |
| Enricher | `pointFromIntent` — lifts lat/lon out of the intent |
| Mappings | `registry/mappings/mausamgram/select.{request,response}.jsonata` |

---

## The registry records

Two query params, one call. The enricher is the trivial case — it only lifts lat/lon out
of the Beckn intent — which is exactly why this is the right binding to read first.

```json
{
  "Provider": {
    "providerId": "mausamgram",
    "name": "IMD Mausamgram NWP",
    "baseUrl": "https://mausamgram.imd.gov.in/nwpapi",
    "status": "active",
    "auth": {
      "scheme": "basic",
      "secrets": {
        "username": "env://MAUSAMGRAM_USER",
        "password": "env://MAUSAMGRAM_X_API_KEY"
      }
    }
  }
}
```

```json
{
  "ProviderCapability": {
    "bindingKey": "mausamgram|openagrinet:WeatherObservation|select",
    "providerId": "mausamgram",
    "capabilityCode": "openagrinet:WeatherObservation",
    "action": "select",
    "method": "GET",
    "path": "/get-daily",
    "requestMapping": "mappings/mausamgram/select.request.jsonata",
    "responseMapping": "mappings/mausamgram/select.response.jsonata",
    "enricher": "pointFromIntent",
    "timeoutMs": 30000,
    "retryMax": 3,
    "status": "active"
  }
}
```

---

## Execution, step by step


Nothing below is illustration. The JSONata ran through ONIX's own `jsonata-go`, and every
payload was validated against the authoritative `beckn.yaml` and network-specs v0.1
(see [Conformance](../reference/conformance.md)).

**Topology A**, synchronous. Editable version: `docs/diagrams/request-flow.excalidraw`.

```
  FARMER          EXPERIENCE LAYER          ONIX ADAPTER         REGISTRY        IMD
    │                    │                       │                  │            │
    │ "will it rain?"    │                       │                  │            │
    ├───────────────────▶│                       │                  │            │
    │              A.1 resolve meaning           │                  │            │
    │                    │                       │                  │            │
    │                    │  A.2 discover         │                  │            │
    │                    ├──────────────────────▶│                  │            │
    │                    │       (adapter queries the discovery service's        │
    │                    │        indexed catalog store — no provider touched)   │
    │                    │◀──────────────────────┤                  │            │
    │                    │  A.3 on_discover: catalogs, IMMEDIATELY  │            │
    │                    │      provider.id + capability @type      │            │
    │                    │      an ADVERTISEMENT — no value in it   │            │
    │                    │                       │                  │            │
    │                    │  A.4 select           │                  │            │
    │                    ├──────────────────────▶│                  │            │
    │                    │                       │ A.5 resolve      │            │
    │                    │                       ├─────────────────▶│            │
    │                    │                       │◀─────────────────┤            │
    │                    │                       │  call plan + auth│            │
    │                    │                       │                  │            │
    │                    │                       │ A.6 enrich, map request,      │
    │                    │                       │     authenticate, call        │
    │                    │                       ├──────────────────────────────▶│
    │                    │                       │◀──────────────────────────────┤
    │                    │                       │      IMD's native JSON        │
    │                    │                       │                  │            │
    │                    │                       │ A.7 map response → Beckn v2   │
    │                    │◀──────────────────────┤                  │            │
    │                    │  on_select: 5 WeatherObservation resources│           │
    │◀───────────────────┤                       │                  │            │
    │  "उद्या १२.४ मिमी पाऊस"  │                       │                  │            │
```

> **The farmer asks:** *"पुढच्या पाच दिवसांत पाऊस पडेल का?"* — will it rain in the next
> five days? Device location: Nashik, `[73.7898, 19.9975]`.

## 1. The experience layer resolves meaning

Before any Beckn call, the utterance becomes concepts:

| from the utterance | concept |
|---|---|
| "पाऊस" | subject area `Weather`, parameter `Rainfall` |
| device GPS | point `[73.7898, 19.9975]` |
| "पुढच्या पाच दिवसांत" | period `2026-08-26 .. 2026-08-30` |

This is the boundary the whole design rests on: **the experience layer owns meaning, the
adapter owns encoding.** The experience layer does not know that IMD calls this
`fcstday1..fcstday5`, and it must not.

## 2. `discover` — the experience layer asks who can answer

```json
{
  "context": {
    "version": "2.0.0", "action": "discover",
    "bapId": "vistaar-app.bharatvistaar.gov.in",
    "bapUri": "https://vistaar-app.bharatvistaar.gov.in/beckn",
    "transactionId": "9f2c1a8e-4b70-4d31-9c55-6f2e0b1d7a44",
    "messageId": "1c0a55d7-8e64-4b19-9a2f-33b7c6e1d905",
    "timestamp": "2026-08-26T06:11:58.004Z"
  },
  "message": {
    "intent": {
      "textSearch": "weather forecast rain next five days",
      "filters": {
        "type": "jsonpath",
        "expression": "$.catalogs[*] ? (@.resourceAttributes.\"@type\" == \"openagrinet:WeatherObservation\" || @.resourceAttributes.\"@type\" == \"openagrinet:WeatherAdvisory\")"
      },
      "spatial": [{
        "op": "S_DWITHIN",
        "targets": "$['provider']['availableAt'][*]['geo']",
        "geometry": { "type": "Point", "coordinates": [73.7898, 19.9975] },
        "distanceMeters": 250000, "quantifier": "ANY"
      }]
    }
  }
}
```

The adapter forwards this to the discovery service, which evaluates it against its
**indexed catalog store**. No provider is contacted.

**The filter is written in PostgreSQL SQL/JSON path, not RFC 9535.** That is not a style
choice and it is the one thing on this page most likely to be copied wrongly. `beckn.yaml`
describes `filters.expression` as *"JSONPath filter expressions per RFC 9535"* and its own
`Intent` example uses the Goessner subscript form `$.catalogs[?(@.rating.value >= 4.0)]` —
but a discovery service backed by Postgres executes the expression with the `@?` operator,
which takes a different grammar. Three differences bite:

| | RFC 9535 (the spec's example) | SQL/JSON path (what executes) |
|---|---|---|
| filter | `$.catalogs[?(...)]` | `$.catalogs[*] ? (...)` |
| quoting | `['@type']` or `'literal'` | `."@type"` and `"literal"` — double quotes |
| membership | `anyof [a, b]` | no such operator; expand to `a \|\| b` |

An earlier version of this example used all three of the left-hand forms. None of them are
caught by validation — `filters.expression` is declared `type: string`, so a wrong dialect
is schema-valid and the envelope on this page passes `beckn.yaml` either way. `anyof` in
particular is a hard Postgres syntax error, so it fails at query time, per request, on the
provider side of the network. Write the right-hand forms.

Rooting matters too: the expression is evaluated against a document whose top level is
`catalogs`, so a filter starting `$[?(...)]` addresses the wrong node and matches nothing
— and a filter that matches nothing is indistinguishable from an honest empty result.

Two things to notice, because they are exactly why `select` has to exist — see
[Which action is the second call?](../01-overview.md#which-action-is-the-second-call):

- The point had to be smuggled in as a **spatial constraint on the provider's service
  area** — `S_DWITHIN` against `availableAt[*].geo` — because `Intent` has no field for
  "the location I am asking about".
- There is **nowhere at all** to put the validity window `2026-08-26 .. 2026-08-30`.

## 3. `on_discover` — catalogs come back immediately

```json
{
  "context": { "version": "2.0.0", "action": "on_discover",
    "transactionId": "9f2c1a8e-4b70-4d31-9c55-6f2e0b1d7a44",
    "bppId": "bharatvistaar.gov.in", "bppUri": "https://bharatvistaar.gov.in/beckn" },
  "message": {
    "catalogs": [{
      "id": "catalog:bharatvistaar:weather:2026-08",
      "descriptor": {
        "code": "BV_WEATHER", "name": "Bharat Vistaar weather services",
        "longDesc": "This catalog advertises capabilities only. Requested coordinates, validity windows and measured values are returned after capability invocation."
      },
      "provider": {
        "id": "mausamgram",
        "descriptor": { "code": "IMD-NWP-01", "name": "IMD Mausamgram NWP" },
        "availableAt": [{ "geo": { "type": "Point", "coordinates": [77.209, 28.6139] } }]
      },
      "resources": [{
        "id": "capability:mausamgram:point-forecast",
        "descriptor": { "code": "WEATHER_OBSERVATION", "name": "Five-day point forecast" },
        "resourceAttributes": {
          "@context": "https://schemas.openagrinet.global/schema/AgricultureCapability/v0.1/context.jsonld",
          "@type": "openagrinet:WeatherObservation",
          "informationMode": "OnDemand",
          "subjectCategories": ["Weather"],
          "languages": ["en","hi","mr"],
          "coverageAreas": [{ "codeScheme": "ISO-3166-1", "areaCode": "IN",
                              "areaLevel": "Country" }]
        }
      }]
    }]
  }
}
```

Read that `longDesc` — it is network-specs' own wording, and it is the entire answer to
*"is hop ① enough?"*

**What the experience layer takes from this, and how it decides:**

| read | value | used as |
|---|---|---|
| `catalogs[0].provider.id` | `mausamgram` | first half of the `bindingKey` |
| `…resources[0].resourceAttributes.@type` | the resource type | second half |
| `context.action` | `select` | third |

**`informationMode` is what says "keep going".** The advertisement carries `OnDemand`:
the provider *can* supply this, but a call is required to get it. So the resource has a
governed `@type`, a descriptor and subject metadata — and **no observed value, no
coordinates, no validity window.** The experience layer reads one field and builds a
`select`.

Had it read `Direct`, it would have stopped: a published knowledge article whose
`contentUri` is already in the catalog needs no second call. That is a one-field test,
not a heuristic about which properties happen to be absent.

## 4. `select` — name that provider, ask for the data

```json
{
  "context": {
    "version": "2.0.0", "action": "select",
    "bapId": "vistaar-app.bharatvistaar.gov.in",
    "bppId": "bharatvistaar.gov.in",
    "transactionId": "9f2c1a8e-4b70-4d31-9c55-6f2e0b1d7a44",
    "messageId": "77b1e4d2-9a03-4f18-8c62-1e5d3f907ab2",
    "timestamp": "2026-08-26T06:12:04.201Z"
  },
  "message": {
    "contract": {
      "descriptor": { "code": "WEATHER_FORECAST_QUERY", "name": "Five-day forecast for Nashik" },
      "commitments": [{
        "status": { "descriptor": { "code": "DRAFT", "name": "Draft" } },
        "resources": [{
          "id": "res:mausamgram:point-forecast",
          "quantity": 1,
          "descriptor": { "code": "POINT_WEATHER_FORECAST", "name": "Point forecast" },
          "resourceAttributes": {
            "@context": "https://schemas.openagrinet.global/schema/AgricultureCapability/v0.1/context.jsonld",
            "@type": "openagrinet:WeatherObservation",
            "location": { "type": "Point", "coordinates": [73.7898, 19.9975] },
            "validity": { "startsAt": "2026-08-26", "endsAt": "2026-08-30" }
          }
        }],
        "offer": {
          "id": "offer:mausamgram:open-data",
          "resourceIds": ["res:mausamgram:point-forecast"],
          "provider": {
            "id": "mausamgram",
            "descriptor": { "code": "IMD-NWP-01", "name": "IMD Mausamgram NWP" }
          }
        }
      }]
    }
  }
}
```

Note what `resourceAttributes` carries that `intent` could not: a **Point** and a
**validity window**. And note `status: DRAFT` with no price on the offer — nothing is
being committed.

**Everything needed to resolve the binding is on the request body:**

```
offer.provider.id               → "mausamgram"
resourceAttributes["@type"]     → "openagrinet:WeatherObservation"
```

The `@type` is the **same string the advertisement carried** — one type spans both calls,
and `informationMode` is the only thing that differs. That is what makes the second half
of the `bindingKey` a straight copy rather than a lookup from advertised type to outcome
type.

No hidden state, no session, no side lookup — which is what makes a stateless
network-layer deployment possible. Two concurrent farmer questions to two different
providers are just two different request bodies.

## 5. Resolve — one exact-match lookup

```
bindingKey = providerId "|" capabilityCode "|" action
           = "mausamgram|openagrinet:WeatherObservation|select"
```

The `ProviderCapability` row that comes back:

```json
{
  "bindingKey": "mausamgram|openagrinet:WeatherObservation|select",
  "providerId": "mausamgram",
  "capabilityCode": "openagrinet:WeatherObservation",
  "action": "select",
  "method": "GET",
  "path": "/get-daily",
  "enricher": "pointFromIntent",
  "requestMapping":  "mappings/mausamgram/select.request.jsonata",
  "responseMapping": "mappings/mausamgram/select.response.jsonata",
  "timeoutMs": 30000,
  "retryMax": 3,
  "status": "active"
}
```

and the `Provider` row:

```json
{
  "providerId": "mausamgram",
  "name": "IMD Mausamgram NWP",
  "baseUrl": "https://mausamgram.imd.gov.in/nwpapi",
  "auth": {
    "scheme": "basic",
    "secrets": { "username": "env://MAUSAMGRAM_USER",
                 "password": "env://MAUSAMGRAM_X_API_KEY" }
  },
  "status": "active"
}
```

**How the plan reaches the later steps without forking ONIX.** `model.StepContext` has
fixed fields and no extensible bag — but it *embeds* `context.Context` and exposes
`WithContext`. So `resolveCapability` stashes the plan with `context.WithValue`, and the
enrich/transform steps read it back out. It then sets the route ONIX will proxy to:

```go
ctx.Route = &model.Route{ TargetType: "url",
                          URL: mustParse(provider.baseUrl + binding.path) }
```

`TargetType: "url"` is the synchronous branch — `stdHandler.go` builds an
`httputil.ReverseProxy` and streams the upstream response back through `ModifyResponse`.

## 6. Enrich, transform the request, authenticate, call

**Enrich.** The binding names `enricher: "pointFromIntent"` — declared in the registry,
implemented in Go:

```
resourceAttributes.location.coordinates  →  _local = { "lat": 19.9975, "lon": 73.7898 }
```

Trivial here, because IMD's NWP API takes a raw point. It is **not** trivial in general,
and that is why the step exists: Agmarknet wants `statecode`, `districtcode`,
`marketcode`, `commoditycode` — integers in its own private namespace, none derivable
from the Beckn body. The registry holds the **name**; code holds the behaviour. Config
that tried to hold the behaviour would become a programming language.

**Transform the request.** The stored `requestMapping`, evaluated by the stock
`jsonata-go` engine over `{ request, _local }`:

```jsonata
{ "lat": $string(_local.lat), "lon": $string(_local.lon) }
```

→

```json
{ "lat": "19.9975", "lon": "73.7898" }
```

`$string()` is not cosmetic — IMD's endpoint rejects unquoted numerics.

**Authenticate and call.** `auth.scheme` is `basic`, so `env://MAUSAMGRAM_USER` and
`env://MAUSAMGRAM_X_API_KEY` are resolved from the process environment at call time and
base64'd into an `Authorization` header:

```
GET https://mausamgram.imd.gov.in/nwpapi/get-daily?lat=19.9975&lon=73.7898
Authorization: Basic <resolved>
timeout 30000ms   retries 3
```

Timeout and retry count are registry columns now, not constants in a service class.

**IMD answers in its native shape** — `fcstday1..fcstday5`:

```json
{
  "location": { "lat": 19.9975, "lon": 73.7898 },
  "fcstday1": {
    "date": "2026-08-26", "rain": 12.4,
    "tmin": 22.1, "tmax": 30.6, "rhmin": 58, "rhmax": 89,
    "wspd": 4.2, "wind": [4.2, "SW"],
    "cloud_message": "Partly cloudy",
    "weather_warning": "Heavy rainfall warning"
  }
}
```

*(`fcstday2..5` elided; all five were produced and all five validate.)*

## 7. Transform the response, and send it back

`responseMapping`, evaluated over `{ request, response, _local }` — `_local` stays in
scope so the resolved point can be carried into the output. Five IMD forecast days become
five typed Beckn resources. First day:

```json
{
  "id": "res:mausamgram:forecast:2026-08-26",
  "quantity": 1,
  "descriptor": { "code": "WEATHER_FORECAST_DAY", "name": "Forecast for 2026-08-26" },
  "resourceAttributes": {
    "@context": "https://schemas.openagrinet.global/schema/WeatherObservation/v0.1/context.jsonld",
    "@type": "openagrinet:WeatherObservation",
    "informationMode": "Direct",
    "observationType": "Forecast",
    "location": { "type": "Point", "coordinates": [73.7898, 19.9975] },
    "validity": { "startsAt": "2026-08-26T00:00:00Z", "endsAt": "2026-08-26T23:59:59Z" },
    "generatedAt": "2026-08-26T06:12:04.201Z",
    "parameters": [
      { "parameter": "Rainfall",    "aggregation": "Total",   "unit": "mm",  "value": 12.4 },
      { "parameter": "Temperature", "aggregation": "Minimum", "unit": "Cel", "value": 22.1 },
      { "parameter": "Temperature", "aggregation": "Maximum", "unit": "Cel", "value": 30.6 },
      { "parameter": "Humidity",    "aggregation": "Minimum", "unit": "%",   "value": 58 },
      { "parameter": "Humidity",    "aggregation": "Maximum", "unit": "%",   "value": 89 },
      { "parameter": "WindSpeed",   "aggregation": "Mean",    "unit": "m/s", "value": 4.2 },
      { "parameter": "Alert",       "aggregation": "Instant", "unit": "1",
        "value": "Heavy rainfall warning" }
    ],
    "source": { "sourceId": "source:mausamgram", "sourceName": "IMD Mausamgram NWP",
                "sourceUri": "https://mausamgram.imd.gov.in" },
    "subjectCategories": ["Weather"],
    "coverageAreas": [{ "codeScheme": "ISO-3166-1", "areaCode": "IN",
                        "areaLevel": "Country" }],
    "languages": ["en"]
  }
}
```

The offer ties the five together:

```json
{
  "id": "offer:mausamgram:open-data",
  "descriptor": { "code": "OPEN_DATA", "name": "Open government data, no charge" },
  "provider": { "id": "mausamgram",
                "descriptor": { "code": "IMD-NWP-01", "name": "IMD Mausamgram NWP" } },
  "resourceIds": [
    "res:mausamgram:forecast:2026-08-26", "res:mausamgram:forecast:2026-08-27",
    "res:mausamgram:forecast:2026-08-28", "res:mausamgram:forecast:2026-08-29",
    "res:mausamgram:forecast:2026-08-30"
  ]
}
```

`context.action` is flipped with a merge operator rather than a rebuild, so
`transactionId`, `messageId` and `timestamp` survive unchanged:

```jsonata
"context": request.context ~> |$|{ "action": "on_select" }|
```

`signAck` signs the result, and ONIX streams it back on the still-open connection.

> **Three JSONata details that cost real debugging**, for whoever writes the next mapping:
>
> - **Pre-bind the root.** Inside a predicate `[...]` the evaluation context changes, so
>   a bare `response` does not resolve. Bind `$resp := response` first, then `$filter`
>   with an explicit lambda. The naive form silently returns **zero** resources — it does
>   not error.
> - **Wrap arrays in `[ ]`.** A one-element `$map` result singleton-flattens to a bare
>   object, which turns `resources` into an object and fails `SelectAction`. The outer
>   brackets force an array even at length 1.
> - **Emit camelCase** (`bppId`, `bapUri`) per the authoritative spec. ONIX itself accepts
>   either spelling, but the spec has one.

**On failure it is NACK only** — no partial results, no silent empties. Upstream 500,
timeout, or a non-conformant mapping result all become one `Error` body
(`NackBadRequest`, `NackUnauthorized`, `NackNotFound`, …). See
[Topology B](../01-overview.md#topology-b--adapter-on-both-sides) for the trap in
implementing this.

## What the farmer hears

> *"पुढच्या पाच दिवसांत नाशिकमध्ये पाऊस अपेक्षित आहे. उद्या १२.४ मिमी पाऊस आणि मुसळधार पावसाचा इशारा आहे."*

Rendering is the experience layer's job. It received five typed `WeatherObservation`
resources and chose what to say.

## The two calls, side by side

| | ① discover | ② select |
|---|---|---|
| resolved by | discovery service (CN → DS) | provider node via ONIX (CN → PN) |
| answers | *who* can answer | *what* the answer is |
| touches a provider | no | yes |
| touches the registry | no | yes — one exact-match lookup |
| Beckn slot | `message.catalogs[]` | `message.contract.commitments[]` |
| query params can live in | nowhere — `Intent` is closed | `resourceAttributes` — `Attributes` is open |
| carries live values | **no**, by network-specs' own statement | yes |
| latency | ~20ms, index only | upstream-bound (IMD: 30s timeout, 3 retries) |
