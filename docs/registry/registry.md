# OpenAgriNet registry

The registry stores **providers, capabilities, and the binding between them**. The binding
is the call plan: given a provider and a capability, how do you reach them.

Nothing else lives here — no catalogs, no resources, no search index.

**Who reads it:** the adapter (or the adopter's experience layer).
**Who does not:** discovery-service. It answers `/discover` from its own catalog store.

| | | |
|---|---|---|
| 1 | [How it fits](#1-how-it-fits) | Two hops, and which one reads the registry |
| 2 | [Three schemas](#2-three-schemas) | What each one answers |
| 3 | [The schemas](#3-the-schemas) | The contract, field by field |
| 4 | [Examples](#4-examples) | One record per entity |
| 5 | [APIs](#5-apis) | Create, search, update, delete — with payloads |
| 6 | [Do today's providers fit?](#6-do-todays-providers-fit) | All 5 v1 providers, validated |
| → | [usecases.md](usecases.md) | Each use case end to end |
| → | [dpg-fit.md](dpg-fit.md) | Whether those use cases conform to the OAN domain packs |
| → | [Deferred](#deferred--out-of-scope) | What is deliberately absent, and what brings it back |

---

## 1. How it fits

Two calls. **The first finds who. The second gets the data.**

| hop | call | goes to | reads registry |
|---|---|---|---|
| ① | `discover` | discovery service | **no** |
| ② | `select` | adapter → provider | **yes** — twice |

```
FARMER ─▶ EXPERIENCE LAYER ─① discover ──▶ DISCOVERY SVC   "mausamgram serves WeatherObservation"
                           ─② select ───▶ ADAPTER ─▶ REGISTRY   read binding + provider
                                                   └─▶ PROVIDER  call, map back to Beckn
```

Hop ① returns an **advertisement** — no values in it. Hop ② returns the **data**. Same
`@type`, same `@context`; what the caller does with the answer is the only difference, and it
is why a second call exists.

**Adapter placement.** Either one adapter at the centre, or one adapter per layer
(experience-layer adapter calls `/discover`, then calls the provider adapter for `select`).
Hop ② is identical in both. What changes is **who holds the upstream credentials** — with one
central adapter, it does; with per-layer adapters, each provider adapter holds its own.

---

## 2. Three schemas

| schema | answers | v1 records |
|---|---|---|
| **`Provider`** | Where is this provider, and how do we authenticate to it? | 5 |
| **`Capability`** | What outcome is this, in network vocabulary? | 3 |
| **`ProviderCapability`** | Which endpoint of that provider answers that capability, and how is it shaped? | 5 |

They join on ids, not on foreign keys — Sunbird RC has none:

```
Provider.providerId ─┐
                     ├─▶ ProviderCapability.bindingKey = "<providerId>|<capabilityCode>"
Capability.capabilityCode ─┘
```

Schema files, which are the contract: [`schemas/Provider.json`](schemas/Provider.json) ·
[`schemas/Capability.json`](schemas/Capability.json) ·
[`schemas/ProviderCapability.json`](schemas/ProviderCapability.json).
All three are draft-07 with `additionalProperties: false` and an RC `_osConfig` block.

**This page describes those files; it does not restate them.** An inline copy of a schema is
a second thing to keep true, and it is the copy that rots.

---

## 3. The schemas

### 3.0 Shared definitions

RC loads each entity schema on its own, so a `$ref` across files is not available. Four
building blocks are therefore repeated **verbatim** in every file that uses them, under the
same name — so a mismatch is a diff, not a judgement call.

| definition | value | used by |
|---|---|---|
| `Status` | `active` \| `inactive` | all three |
| `ProviderId` | `^[a-z0-9][a-z0-9._:-]{2,63}$` — one char, then 2–63 more, so **min length 3** | `Provider`, `ProviderCapability` |
| `CapabilityCode` | `^openagrinet:[A-Z][A-Za-z0-9]*$`, and not `AgricultureResource`/`AgricultureCapability` | `Capability`, `ProviderCapability` |
| `Secret` | `^(env://[A-Z][A-Z0-9_]{0,63}\|inline:[!-~][ -~]{0,998})$` — two legal forms and nothing else | `Provider.auth`, `Enricher` |

Four more are local to one file and are **not** shared: `ParamName` (`Provider`), `TypeCode`
(`Capability`), `Path` and `MappingPath` (`ProviderCapability`).

One `status` vocabulary across all three entities is deliberate. Every read filters
`status == "active"`; a `Capability` that said `deprecated` instead of `inactive` meant the
same thing to the reader and a different string to the filter.

### 3.1 `Provider`

Four scalars and one `auth` object. That is the whole entity.

Every field, in the shape RC receives it. This is a **valid record** — the checker below
validates it against `schemas/Provider.json` and fails if any field goes unexercised — but
the constraints are in the table above, not here.

```jsonc
{ "Provider": {                                 // write bodies are wrapped: the key is the entity
  "providerId": "hasura-content",               // required · Beckn provider.id
  "name":       "Vistaar Knowledge Content",    // required · display only
  "baseUrl":    "https://content.internal",     // required · no trailing slash
  "status":     "active",                       // required · active | inactive
  "auth": {                                     // required
    "scheme":      "apiKeyHeader",              // required · none | apiKeyQuery | apiKeyHeader | basic
    "paramName":   "Authorization",             // required for both apiKey schemes
    "valuePrefix": "Bearer ",                   // optional · apiKeyHeader only · trailing space required
    "secrets":     { "token": "env://HASURA_TOKEN" }   // env://VAR or inline:… — nothing else
  }
} }
```

`scheme` decides which of the other three may appear at all; the table below is that rule.
`none` carries none of them, `basic` carries only `secrets` and needs exactly `username` and
`password`.

| field | type | constraint | req |
|---|---|---|---|
| `providerId` | string | `ProviderId` — this is the Beckn `provider.id` | ✓ |
| `name` | string | 1–200 chars, at least one non-space, display only | ✓ |
| `baseUrl` | string | scheme + host + optional path segments, nothing else. `https` required if any credential is held | ✓ |
| `status` | string | `active` \| `inactive` | ✓ |
| `auth` | object | → `Auth` — the credential for every call to this provider | ✓ |

**Auth is on `Provider`, not on the binding.** One credential opens all of that provider's
endpoints — true for all five. On `ProviderCapability` it would be copied into every binding
row, and a rotation would touch N rows instead of one.

`Provider.baseUrl` is **where** the provider is; `method` + `path` on the binding is **which
endpoint** answers a capability. They are split because one provider serves several
capabilities from different paths — put `path` on `Provider` and each needs a duplicate
record, which means a duplicate credential.

```
https://mausamgram.imd.gov.in/nwpapi  +  /get-daily
└────────── Provider.baseUrl ───────┘     └─ ProviderCapability.path ─┘
```

`baseUrl` forbids a trailing slash, `path` requires a leading one — exactly one `/` falls
between them, so no code normalises. Four more things `baseUrl` refuses, each because the
concatenation above would otherwise produce a URL nobody wrote:

| refused in `baseUrl` | what the concatenation would have done |
|---|---|
| `?` or `#` | `…/v1?tenant=a` + `/get-daily` puts the path **inside the query string** |
| `@` (userinfo) | `https://user:pass@host` is a credential outside `auth.secrets` — so outside `privateFields`, and into every `/search` response and every log line that prints the URL |
| whitespace | unusable, and silently mangled differently by every HTTP client |
| `..` | traversal against the upstream, from a field nobody reads as a path |

`path` forbids `?` for the same reason from the other side: a query string belongs to the
`requestMapping`, which builds it from the request, so a value never reaches the wire by being
concatenated into a stored string.

**`Auth`** — three fields, four schemes.

| field | constraint | req |
|---|---|---|
| `scheme` | `none` \| `apiKeyQuery` \| `apiKeyHeader` \| `basic` | ✓ |
| `paramName` | `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$` — the query-parameter or header name | per scheme |
| `valuePrefix` | `^[A-Za-z][A-Za-z0-9._-]{0,30} $` — prepended to the credential. **The trailing space is required** | optional |
| `secrets` | every value `Secret` | per scheme |

| `scheme` | the adapter does | then requires | and must not carry |
|---|---|---|---|
| `none` | sends no credential | — | `secrets`, `paramName`, `valuePrefix` |
| `apiKeyQuery` | appends `?<paramName>=<secret>` | `paramName`, exactly one `secrets` entry | `valuePrefix` |
| `apiKeyHeader` | sets `<paramName>: <valuePrefix><secret>` | `paramName`, exactly one `secrets` entry | — |
| `basic` | RFC 7617 from `secrets.username` / `secrets.password` | both keys | `paramName`, `valuePrefix` |

**There is no `bearer` scheme, because `valuePrefix` is one.** `Authorization: Bearer <token>`
is `apiKeyHeader` with `paramName: "Authorization"` and `valuePrefix: "Bearer "` — and the same
field also expresses `Token `, `ApiKey `, or whatever the next upstream invents. A scheme per
prefix would be an enum that grows once per provider.

**`valuePrefix` requires its own trailing space**, which looks fussy and is not. Without it,
whether the adapter inserts a separator is a convention living in code, and the first provider
that wants `Bearer<token>` with no space cannot be expressed at all. Writing the separator
into the value makes the wire format visible in the record.

**Three patterns exist to stop header injection**, and they do not reach far enough on their
own. `paramName`, `valuePrefix` and an `inline:` secret are written into an HTTP header
verbatim, so a `\r\n` in any of them appends attacker-chosen headers; none of the three admits
a control character. That is the only reason they are constrained more tightly than "a
non-empty string".

**What the schema cannot cover, and the adapter therefore must.** An `env://` secret's *value*
never passes through this schema — it arrives from the environment at call time. So the
control-character check has to be repeated on the **resolved** credential, or it protects only
the `inline:` half. Two more belong to the adapter for the same reason: percent-encoding the
credential before it goes in a query string (`apiKeyQuery` with a secret containing `&` would
otherwise append a parameter), and whatever allowlist decides that a `baseUrl` may not be
`localhost` or `169.254.169.254` — the pattern cannot tell a metadata endpoint from a
hostname, and the adapter attaches a credential to whatever it is given.

**One credential per API-key scheme**, enforced by `maxProperties: 1`. A provider needing both
an API key and a tenant token is a real thing and is not v1's; see
[Deferred](#deferred--out-of-scope). Capping it now means the second credential arrives as a
reviewed schema change rather than as an extra map entry nobody notices.

**A secret has exactly two legal forms, and must say which:**

```
"secrets": { "password": "env://MAUSAMGRAM_PASS" }   // pointer — resolved from the adapter's environment
"secrets": { "token":    "inline:a7f3c9d2e1b8..." }  // the credential itself, stored here
```

A bare pasted key matches neither and is rejected at write time. An `inline:` value is
printable ASCII, 1–999 characters, no leading space, so no carriage return or tab reaches the
header it is written into. One caveat measured rather than assumed: a *trailing* newline is
refused by ECMA-262 and by Java's `matches()`, and accepted by Python's `re`, where `$` also
matches before a final newline — so the guarantee is the validator's, not the pattern's. **Prefer `env://`** — the
registry then holds no credential at all. `inline:` exists for operators who cannot set the
adapter's environment, and it costs three things: `/search` is authenticated
([§5](#5-apis)), the database holds live key material, and rotation becomes a registry write.
Because the prefix is literal, *which providers hold a real key* is one query over the table.

**A credential implies TLS.** Every scheme except `none` requires `secrets`, so the schema
forces `baseUrl` to `https` whenever `auth.scheme != "none"`. Plaintext stays legal for
`scheme: "none"` only.

---

### 3.2 `Capability`

```jsonc
{ "Capability": {
  "capabilityCode": "openagrinet:WeatherObservation",   // required · the outcome @type
  "name":           "Weather Observation and Forecast", // required · display only
  "schemaUrl":      "https://raw.githubusercontent.com/OpenAgriNet/network-specs/3e593b3627acae6f416382e6d4bd58f641f309e8/schema/WeatherObservation/v0.1/attributes.yaml",
  "status":         "active",                           // required · active | inactive
  "baseTypes":      ["openagrinet:AgricultureResource"] // optional · unique, composed with allOf
} }
```

| field | type | constraint | req |
|---|---|---|---|
| `capabilityCode` | string | `CapabilityCode`. **This is the outcome `@type`** | ✓ |
| `name` | string | `minLength: 1` | ✓ |
| `schemaUrl` | string | the network-specs pack, **not on a branch** — `refs/heads/…` and `/main/`, `/master/`, `/develop/` are rejected | ✓ |
| `status` | string | `active` \| `inactive` | ✓ |
| `baseTypes` | array | `TypeCode`, unique — shared field sets this pack composes with `allOf` | |

**Nothing names a provider here.** A capability is network vocabulary; the binding attaches it
to a provider. The branch rejection matters because a capability pinned to `main` validates
against a different contract each week, silently. The seeded records go further and pin a full
commit sha, which is the only genuinely immutable ref — the pattern cannot require it without
also excluding hosts that do not expose one.

> Whether `capabilityCode` should carry the **outcome** type (`openagrinet:WeatherObservation`)
> or the governed **capability** type (`openagrinet:WeatherObservationCapability`) is an open
> alignment question with the OAN domain packs, and one of the seeded three does not match
> either. [dpg-fit.md](dpg-fit.md) has the evidence.

---

### 3.3 `ProviderCapability`

The entity that does the work. A record is **one call** — one shape, no alternatives.

```jsonc
{ "ProviderCapability": {
  "bindingKey":      "imd-city-weather|openagrinet:WeatherObservation",  // required · providerId + "|" + capabilityCode
  "providerId":      "imd-city-weather",             // required · must equal segment 1
  "capabilityCode":  "openagrinet:WeatherObservation",  // required · must equal segment 2
  "status":          "active",                       // required · active | inactive
  "method":          "GET",                          // required · GET | POST
  "path":            "/citywx/city_weather_test.php",   // required · appended to Provider.baseUrl
  "requestMapping":  "mappings/imd-city-weather/select.request.jsonata",   // required
  "responseMapping": "mappings/imd-city-weather/select.response.jsonata",  // required
  "enricher": {                                      // optional · a Go plugin, run before the request mapping
    "name":    "nearestStation",                     // required inside enricher
    "config":  { "maxDistanceKm": 50 },              // optional · free-form, passed to the plugin
    "secrets": { "dsn": "env://IMD_DB_DSN" }         // optional · same two forms as Provider.auth
  },
  "timeoutMs": 15000,                                // optional · 1000–120000, default 15000
  "retryMax":  0                                     // optional · 0–5, default 0
} }
```

| field | type | constraint | req |
|---|---|---|---|
| `bindingKey` | string | exactly `providerId` + `\|` + `capabilityCode` — **two segments** | ✓ |
| `providerId` | string | must equal segment 1 | ✓ |
| `capabilityCode` | string | must equal segment 2 | ✓ |
| `status` | string | `active` \| `inactive` | ✓ |
| `method` | string | `GET` \| `POST` | ✓ |
| `path` | string | `Path` — appended to `Provider.baseUrl`. **No query string** | ✓ |
| `requestMapping` | string | Beckn request → upstream request | ✓ |
| `responseMapping` | string | upstream response → Beckn v2 resources | ✓ |
| `enricher` | object | → `Enricher` — a Go plugin run **before** the request mapping | |
| `timeoutMs` | integer | 1000–120000, default 15000 | |
| `retryMax` | integer | 0–5, default 0 | |

**`GET` and `POST` only.** Every binding here answers a read. A `PUT` or `DELETE` in a
discovery path is a bug, and an enum is a cheaper place to catch it than a review.

**Timeout and retry are registry columns, not constants in a service class.** IMD gets 30 s
and 3 retries; Hasura gets 15 s and none. Those are properties of the upstream, changed by an
operator.

> `retryMax` is not conditioned on `method`, deliberately. The obvious rule — *no retries on
> `POST`* — would forbid retrying a GraphQL **read**, which is the single most common POST in
> this network. The judgement stays with whoever writes the record.

**`Enricher`** — `{name, config, secrets}`, always the object form. It exists only for what
the Beckn body cannot express: a private code namespace (Agmarknet's `marketcode`), or a
lookup against something the adapter owns (`nearestStation`'s Postgres). *If a JSONata
expression can do it, it is a mapping, not an enricher.*

**Mappings live in files, not in the row** —
`mappings/<provider>/<action>.<request|response>.jsonata`. The row stores the path; the file
is reviewed and diffed like source. The pattern rejects `..` traversal and uppercase — a
case-only difference resolves on macOS and 404s on a Linux pod.

| mapping | input | output |
|---|---|---|
| `enricher` (Go) | the Beckn request | `_local` |
| `requestMapping` | `{beckn, _local}` | the upstream request |
| `responseMapping` | `{beckn, _local, response}` | Beckn v2 resources |

---

### 3.4 Two rules the schema cannot express

JSON Schema cannot compare two fields, and RC enforces no reference between entities. These
run in the onboarding path and in the conformance suite:

1. `bindingKey` **must equal** `providerId` + `"|"` + `capabilityCode`.
2. Both must resolve to **live** records — an `active` `Provider` and an `active` `Capability`.

Two more are known and not yet built: resolving every `enricher.name` against the adapter's
plugin table at boot and refusing to start if one is missing, and confirming that every
`responseMapping` emits something the pack in `Capability.schemaUrl` accepts — which
[dpg-fit.md](dpg-fit.md) shows three of five bindings currently do not.

---

## 4. Examples

One record per entity. The thirteen actually seeded are in **[examples.md](examples.md)**;
each one traced end to end is in **[usecases.md](usecases.md)**.

**`Provider`** — `apiKeyHeader` with a prefix, the shape a Bearer-token upstream takes

```json
{ "Provider": {
  "providerId": "example-provider",
  "name": "A provider behind a bearer token",
  "baseUrl": "https://api.example.gov.in/v2",
  "status": "active",
  "auth": { "scheme": "apiKeyHeader",
            "paramName": "Authorization",
            "valuePrefix": "Bearer ",
            "secrets": { "token": "env://EXAMPLE_TOKEN" } }
} }
```

The other three schemes in full:

```json
{ "scheme": "none" }
{ "scheme": "apiKeyQuery",  "paramName": "token", "secrets": { "token": "env://EXAMPLE_TOKEN" } }
{ "scheme": "basic", "secrets": { "username": "env://EXAMPLE_USER", "password": "env://EXAMPLE_PASS" } }
```

**`Capability`** — every property

```json
{ "Capability": {
  "capabilityCode": "openagrinet:WeatherObservation",
  "name": "Weather Observation and Forecast",
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/3e593b3627acae6f416382e6d4bd58f641f309e8/schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active",
  "baseTypes": ["openagrinet:AgricultureResource"]
} }
```

**`ProviderCapability`** — every property, which is also the shape all five v1 bindings use

```json
{ "ProviderCapability": {
  "bindingKey": "example-provider|openagrinet:WeatherObservation",
  "providerId": "example-provider",
  "capabilityCode": "openagrinet:WeatherObservation",
  "status": "active",

  "method": "GET",
  "path": "/get-daily",
  "requestMapping":  "mappings/example-provider/select.request.jsonata",
  "responseMapping": "mappings/example-provider/select.response.jsonata",

  "enricher": { "name": "nearestStation",
                "config": { "maxDistanceKm": 50, "maxStationAttempts": 5 },
                "secrets": { "dsn": "env://IMD_DB_DSN" } },
  "timeoutMs": 30000,
  "retryMax": 3
} }
```

There is no second, larger example to show. Every field either appears above or does not
exist.

---

## 5. APIs

Sunbird RC generates the REST surface from the three schemas. `<Entity>` is `Provider`,
`Capability` or `ProviderCapability`.

| route | who | what |
|---|---|---|
| `POST /api/v1/<Entity>` | `registryOperator` | create |
| `POST /api/v1/<Entity>/search` | authenticated | look up by indexed field |
| `GET /api/v1/<Entity>/{osid}` | authenticated | read one |
| `PUT /api/v1/<Entity>/{osid}` | `registryOperator` | replace in full |
| `DELETE /api/v1/<Entity>/{osid}` | `registryOperator` | remove permanently |

All three schemas declare `"roles": ["registryOperator"]`. **Read access is a separate,
narrower role and is not declared in these files** — RC's `_osConfig.roles` gates the
entity, not the verb. Until a read-only role exists in the RC deployment's token issuer, any
token that can read can also write, and the table above describes intent rather than
enforcement. This is the one gap in this section worth closing before seeding real
credentials.

`osid` is RC's row id, returned by the create. It is **not** `providerId` and not
`bindingKey` — so an update has to search first.

### Create

```http
POST /api/v1/Provider
Authorization: Bearer <operator-token>
Content-Type: application/json
```
```json
{ "Provider": {
  "providerId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "baseUrl": "https://api.agmarknet.gov.in",
  "status": "active",
  "auth": { "scheme": "apiKeyQuery",
            "paramName": "token",
            "secrets": { "token": "env://MANDI_TOKEN" } } } }
```
```json
200 OK
{ "id": "sunbird-rc.registry.create",
  "params": { "status": "SUCCESSFUL" },
  "result": { "Provider": { "osid": "1-8f2c4e7a-3b91-4d0e-9c55-2a1f6b8e0d34" } } }
```

**The body is wrapped**, one level down under the entity name — which is exactly what each
schema's top level requires (`required: ["Provider"]`). The same form validates locally and
goes on the wire, so the records in [examples.md](examples.md) are write bodies as they
stand.

Seed in order: **`Capability` → `Provider` → `ProviderCapability`.** The binding's integrity
rules need the other two to exist and be `active`.

### Search

```http
POST /api/v1/ProviderCapability/search
Authorization: Bearer <read-token>
```
```json
{ "filters": { "bindingKey": { "eq": "agmarknet|openagrinet:MandiPrice" },
               "status":     { "eq": "active" } } }
```
```json
200 OK
[ { "osid": "1-4c7d...", "bindingKey": "agmarknet|openagrinet:MandiPrice",
    "providerId": "agmarknet", "capabilityCode": "openagrinet:MandiPrice",
    "method": "GET", "path": "/v1/fetch-agmarknet-vistaar-location",
    "requestMapping": "mappings/agmarknet/select.request.jsonata",
    "responseMapping": "mappings/agmarknet/select.response.jsonata",
    "enricher": { "name": "marketAndCommodityCodes" },
    "timeoutMs": 20000, "retryMax": 2, "status": "active" } ]
```

> The two response bodies above are **illustrative shapes, not captured output** — whether
> RC returns the rows bare or wrapped, and what it puts around them, has not been checked
> against the pinned build. The requests are the part the archive corroborates.

**The adapter needs exactly two reads**, both single-field exact matches — the binding above,
then `POST /api/v1/Provider/search` with `{"providerId": {"eq": "agmarknet"}}`. No join, and
no `Capability` read: `Capability` is vocabulary, not part of the call path.

**`search` is not public.** A record may hold an `inline:` credential, so a read of `Provider`
is a read of live key material.

> `Provider._osConfig.privateFields` lists `$.auth.secrets`, which should mean RC redacts it
> from this response. That has **not** been verified against the pinned build
> (`RELEASE_VERSION=v2.0.0`), and the two statements — "`/search` returns secrets, so
> authenticate it" and "secrets are a private field" — cannot both be the whole truth. One
> check on first boot resolves it; until then assume the response carries the credential.

### Update

Replace in full — RC's `PUT` is not a merge patch. Search for the `osid`, change the field,
send the whole record back.

```http
PUT /api/v1/Provider/1-8f2c4e7a-3b91-4d0e-9c55-2a1f6b8e0d34
Authorization: Bearer <operator-token>
```
```json
{ "Provider": {
  "providerId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "baseUrl": "https://api.agmarknet.gov.in",
  "status": "inactive",
  "auth": { "scheme": "apiKeyQuery",
            "paramName": "token",
            "secrets": { "token": "env://MANDI_TOKEN_2026" } } } }
```

Rotating an `env://` pointer is a registry write; rotating the value behind it is not.
Rotating an `inline:` credential is always a registry write.

### Delete

```http
DELETE /api/v1/ProviderCapability/1-4c7d5e91-2a08-4f6b-8d13-77e0c9a4b521
Authorization: Bearer <operator-token>
```
```json
200 OK
{ "id": "sunbird-rc.registry.delete", "params": { "status": "SUCCESSFUL" } }
```

**Prefer `status: "inactive"` over `DELETE`.** Every read filters on `status`, so flipping it
takes a provider out of service just as completely and leaves the row where an operator can
see what was turned off. `DELETE` orphans quietly — removing a `Provider` leaves its bindings
pointing at nothing, and RC enforces no reference between them.

### The runtime does not call these per request

```
13 records — 5 Provider, 3 Capability, 5 ProviderCapability.  A few KB.
```

Load all three entities **at boot**, index `ProviderCapability` by `bindingKey` and `Provider`
by `providerId`. Resolution is then two map lookups and the per-request registry cost is zero
reads. Records change on the order of weeks; refresh is a redeploy or a TTL.

**Preload is right whichever way `/search` lands**, which is why two RC questions can wait for
first boot rather than blocking the design: whether this deployment's search provider is
database-backed or needs Elasticsearch, and which read returns every row of an entity for the
boot load. Thirteen records is a few KB, and an exact-match key lookup has nothing to gain
from a search engine even when one is available. `indexFields` stays declared because it
documents what is meant to be queryable, not because anything at runtime depends on it.

Both questions are RC-behaviour questions, and **no deployment manifest in this repo pins
`RELEASE_VERSION=v2.0.0`** — the version is stated here and nowhere enforced. A reader cannot
reproduce any RC claim on this page from the repo alone. Every one of them is marked as
unverified where it appears; the fix is a committed compose file, not more prose.

---

## 6. Do today's providers fit?

Yes — all four v1 categories, all five providers, all five bindings. No field left over and
nothing forced.

**Realtime Information**

| use case | capability | provider | transport | auth | binding | enricher |
|---|---|---|---|---|---|---|
| **Weather** — point forecast | `WeatherObservation` | `mausamgram` | HTTPS REST | `basic` | `GET /get-daily` | `pointFromIntent` |
| **Weather** — city / station | `WeatherObservation` | `imd-city-weather` | HTTPS REST | `none` | `GET /citywx/city_weather_test.php` | `nearestStation` (+config, +secret) |
| **Mandi prices** | `MandiPrice` | `agmarknet` | HTTPS REST | `apiKeyQuery` | `GET /v1/fetch-agmarknet-vistaar-location` | `marketAndCommodityCodes` |

**Advisory (Knowledge)**

| use case | capability | provider | transport | auth | binding | enricher |
|---|---|---|---|---|---|---|
| **Schemes** | `KnowledgeResource` | `hasura-content` | HTTPS GraphQL | `apiKeyHeader` | `POST /v1/graphql` | `knowledgeQueryParams` |
| **Crop & pest** | `KnowledgeResource` | `oan-vector` | HTTP REST | `none` | `POST /indexes/oan-index/search` | `knowledgeQueryParams` |

**Two categories share one capability.** Schemes and Crop & pest are both
`openagrinet:KnowledgeResource`; they are separated at `discover` by a category filter, not by
a second capability code. Two bindings, same `capabilityCode`, different `providerId`.

**GraphQL needs no new field.** It is `POST` to one path with the query in the body, built by
the `requestMapping` — using GraphQL *variables*, never string concatenation.

**`oan-vector` is plaintext**, and legal only because `scheme: none`. Moving it behind TLS is
onboarding work, not a schema change, and should happen before real traffic.

**No transport discriminator in v1.** Every provider is HTTP. Adding one later is purely
additive: `"transport": "http"` on the binding, absent meaning `http`, existing records
unchanged. Naming a second value now would mean inventing semantics nobody has.

### Validated, not asserted

Run with `jsonschema` draft-07 against [`schemas/`](schemas/):

| | |
|---|---|
| The 5 v1 providers, 3 capabilities, 5 bindings | accept |
| 5 hypothetical newcomers — Bearer-token weather API, second mandi source, no-auth schemes REST, TLS+basic crop KB, `inline:`-secret provider | accept |
| 6 providers on one capability · 2 categories on one capability · 1 provider serving 2 capabilities · binding with no enricher | accept |
| Header injection via `valuePrefix` or `paramName`; `valuePrefix` with no separator; two credentials in one API-key scheme; `valuePrefix` on `basic` or `apiKeyQuery`; `paramName` on `none`; credential over plain HTTP; bare pasted secret; `bearer` as a scheme name; query string in `path`; `..` in a mapping path; `AgricultureResource` as a capability code; `PUT` on a binding | **reject** |
| CR, CRLF or a tab inside an `inline:` secret; `baseUrl` carrying a query string, userinfo, whitespace or `..`; a 100 000-character `name` or `path`; a `name` of only spaces | **reject** |

What it still accepts, checked and left accepted on purpose:

| accepted | why not a pattern's job |
|---|---|
| `baseUrl` of `https://localhost:8080/v1` or `https://169.254.169.254/…` | a regex cannot tell a metadata endpoint from a hostname, and `localhost` is what a developer's own deployment uses. An allowlist in the adapter can; see [§3.1](#31-provider) |
| an `apiKeyQuery` secret containing `&` or `#` | fixed by percent-encoding at call time. Banning the characters would reject a legitimate key instead |
| `path` of exactly `/` | a provider that answers at its root is not a mistake |
| a `providerId` that disagrees with its own `bindingKey` | JSON Schema cannot compare two fields — [§3.4](#34-two-rules-the-schema-cannot-express) rule 1 |

What the schema **cannot** catch is in [§3.4](#34-two-rules-the-schema-cannot-express), and
whether the resulting responses satisfy the OAN domain packs is in [dpg-fit.md](dpg-fit.md) —
where three of five bindings currently fail.

---

## Deferred / out of scope

Each of these was in an earlier draft and was measured out. **Re-adding any of them is a
schema addition, not a migration**: existing records keep validating, so nothing is bought by
carrying them before they have a user. That is the whole test applied below.

| absent | brought back by | cost when it arrives |
|---|---|---|
| `signing` — a provider's public key, for verifying what it sends **inbound** | a provider that runs its own adapter and signs its replies, or POSTs to a callback URL of ours. Then TLS proves we reached the host but not who wrote the body | one `Signing` definition, plus two cross-field rules (`keyId` segment 1 = `providerId`, segment 3 = `algorithm`; `validUntil` after `validFrom`) — it costs more integrity rules than the rest of `Provider` combined |
| `auth.login` / an acquired-token scheme | a provider whose credential is fetched by calling it (login → token → cache for a TTL) | one `Acquire` definition on `Auth`, and `Path` + `MappingPath` become shared into `Provider.json` |
| a second credential — `extraHeaders`, or `maxProperties > 1` | a provider wanting both an API key and a tenant token | relaxing one `maxProperties`, plus deciding how a second secret is redacted |
| `encryptedEnvelope` / a body codec | a provider that encrypts request bodies (PM-Kisan's AES envelope) | one plugin reference on the binding — the same mechanism as `enricher`, so no new concept |
| `steps[]` — 2–6 upstream calls for one Beckn action | one action needing call 2 to read call 1's output (PM-Kisan: verify OTP, then fetch the benefit) | an ordered array whose members are the fields the binding already has, and a `steps.<id>` scope in the JSONata inputs |
| `sessionGate` / `sessionGrant` | a gated action, where one call proves something a later one requires | **place the grant on the step that earns it**, never on the record — any step failing NACKs the whole action while the upstream has already consumed the OTP |
| an **action segment** on `bindingKey` | one (provider, capability) pair answering several Beckn actions from different endpoints — `init` and `status` on PM-Kisan, PMFBY or Soil Health Card | this is the **only** entry here that is not free. `bindingKey` is the unique index, so two actions on one pair collide today and the second write **silently overwrites** the first — JSON Schema cannot see a cross-record duplicate. Undoing it means either rewriting every stored key or carrying a dual lookup |

**Read that last row before deferring it again.** Everything above it is additive. The key's
shape is identity, and identity is the one thing a later schema addition cannot fix quietly.
The four v1 use cases are all `select`, which is why it is out — not because it is cheap.

A conformance check worth writing when the session fields return: **every gated scope must be
granted by some binding of the same provider.** An earlier draft of this document shipped an
example that violated it.

---

## Known gaps for v1

| | |
|---|---|
| **Advisory responses do not conform** | Both `KnowledgeResource` bindings emit private synonyms (`title`, `summary`, `url`, `publisher`) and omit five required pack fields. Mandi omits three. [dpg-fit.md](dpg-fit.md) has each violation and the fix. This is the largest open item on this page. |
| **`MandiPrice` may not be a real type** | The domain pack is named `MandiPriceObservation`; the docx's own information-mode examples say `MandiPrice`. One `Capability`, one binding and every filter carry whichever loses. Needs a ruling from the network owners. |
| **`/discover` matches outcome types only** | The domain packs sanction advertising a governed **capability** type (`WeatherObservationCapability`) as well as an `OnDemand` outcome resource. Filters that match only the outcome type make conformant providers invisible. Lands on discovery-service. |
| No min/max qualifier on `parameter` | `WeatherObservation.parameters` items require `{parameter, value, unit}` and are not closed, so *tomorrow's high is 30.6, low 22.1* is inexpressible — and every Indian weather upstream reports `tmin`/`tmax`. Mappings emit a private `aggregation`, which validates while meaning nothing to anyone else. |
| `informationMode` is **proposed, not governed** | It appears in no pack schema — only in the *Information Modes* section's examples, marked *Proposed terminology*. It still validates, since the packs are open at the top level, but no filter can rely on it. |
| Nothing re-pins `schemaUrl` | The three `Capability` records point at `3e593b3`. When network-specs moves, nothing notices. A check belongs in the seeding path. |
| Read access is not a distinct role | [§5](#5-apis) — `_osConfig.roles` gates the entity, not the verb. |
| `privateFields` is unverified | [§5](#5-apis) — whether RC redacts `$.auth.secrets` from `/search` on the pinned build has not been checked. |
| `oan-vector` on plain HTTP | Legal, but should move behind TLS. |
| Enricher names are unvalidated | A binding naming a plugin that does not exist fails at call time, not at boot. |
| The patterns need an ECMA-262 engine | Three of them use negative lookahead — `baseUrl` and `Capability.schemaUrl` to refuse `..` and a branch ref, `MappingPath` to refuse traversal. Ajv and Java both compile them; **Go's RE2 does not**, so a Go adapter validating these records locally has to implement those three rules in code rather than reuse the pattern. Nothing else in the three files is RE2-hostile — the length caps are written under RE2's 1000-repeat limit precisely so this stays a one-reason problem. |
| The evidence is not committed | Every rejection claimed in [§6](#6-do-todays-providers-fit) was produced by throwaway scripts in a scratchpad, and the `discover` dialect in [usecases.md](usecases.md) by a hand-run query against the dev database. Nothing re-runs them. A schema whose security controls are verified once, by hand, is a schema whose next edit is unchecked — this is the one gap on this page that will cost the most the soonest. |
| Shared definitions are copied, not referenced | RC loads each entity schema alone, so `Status`, `ProviderId`, `CapabilityCode` and `Secret` are duplicated verbatim across files ([§3.0](#30-shared-definitions)). Identical names make a drift a diff, but nothing yet fails a build on it. |
