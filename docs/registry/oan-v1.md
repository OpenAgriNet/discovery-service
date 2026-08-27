# OpenAgriNet registry — v1

**The registry stores three things: providers, capabilities, and the mapping between
them.** That mapping is the call plan — given a provider and a capability, how do you
actually reach them.

It holds nothing else. Not catalogs, not resources, not search indexes, not participant
identity. A question it cannot answer is a question for something else: *who serves
weather?* is answered by the discovery service from its indexed catalog, and only once
that names a provider is the registry read at all.

| Read this for | Section |
|---|---|
| How a farmer's question becomes a provider call | [1. Architecture](#1-architecture) |
| Where the adapter sits, and who calls the registry | [2. Deployment topologies](#2-deployment-topologies) |
| The three entities, and the thirteen records to seed | [3. Schema and records](#3-schema-and-records) |
| The endpoints the adapter calls | [4. Registry APIs](#4-registry-apis) |
| One question traced end to end, with payloads | [5. Use case execution](#5-use-case-execution) |

**What this page is written against.** Sunbird RC `RELEASE_VERSION=v2.0.0`, run **without
Elasticsearch**; network-specs pinned to `3e593b3`. Nothing is deployed yet, so anything
below that depends on RC *behaviour* rather than on its schema contract is marked as
needing a first-boot check.

| v1 category | `@type` | Providers | Bindings |
|---|---|---|---|
| Weather | `openagrinet:WeatherObservation` | `mausamgram`, `imd-city-weather` | 2 |
| Mandi prices | `openagrinet:MandiPrice` | `agmarknet` | 1 |
| Advisory — Schemes | `openagrinet:KnowledgeResource` | `hasura-content` | 1 |
| Advisory — Crop & Pest | `openagrinet:KnowledgeResource` | `oan-vector` | 1 |

Five bindings, five providers, three capabilities. **Every one is a single upstream call.**
`steps[]`, `sessionGate` and `sessionGrant` exist in the schema and no v1 binding uses
them — they are for PM-Kisan and PMFBY, which are not in v1.


---

## 1. Architecture

Two calls. **The first finds who. The second gets the data.**

**Hop ① — `discover`.** The experience layer asks the discovery service what exists. It
answers from its own indexed catalog store: no provider is contacted, no credential is
touched, **and the registry is not read.** It is a directory lookup, and what comes back
is an *advertisement* — `mausamgram` serves `WeatherObservation` — not a forecast.

**Hop ② — `select`.** Now the request names that provider and that capability. **This is
the only hop the registry serves.** The adapter builds a key from those two values, reads
the call plan, enriches, maps, authenticates, calls the upstream, and maps the answer
back into Beckn.

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
result carries `Direct`. **Same pack, same `@type`, same `@context`** — the mode is the
only thing that differs, and it is why a second call exists at all. There is no separate
capability schema; that model was proposed and dropped.

It is `required` on every pack, via the shared `AgricultureResource` field set, and each
pack conditions its other required fields on it. So the mode is not a hint — it selects
which half of the contract applies:

| pack | `OnDemand` requires | `Direct` requires |
|---|---|---|
| `WeatherObservation` | `supportedObservationTypes`, `supportedParameters`, `geographicGranularities`; **no** `parameters` | `observationType`, `source`, `location`, `generatedAt`, `parameters` |
| `MandiPrice` | `supportedCommodities`, `supportedPriceFields`; **no** `prices` | `source`, `commodity`, `market`, `arrivalDate`, `prices`, `generatedAt` |
| `KnowledgeResource` | `topics`, `languages`, `supportedKnowledgeTypes`; **no** `content` | `topics`, `languages`, `knowledgeType`, `version`, `lifecycleStatus`, `content`, `provenance` |

An advertisement that carried real values would fail its own pack, and a result that
carried only capabilities would too. That is the point: the two cannot be confused.


---

## 2. Deployment topologies

The work at hop ② is **identical in both**. What changes is how many network boundaries
sit in front of it, and therefore who calls the registry.

### A — one adapter, at the centre

```
   ADOPTER                          NETWORK LAYER                    PROVIDER
 ┌───────────────┐              ┌──────────────────┐             ┌────────────┐
 │ experience    │  /discover   │                  │  discovery  │            │
 │ layer         ├─────────────▶│   ONIX adapter   │◀───────────▶│    (not    │
 │ (chatbot,     │              │                  │   service   │   called)  │
 │  call centre) │  /select     │   ┌──────────┐   │             │            │
 │               ├─────────────▶│   │ registry │   ├────────────▶│    IMD     │
 │               │◀─────────────┤   └──────────┘   │◀────────────┤            │
 └───────────────┘  on_select   └──────────────────┘             └────────────┘
```

One ONIX instance is both the consumer's outbound point and the provider node. **The
adapter calls the registry**, and the experience layer never sees it.

Fewest moving parts: one process to operate, one registry read, and signature
verification that resolves to the same party on both sides — so it proves nothing and
costs nothing. Fine while the adopter is the only participant. It stops being fine the
moment a second consumer wants these capabilities, because there is no real trust
boundary to enforce anything at.

### B — an adapter at each layer

```
   ADOPTER                    NETWORK LAYER              PROVIDER LAYER
 ┌────────────────┐         ┌───────────────┐         ┌────────────────────┐
 │ experience     │         │               │         │  PROVIDER ONIX     │
 │ layer          │ /select │ CONSUMER ONIX │         │  validate · route  │
 │                ├────────▶│  route · sign ├────────▶│  ┌──────────┐      │
 │                │◀────────┤               │◀────────┤  │ registry │      ├──▶ IMD
 └────────────────┘         └───────────────┘         │  └──────────┘      │◀──
                                                      │  map · respond     │
                                                      └────────────────────┘
```

Same two hops, one more boundary. Three things change, and only the third is about the
registry:

1. **Signature verification becomes real.** The consumer and provider sides are different
   parties, so verifying the caller is now worth doing.
2. **Half the adapter config is dormant.** The async callback route never fires under
   synchronous transport. Expected, not a misconfiguration.
3. **The registry stays on the provider side, and only there.** This is the rule that
   matters.

> **The consumer side must never learn that `mausamgram` means
> `https://mausamgram.imd.gov.in/nwpapi`.** Resolving a call plan means holding the
> upstream credentials that go with it. A consumer-side adapter that resolves capabilities
> needs `env://MAUSAMGRAM_X_API_KEY` to be resolvable in *its* environment — and at that
> point the credential has left the provider's control, which is the one thing the
> `env://` pointer design exists to prevent.

### Which to run

| | A — adapter at the centre | B — adapter at each layer |
|---|---|---|
| Registry is read by | the single adapter | the **provider-side** adapter only |
| Experience layer knows | provider ids only | provider ids only |
| Upstream credentials live | in the one adapter | in the provider-side adapter |
| Signature check | resolves to self; proves nothing | a real trust boundary |
| Network hops before the upstream call | 1 | 2 |

**v1 runs A**, and the page below traces A. B is what a second consumer forces, and
nothing in the schema or the records changes when it happens — only where the adapter is
deployed and which side holds the secrets. That is the point of keeping the call plan in
a registry rather than in a service's config: the move is a deployment change, not a
rewrite.

## 3. Schema and records

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
and capability — under a two-part key they collide. Whether RC then rejects the second
write or silently overwrites the first is a property of the RC build, and since v1 picks
its own build that is a question to answer once with a test rather than design around. The
three-part key means the answer never matters. What went away is the redundant column, not
the discriminator; to display or group by action, split the key.

**Mappings live in files, not in the row.** The Mausamgram response transform is 76 lines
of JSONata; stored in the row it is one string with every newline escaped, unreviewable in
a diff. The pattern pins the directory, rejects `..`, requires `.jsonata`, and allows
lowercase only — a path differing from disk by case resolves on macOS and 404s on Linux.

```
registry/mappings/<provider>/<action>.<request|response>.jsonata
```

### Records to seed

Thirteen records: 3 `Capability`, 5 `Provider`, 5 `ProviderCapability`. Shown in RC write
form.

> `schemaUrl` points at [`OpenAgriNet/network-specs`](https://github.com/OpenAgriNet/network-specs),
> pinned to a **full commit sha** — `3e593b3` here. The schema's two negative lookaheads
> reject a branch ref on purpose: pinned to `main`, the contract you validated against last
> week is not the one you validate against today and nothing tells you it moved. Bumping
> the sha is a reviewed change to these three records.

#### Capabilities

```json
{ "Capability": {
  "capabilityCode": "openagrinet:WeatherObservation",
  "name": "Weather Observation and Forecast",
  "baseTypes": ["openagrinet:AgricultureResource"],
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/3e593b3627acae6f416382e6d4bd58f641f309e8/schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active"
} }
```

```json
{ "Capability": {
  "capabilityCode": "openagrinet:MandiPrice",
  "name": "Mandi Price",
  "baseTypes": ["openagrinet:AgricultureResource"],
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/3e593b3627acae6f416382e6d4bd58f641f309e8/schema/MandiPrice/v0.1/attributes.yaml",
  "status": "active"
} }
```

```json
{ "Capability": {
  "capabilityCode": "openagrinet:KnowledgeResource",
  "name": "Knowledge Resource",
  "baseTypes": ["openagrinet:AgricultureResource"],
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/3e593b3627acae6f416382e6d4bd58f641f309e8/schema/KnowledgeResource/v0.1/attributes.yaml",
  "status": "active"
} }
```

One `Capability` serves both Advisory categories. Schemes and Crop & Pest are the same
outcome type; they are told apart on the published resource by `subjectCategories`
(`Scheme` vs `Crop`), not by the registry. See [§5](#5-use-case-execution).

#### Providers

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

#### Bindings

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
  "bindingKey": "agmarknet|openagrinet:MandiPrice|select",
  "providerId": "agmarknet",
  "capabilityCode": "openagrinet:MandiPrice",
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

#### Before seeding

- `agmarknet`'s `select.request.jsonata` must emit `lat`, `long`, `commodity_id` and a
  single `date`. The location endpoint above takes those; the older four-code endpoint the
  mapping was written for is not what production calls.
- Both `KnowledgeResource` bindings share `enricher: knowledgeQueryParams`. Their request
  mappings do not — one shapes a Hasura GraphQL `variables` block, the other a vector
  search body.

---

## 4. Registry APIs

Sunbird RC generates the REST surface from the JSON Schemas in §3 — one set of routes per
entity, named after it. Nothing here is hand-written.

### The reads the adapter needs

Exactly two, both single-field and both exact-match:

```
POST /api/v1/ProviderCapability/search
{ "filters": { "bindingKey": { "eq": "mausamgram|openagrinet:WeatherObservation|select" },
               "status":     { "eq": "active" } } }

POST /api/v1/Provider/search
{ "filters": { "providerId": { "eq": "mausamgram" },
               "status":     { "eq": "active" } } }
```

The first returns the call plan, the second the base URL and auth block. **No join, and no
second capability read** — `Capability` is vocabulary, not something the call path needs.

> The `|select` in the key is not a leftover. `bindingKey` is
> `<providerId>|<capabilityCode>|<action>`, and the action segment is what keeps
> `pm-kisan|…|init` and `pm-kisan|…|status` apart. What v1 removed is the redundant
> `action` *column*, not the key segment — see §3.

### Two things to confirm on first boot

**Whether `/search` works at all without Elasticsearch.** In a standard RC deployment the
search API is ES-backed, and v1 runs without ES. Whether v2.0.0 also ships a
database-backed search provider, and what filter grammar it accepts, is a config question
to settle against the release — RC's own notes warn that `_osConfig` support and the
search grammar differ between versions, which is why the version is pinned at all. **Nothing
in this page's design depends on the answer**, for the reason below.

**Which read returns every row of an entity.** The boot load needs one, and its exact route
is the thing to check first in the generated surface.

### The runtime does not read the registry per request

```
13 records total — 5 Provider, 3 Capability, 5 ProviderCapability.  A few KB.
```

**Load all three entities at boot and resolve in memory.** Index `ProviderCapability` by
`bindingKey` and `Provider` by `providerId`; resolution is then two map lookups and the
per-request registry cost is **zero reads**, not one or two. Records change on the order of
weeks, so refresh is a redeploy or a TTL, never a protocol.

This is the right shape even with ES available: an exact-match lookup over 13 rows has
nothing to gain from a search engine. It becomes a question at a scale v1 is nowhere near,
and what changes then is the boot load, not the resolution logic.

One consequence worth stating plainly: **with no ES, `indexFields` buys nothing at
runtime**, because the runtime never queries. It stays declared because it documents which
fields are meant to be queryable, and because operational reads — *which bindings does this
provider have?* — still go through whatever read path the build offers.

### Writes — onboarding only

```
POST /api/v1/{Entity}              create
PUT  /api/v1/{Entity}/{osid}       update
```

**The adapter never writes.** These belong to the onboarding path, where the two integrity
rules that no JSON Schema can express are also enforced: `bindingKey`'s first two segments
must agree with `providerId` and `capabilityCode`, and both must resolve to live records.

---

## 5. Use case execution

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
request — see [§4](#4-registry-apis) for the reads, the boot load, and what to confirm
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


---

## Known gaps for v1

| | |
|---|---|
| No min/max qualifier on `parameter` | The `parameters` item is `{parameter, value, unit}` with an eight-value enum and no aggregation field, so *tomorrow's high is 30.6 and low is 22.1* is inexpressible — and every Indian weather upstream reports `tmin`/`tmax`. The item is open, so mappings emit a private `aggregation` and it validates while meaning nothing to anyone else. Affects Weather, a v1 category. |
| `informationMode` is not in `docs/design/` | Zero mentions in our plan and zero in `src/`, yet it is `required` on every published resource and decides which half of each pack applies. Whether the DS stores it, indexes it, and lets a caller filter on it is an open decision — and the one item here that changes our code. |
| Nothing re-pins the `schemaUrl` sha | The three `Capability` records point at `3e593b3`. When network-specs moves, nothing here notices; the pin is correct and manual. A check that the sha resolves and that the pack still declares the expected `@type` belongs in the seeding path. |
| `oan-vector` on plain HTTP | Legal (`scheme: none`) but should move behind TLS before real traffic. |
| No JSON Schema files behind this page | Everything above is prose. Sunbird RC boots from JSON Schema, not from a table, so `Provider`, `Capability` and `ProviderCapability` have to be authored before anything can be seeded. Nothing is being migrated — v1 stands the registry up from scratch — so these are ours to write, not BV's to send. |
