# OpenAgriNet registry — v1

Three things: the **schema**, the **records to store**, and the **execution** from
experience layer to provider.

Scoped to the v1 provider set. The imported BV pages
([overview](01-overview.md) · [schema](02-registry-schema.md) ·
[use cases](usecases/README.md)) carry the full reasoning, eleven bindings and four call
shapes; this page carries what v1 needs. Where they disagree, they are describing BV and
this is describing us.

| v1 category | `@type` | Providers | Bindings |
|---|---|---|---|
| Weather | `openagrinet:WeatherObservation` | `mausamgram`, `imd-city-weather` | 2 |
| Mandi prices | `openagrinet:MandiPriceObservation` | `agmarknet` | 1 |
| Advisory — Schemes | `openagrinet:KnowledgeResource` | `hasura-content` | 1 |
| Advisory — Crop & Pest | `openagrinet:KnowledgeResource` | `oan-vector` | 1 |

Five bindings, five providers, three capabilities. **Every one is a single upstream call.**
`steps[]`, `sessionGate` and `sessionGrant` exist in the schema and no v1 binding uses
them — they are for PM-Kisan and PMFBY, which are not in v1.

---

## 1. Schema

Sunbird RC generates storage and REST from JSON Schema. Three entities, joined by a
denormalised key:

```
Provider.providerId ───────┐
                           ├──▶ bindingKey ──▶ the call plan
Capability.capabilityCode ─┘
```

Every record sets `additionalProperties: false`. On the wire each is wrapped —
`{"Provider": {...}}` — because each schema's top level is `required: ["Provider"]`.

### `Provider` — who they are, how we authenticate to them

`uniqueIndexFields: [providerId]` · `indexFields: [status]`

| field | type | constraint | req |
|---|---|---|---|
| `providerId` | string | `^[a-z0-9][a-z0-9._:-]{2,63}$` — the Beckn `provider.id` | ✓ |
| `name` | string | `minLength: 1` | ✓ |
| `baseUrl` | string | `^https?://[^/].*[^/]$`, no trailing slash. **TLS required if the record carries a credential** | ✓ |
| `status` | string | `active` \| `inactive` | ✓ |
| `auth` | object | → `Auth` | ✓ |
| `authProfiles` | object | keys `^[a-z][a-zA-Z0-9]*$`, values → `Auth` | |

Per-call paths are **not** here — they belong to the binding, because one provider serves
several. A common prefix is fine (`mausamgram` is `…/nwpapi`).

### `Auth` — how the adapter authenticates upstream

Not Beckn signing; ONIX does that separately.

| field | constraint |
|---|---|
| `scheme` | `none` \| `apiKeyQuery` \| `apiKeyHeader` \| `basic` \| `bearer` \| `loginToken` \| `encryptedEnvelope` |
| `paramName` | query-param or header name |
| `secrets` | every value `^env://[A-Z][A-Z0-9_]*$` |
| `extraHeaders` | same pointer form |
| `login` | `{path, tokenPath, ttlSeconds (30–86400), method, bodyMapping}` |

Required-by-scheme, as `if`/`then` rather than prose:

| `scheme` | then required |
|---|---|
| `apiKeyQuery` `apiKeyHeader` `bearer` | `paramName`, `secrets` |
| `basic` | `secrets` |
| `loginToken` | `paramName`, `secrets`, `login` |
| `encryptedEnvelope` | `secrets`, `envelope` |
| `none` | **must not** carry `secrets` |

**Two rules that matter more than the rest.**

*Secrets are never stored.* `secrets` holds `env://MAUSAMGRAM_X_API_KEY` — a pointer the
adapter resolves at call time. The pointer form is enforced by pattern at write time, so a
pasted key cannot reach the database in the first place.

*A credential implies TLS.* Every scheme except `none` requires `secrets`, so
`scheme != "none"` and "this record holds a credential" are the same statement. Without a
clause relating the two, an `apiKeyHeader` on a plaintext base URL is a well-formed record
that puts a live secret on the wire in clear.

```json
{ "if":   { "properties": { "auth": { "properties": { "scheme": { "not": { "const": "none" } } },
                                      "required": ["scheme"] } },
            "required": ["auth"] },
  "then": { "properties": { "baseUrl": { "pattern": "^https://[^/].*[^/]$" } } } }
```

Plaintext stays legal for `scheme: none` — `oan-vector` needs it until it moves behind TLS.

### `Capability` — network vocabulary, provider-independent

`uniqueIndexFields: [capabilityCode]` · `indexFields: [status]`

| field | type | constraint | req |
|---|---|---|---|
| `capabilityCode` | string | `^openagrinet:[A-Z][A-Za-z0-9]*$`, and `not` `AgricultureCapability` / `AgricultureResource` | ✓ |
| `name` | string | `minLength: 1` | ✓ |
| `schemaUrl` | string | `^https://(?!.*/refs/heads/)(?!.*/(main\|master\|develop)/).+/attributes\.yaml$` | ✓ |
| `status` | string | `active` \| `deprecated` | ✓ |
| `baseTypes` | array\<string\> | items `^openagrinet:`, `uniqueItems` | |

`capabilityCode` **is the outcome type** — what the caller gets back. The two negative
lookaheads on `schemaUrl` reject a branch ref: a capability pinned to `main` means the
contract you validated against last week is not the one you validate against today.

Nothing names a provider here. `AgricultureResource` is the shared field set every pack
composes with `allOf` — it identifies nothing, so it cannot be a `capabilityCode`; it goes
on `baseTypes[]`, where a broad request can still fan out to it.

### `ProviderCapability` — the call plan

`uniqueIndexFields: [bindingKey]` · `indexFields: [providerId, capabilityCode, status]`

| field | type | constraint | req |
|---|---|---|---|
| `bindingKey` | string | `<providerId>\|<capabilityCode>\|<action>` | ✓ |
| `providerId` | string | must match segment 1 | ✓ |
| `capabilityCode` | string | must match segment 2 | ✓ |
| `responseMapping` | string | `^mappings/(?!.*\.\.)[a-z0-9][a-z0-9._/-]*\.jsonata$` | ✓ |
| `status` | string | `active` \| `inactive` | ✓ |
| `method` `path` `requestMapping` | | **single-call shape** — all three, and no `steps` | |
| `steps` | array | **multi-step shape** — 2–6, and none of the three above | |
| `enricher` | string \| object | `^[a-z][a-zA-Z0-9]*$`, or `{name, config, secrets}` | |
| `timeoutMs` | integer | 1000–120000, default 15000 | |
| `retryMax` | integer | 0–5, default 0 | |

The single/multi split is a `oneOf`, not documentation — a record is one shape or the
other, never half of each. **All five v1 bindings are single-call.**

```
"pattern": "^[a-z0-9][a-z0-9._:-]{2,63}\\|openagrinet:[A-Z][A-Za-z0-9]*\\|(discover|select|init|confirm|status|track|update|cancel|rate|support)$"
"not":     { "pattern": "\\|openagrinet:Agriculture(Capability|Resource)(\\||$)" }
```

**Two integrity rules no JSON Schema can express**, so they run in onboarding and in the
conformance suite: `bindingKey`'s first two segments must agree with `providerId` and
`capabilityCode`, and both must resolve to live records.

**`providerId` and `capabilityCode` are stored** even though `bindingKey` contains them,
because RC indexes and searches whole fields, never a substring: a segment that lives only
inside the key cannot be queried. Both are in `indexFields`, and *list every binding for
this provider* is what onboarding and deactivation need. They earn the duplication.

**`action` is not stored.** BV's schema has it as a required column; OAN v1 drops it. It
was never in `indexFields`, so it answered no query. The adapter *builds* the key from the
incoming request and does one exact-match lookup — it never reads `action` back off the
row. And the enum it declared was already enforced by the key pattern's own alternation,
so it validated nothing the pattern did not. A third copy of a fact that no code reads is
just a third thing that can disagree with the other two.

The **key keeps all three segments**. It has to: `uniqueIndexFields` is a single field, and
`pm-kisan|…|init` and `pm-kisan|…|status` are different call plans for the same provider
and capability — under a two-part key they collide, and whether RC then rejects or silently
overwrites is a property of the deployed build nobody has confirmed. The third segment
removes the question. What went away is the redundant column, not the discriminator; to
display or group by action, split the key.

**Mappings live in files, not in the row.** The Mausamgram response transform is 76 lines
of JSONata; stored in the row it is one string with every newline escaped, unreviewable in
a diff. The pattern pins the directory, rejects `..`, requires `.jsonata`, and allows
lowercase only — a path differing from disk by case resolves on macOS and 404s on Linux.

```
registry/mappings/<provider>/<action>.<request|response>.jsonata
```

---

## 2. Records to store

Thirteen records: 3 `Capability`, 5 `Provider`, 5 `ProviderCapability`. Shown in RC write
form.

> The three `schemaUrl` values below carry a **placeholder host**,
> `https://REPLACE-ME.invalid/`, because the network-specs raw host and repo path were
> not in the material this page was written from. Everything after it is right: the
> commit segment `c56ee68` is what pins the contract, and the schema's two negative
> lookaheads reject a branch ref, so `main` cannot be substituted for it.
>
> `.invalid` is reserved by RFC 2606 and never resolves — a record seeded as printed
> fails at first fetch rather than silently validating against the wrong pack. Fill the
> host in from the pack index before seeding.

### Capabilities

```json
{ "Capability": {
  "capabilityCode": "openagrinet:WeatherObservation",
  "name": "Weather Observation and Forecast",
  "baseTypes": ["openagrinet:AgricultureResource"],
  "schemaUrl": "https://REPLACE-ME.invalid/network-specs/c56ee68/schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active"
} }
```

```json
{ "Capability": {
  "capabilityCode": "openagrinet:MandiPriceObservation",
  "name": "Mandi Price Observation",
  "baseTypes": ["openagrinet:AgricultureResource"],
  "schemaUrl": "https://REPLACE-ME.invalid/network-specs/c56ee68/schema/MandiPriceObservation/v0.1/attributes.yaml",
  "status": "active"
} }
```

```json
{ "Capability": {
  "capabilityCode": "openagrinet:KnowledgeResource",
  "name": "Knowledge Resource",
  "baseTypes": ["openagrinet:AgricultureResource"],
  "schemaUrl": "https://REPLACE-ME.invalid/network-specs/c56ee68/schema/KnowledgeResource/v0.1/attributes.yaml",
  "status": "active"
} }
```

One `Capability` serves both Advisory categories. Schemes and Crop & Pest are the same
outcome type; they are told apart on the published resource by `subjectCategories`
(`Scheme` vs `Crop`), not by the registry. See §3.

### Providers

```json
{ "Provider": {
  "providerId": "mausamgram",
  "name": "IMD Mausamgram NWP",
  "baseUrl": "https://mausamgram.imd.gov.in/nwpapi",
  "status": "active",
  "auth": { "scheme": "basic",
            "secrets": { "username": "env://MAUSAMGRAM_USER",
                         "password": "env://MAUSAMGRAM_X_API_KEY" } }
} }
```

```json
{ "Provider": {
  "providerId": "imd-city-weather",
  "name": "IMD City Weather",
  "baseUrl": "https://city.imd.gov.in",
  "status": "active",
  "auth": { "scheme": "none" }
} }
```

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
{ "Provider": {
  "providerId": "hasura-content",
  "name": "Vistaar Knowledge Content (Hasura)",
  "baseUrl": "https://content.internal",
  "status": "active",
  "auth": { "scheme": "apiKeyHeader",
            "paramName": "x-hasura-admin-secret",
            "secrets": { "adminSecret": "env://HASURA_GRAPHQL_ADMIN_SECRET" } }
} }
```

```json
{ "Provider": {
  "providerId": "oan-vector",
  "name": "OAN Vector Index",
  "baseUrl": "http://3.6.146.174:8882",
  "status": "active",
  "auth": { "scheme": "none" }
} }
```

> `oan-vector` is a bare IP over plain HTTP. It is legal only because `scheme: none` —
> nothing is leaked by it. Moving it behind TLS with a real hostname is onboarding work,
> not a schema change, and should happen before v1 carries real traffic.

### Bindings

```json
{ "ProviderCapability": {
  "bindingKey": "mausamgram|openagrinet:WeatherObservation|select",
  "providerId": "mausamgram",
  "capabilityCode": "openagrinet:WeatherObservation",
  "method": "GET",
  "path": "/get-daily",
  "enricher": "pointFromIntent",
  "requestMapping":  "mappings/mausamgram/select.request.jsonata",
  "responseMapping": "mappings/mausamgram/select.response.jsonata",
  "timeoutMs": 30000,
  "retryMax": 3,
  "status": "active"
} }
```

```json
{ "ProviderCapability": {
  "bindingKey": "imd-city-weather|openagrinet:WeatherObservation|select",
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

```json
{ "ProviderCapability": {
  "bindingKey": "agmarknet|openagrinet:MandiPriceObservation|select",
  "providerId": "agmarknet",
  "capabilityCode": "openagrinet:MandiPriceObservation",
  "method": "GET",
  "path": "/v1/fetch-agmarknet-vistaar-location",
  "enricher": "marketAndCommodityCodes",
  "requestMapping":  "mappings/agmarknet/select.request.jsonata",
  "responseMapping": "mappings/agmarknet/select.response.jsonata",
  "timeoutMs": 20000,
  "retryMax": 2,
  "status": "active"
} }
```

```json
{ "ProviderCapability": {
  "bindingKey": "hasura-content|openagrinet:KnowledgeResource|select",
  "providerId": "hasura-content",
  "capabilityCode": "openagrinet:KnowledgeResource",
  "method": "POST",
  "path": "/v1/graphql",
  "enricher": "knowledgeQueryParams",
  "requestMapping":  "mappings/hasura-content/select.request.jsonata",
  "responseMapping": "mappings/hasura-content/select.response.jsonata",
  "timeoutMs": 15000,
  "retryMax": 0,
  "status": "active"
} }
```

```json
{ "ProviderCapability": {
  "bindingKey": "oan-vector|openagrinet:KnowledgeResource|select",
  "providerId": "oan-vector",
  "capabilityCode": "openagrinet:KnowledgeResource",
  "method": "POST",
  "path": "/indexes/oan-index/search",
  "enricher": "knowledgeQueryParams",
  "requestMapping":  "mappings/oan-vector/select.request.jsonata",
  "responseMapping": "mappings/oan-vector/select.response.jsonata",
  "timeoutMs": 15000,
  "status": "active"
} }
```

### Before seeding

- `agmarknet`'s `select.request.jsonata` must emit `lat`, `long`, `commodity_id` and a
  single `date`. The location endpoint above takes those; the older four-code endpoint the
  mapping was written for is not what production calls.
- Both `KnowledgeResource` bindings share `enricher: knowledgeQueryParams`. Their request
  mappings do not — one shapes a Hasura GraphQL `variables` block, the other a vector
  search body.
- Fill `<network-specs-raw>` on all three capabilities.

---

## 3. Execution

Two hops. **Hop ① is `discover`, CN → DS.** The discovery service answers from its own
indexed catalog store — no provider is contacted and the registry is not read.
**Hop ② is `select`, CN → adapter → provider**, and that is the only hop the registry
serves.

```
  FARMER        EXPERIENCE LAYER        ONIX ADAPTER        REGISTRY      DISCOVERY SVC    PROVIDER
    │                  │                     │                 │               │             │
    │ "will it rain?"  │                     │                 │               │             │
    ├─────────────────▶│                     │                 │               │             │
    │            ① resolve meaning           │                 │               │             │
    │                  │                     │                 │               │             │
    │                  │ ② discover ─────────┼─────────────────┼──────────────▶│             │
    │                  │◀────────────────────┼─────────────────┼───────────────┤             │
    │                  │   on_discover: catalogs, ~20 ms       │               │             │
    │                  │   provider.id + @type + OnDemand      │               │             │
    │                  │   an ADVERTISEMENT — no value in it   │               │             │
    │                  │                     │                 │               │             │
    │                  │ ③ select            │                 │               │             │
    │                  ├────────────────────▶│                 │               │             │
    │                  │                     │ ④ resolve ─────▶│               │             │
    │                  │                     │◀────────────────┤               │             │
    │                  │                     │  call plan+auth │               │             │
    │                  │                     │ ⑤ enrich, map, authenticate, call │           │
    │                  │                     ├─────────────────┼───────────────┼────────────▶│
    │                  │                     │◀────────────────┼───────────────┼─────────────┤
    │                  │                     │ ⑥ map response → Beckn v2       │             │
    │                  │◀────────────────────┤                 │               │             │
    │◀─────────────────┤  on_select: 5 WeatherObservation resources, Direct    │             │
```

**`informationMode` is what says "keep going".** The advertisement carries `OnDemand`, the
result carries `Direct`. Same `@type` in both — the mode is the only thing that differs,
and it is why a second call exists at all.

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
| Mandi | `$.catalogs[*] ? (@.resourceAttributes."@type" == "openagrinet:MandiPriceObservation")` |
| Schemes | `$.catalogs[*] ? (@.resourceAttributes."@type" == "openagrinet:KnowledgeResource" && @.resourceAttributes.subjectCategories[*] == "Scheme")` |
| Crop & Pest | `$.catalogs[*] ? (@.resourceAttributes."@type" == "openagrinet:KnowledgeResource" && @.resourceAttributes.subjectCategories[*] == "Crop")` |

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
        "@context": "https://schemas.openagrinet.global/schema/AgricultureCapability/v0.1/context.jsonld",
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

Note the `@context`: it is `AgricultureCapability`, not `WeatherObservation`. The request
restates the resource **as advertised**, and the advertisement is a capability, not an
observation. The result in step ⑥ switches to the `WeatherObservation` context, because
that is when the object becomes one. `@type` is unchanged across all three.

### ④ Resolve — two single-field reads, no join

Everything needed is on the request body:

```
offer.provider.id            → "mausamgram"
resourceAttributes["@type"]  → "openagrinet:WeatherObservation"
action                       → "select"

bindingKey = "mausamgram|openagrinet:WeatherObservation|select"
```

```
POST /api/v1/ProviderCapability/search
{ "filters": { "bindingKey": { "eq": "mausamgram|openagrinet:WeatherObservation|select" },
               "status": { "eq": "active" } } }

POST /api/v1/Provider/search
{ "filters": { "providerId": { "eq": "mausamgram" }, "status": { "eq": "active" } } }
```

The `@type` is the **same string the advertisement carried** — one type spans both calls.

Preload `Provider` at boot: five rows, about 1 KB. Cache `ProviderCapability` on
`bindingKey` too. Both change on the order of weeks, so invalidation is a redeploy or a
TTL, not a protocol. A warm request costs zero registry reads.

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

`aggregation` is not a governed field. The `parameter` enum has no min/max qualifier, so
there is no conformant way to say "tomorrow's high is 30.6 and low is 22.1" — every Indian
weather upstream reports `tmin`/`tmax`. This is a real gap in the network schema, tracked
as issue 3 of [Open issues](reference/open-issues.md), and it affects Weather, a v1 🟢
category.

---

## Known gaps for v1

| | |
|---|---|
| An `OnDemand` advert fails its own pack | The outcome packs require `observationType`, `source`, `location`, `generatedAt` — all Direct-only. Needs an `if`/`then` on `informationMode` upstream. Until then the advert validates against `AgricultureCapability`, so the mode selects the contract. |
| No min/max qualifier on `parameter` | Daily high/low is inexpressible. Affects Weather. |
| `informationMode` is not in `docs/design/` | Zero mentions in our plan and zero in `src/`. The DS has no notion of advertisement-vs-result today. |
| `schemaUrl` host is a placeholder | Three `Capability` records carry `https://REPLACE-ME.invalid/`. They satisfy the schema pattern but resolve to nothing; the real host must be filled in before seeding. |
| `oan-vector` on plain HTTP | Legal (`scheme: none`) but should move behind TLS before real traffic. |
| `registry/schemas/` and `samples/` not imported | The prose here describes the schema; the schema files themselves live in the BV repo. Conformance cannot be re-run from this directory. |
