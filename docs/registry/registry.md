# OpenAgriNet registry

The registry stores **participants, the schemas that describe a capability, and the binding
between them**. The binding is the call plan: given a participant and a capability, how do
you reach them.

**Everyone on the network is a participant.** A participant that has declared capabilities is
a provider; one that only consumes them is a consumer. That is a `roles` value, not a
different entity — the identity, the endpoint and the key material are the same shape either
way.

Nothing else lives here — no catalogs, no resources, no search index.

**Who reads it:** the adapter (or the adopter's experience layer).
**Who does not:** discovery-service. It answers `/discover` from its own catalog store.

| | | |
|---|---|---|
| 1 | [How it fits](#1-how-it-fits) | Two hops, which one reads the registry, and when |
| 2 | [Three schemas](#2-three-schemas) | What each one answers |
| 3 | [The schemas](#3-the-schemas) | The contract, field by field |
| 4 | [Examples](#4-examples) | One record per entity |
| 5 | [APIs](#5-apis) | Create, search, update, delete — with payloads |
| 6 | [Do today's participants fit?](#6-do-todays-participants-fit) | All 5 v1 providers, validated |
| → | [usecases.md](usecases.md) | Each use case end to end |
| → | [dpg-fit.md](dpg-fit.md) | Whether those use cases conform to the OAN domain packs |
| → | [Deferred](#deferred--out-of-scope) | What is deliberately absent, and what brings it back |
| A | [Why the schema is shaped this way](#appendix-a--why-the-schema-is-shaped-this-way) | Rationale, moved out of §3 so §3 stays reviewable |
| B | [What the adapter must do](#appendix-b--what-the-schema-cannot-reach-so-the-adapter-must) | Three checks no pattern can express |

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

**Hop ② is exactly two reads**, both single-field exact matches: the `ProviderSchema` by
`bindingKey`, then the `Participant` by `participantId`. No join, and no `SchemaRegistry` read —
`SchemaRegistry` is vocabulary, not part of the call path. Payloads in [§5](#5-apis).

**Adapter placement.** Either one adapter at the centre, or one adapter per participant
(experience-layer adapter calls `/discover`, then calls the provider's adapter for `select`).
Hop ② is identical in both. Two things change:

- **Who holds the upstream credentials.** With one central adapter, it does — every `auth`
  block in the registry is resolved in its environment. With per-participant adapters, each
  holds its own, and the central one holds none.
- **Whether a signature is worth verifying.** A central adapter calls the upstream directly,
  so TLS is the whole story. A participant's own adapter composes the Beckn reply itself, and
  TLS then proves only which host answered, not who wrote the body — which is what
  `publicKeys` ([§3.1](#31-participant)) is for.

The distributed shape is the direction of travel, and it is why `publicKeys` exists now rather
than in [Deferred](#deferred--out-of-scope).

### The runtime does not call these per request

```
13 records — 5 Participant, 3 SchemaRegistry, 5 ProviderSchema.  A few KB.
```

Load all three entities **at boot**, index `ProviderSchema` by `bindingKey` and `Participant`
by `participantId`. Resolution is then two map lookups and the per-request registry cost is zero
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

## 2. Three schemas

| schema | answers | v1 records |
|---|---|---|
| **`Participant`** | Who is this, where are they, and how do we authenticate to them? | 5 |
| **`SchemaRegistry`** | What outcome is this, in network vocabulary, and at which schema version? | 3 |
| **`ProviderSchema`** | Which endpoint of that participant answers that capability, and how is it shaped? | 5 |

They join on ids, not on foreign keys — Sunbird RC has none:

```
Participant.participantId     ─┐
                               ├─▶ ProviderSchema.bindingKey = "<participantId>|<capabilityCode>"
SchemaRegistry.capabilityCode ─┘
```

Schema files, which are the contract: [`schemas/Participant.json`](schemas/Participant.json) ·
[`schemas/SchemaRegistry.json`](schemas/SchemaRegistry.json) ·
[`schemas/ProviderSchema.json`](schemas/ProviderSchema.json).
All three are draft-07 with `additionalProperties: false` and an RC `_osConfig` block.

**This page describes those files; it does not restate them.** An inline copy of a schema is
a second thing to keep true, and it is the copy that rots.

---

## 3. The schemas

### 3.0 Shared definitions

RC loads each schema alone, so `$ref` across files is unavailable. These three are copied
**verbatim** into every file that uses them: edit one, edit all. Patterns live in
`schemas/*.json`.

| copied into every file that uses it | means | used by |
|---|---|---|
| `Status` | `active` \| `inactive` | all three |
| `ParticipantId` | lowercase, **min length 3** | `Participant`, `ProviderSchema` |
| `CapabilityCode` | `openagrinet:` + PascalCase, never the two `Agriculture*` base types | `SchemaRegistry`, `ProviderSchema` |

`MaterialRef`, `Secret`, `KeyHash`, `ParamName` and `PublicKey` live only in
`Participant.json`; `Path` and `MappingPath` only in `ProviderSchema.json`. None is shared
across files. `Secret` was, until the enricher was deferred out of milestone 1 and
`ProviderSchema` stopped holding key material — see [Deferred](#deferred--out-of-scope).

**One grammar for security material.** `auth.secrets` and `publicKeys[].hash` look like
unrelated fields, but neither holds material — both hold a *reference* to material held
outside the registry, in the same `<scheme>:<locator>` form. `MaterialRef` is that grammar;
`Secret` and `KeyHash` are it intersected with the schemes allowed at each site:

| reference | resolves to | legal in |
|---|---|---|
| `env://VAR` | the adapter's own environment | `Secret` |
| `inline:…` | the record itself — costs an authenticated `/search` | `Secret` |
| `sha256:<64 hex>` | the fingerprint of a key delivered out of band | `KeyHash` |

The narrowing is the point, not bureaucracy: a new scheme — `vault://`, a JWKS pointer — is
one edit to `MaterialRef` plus one deliberate entry in whichever site may use it. Widening the
grammar alone does **not** make `vault://` legal as a public-key hash, and
`verify/auth_cases.py` fails if it ever does. What the shared grammar deliberately does not do
is merge the two fields; [§3.1](#31-participant) says why that is unavailable rather than
merely unattractive.

### 3.1 `Participant`

Four scalars, a `roles` array, one `auth` object and the participant's public keys. Why:
[Appendix A](#participant--rationale).

```jsonc
{ "Participant": {                             // write bodies are wrapped: the key is the entity
  "participantId": "hasura-content",           // required · the Beckn provider.id · lowercase, min 3
  "name":       "Vistaar Knowledge Content",   // required · 1–200 chars, not all space · display only
  "roles":      ["provider"],                  // required · 1–2 of provider | consumer, unique
  "baseUrl":    "https://content.internal",    // required · scheme + host + path segments only:
                                               //   no trailing slash, no ? # @ whitespace ..
                                               //   https whenever a credential is held
  "status":     "active",                      // required · active | inactive
  "auth": {                                    // required · what WE present calling OUT to them
    "scheme":      "apiKeyHeader",             // required · none | apiKeyQuery | apiKeyHeader | basic
    "paramName":   "Authorization",            // per scheme · query-param or header name · no control chars
    "valuePrefix": "Bearer ",                  // optional · apiKeyHeader only · trailing space required
    "secrets":     { "token": "env://HASURA_TOKEN" }   // per scheme · a MaterialRef: env:// | inline:
  },
  "publicKeys": [{                             // optional in the schema, required by network policy
    "keyId": "hasura-content.k1",              // required inside publicKeys · lowercase
    "alg":   "ed25519",                        // required · ed25519 (signing) | x25519 (encryption)
    "hash":  "sha256:04df206b469a7fefd868d6bf40bb592b4359cbfc49f51404dfabba25c4a7a5c1"
  }]                                           // required · a MaterialRef: sha256: only · the hash, not the key
} }
```

**`auth` and `publicKeys` point in opposite directions.** `auth` is the credential *we*
present when we call *them*; `publicKeys` is what *we* verify when *they* sign something for
us. Different lifecycle, different owner, different blast radius when wrong — which is why
they are two fields and not one, even though both are `MaterialRef`s ([§3.0](#30-shared-definitions))
and the resemblance invites merging them.

The reason they cannot merge is `privateFields`, which redacts **by path**. One
`credentials[]` array would need `$.credentials[*].secrets` — the wildcard we cannot rely on —
and even resolved, the rule would have to redact the outbound element and not the inbound one.
RC cannot say that, so a merged array is either fully redacted, making public keys unreadable
and verification impossible, or not redacted, putting secrets in clear. Both fail silently.
Cardinality points the same way: `publicKeys` is plural because rotation needs a window where
the old and new keys are both valid, while a credential rotates by changing one env var.

`publicKeys` is an **array** even though `auth.secrets` deliberately is not. A public key is
not a secret: it needs no `Secret` pattern and no `privateFields` entry, so it never runs into
the wildcard-JSONPath problem that makes an array unsafe for key material
([Appendix C](#appendix-c--adding-an-auth-scheme) step 3).

One credential is the common case, and `paramName` names where it goes. An upstream needing
**two** — a key plus an account id — swaps `paramName` for `paramNames`, which maps each
secret to its own header or query parameter:

```jsonc
{ "Participant": {
  "participantId": "two-header-upstream",
  "name":          "An upstream wanting a key and an account id",
  "roles":         ["provider"],
  "baseUrl":       "https://api.example.gov.in/v1",
  "status":        "active",
  "auth": {
    "scheme":     "apiKeyHeader",
    "secrets":    { "token": "env://UPSTREAM_TOKEN", "account": "env://UPSTREAM_ACCOUNT" },
    "paramNames": { "token": "X-Api-Key",            "account": "X-Account-Id" }
                              // one entry per secret, keyed by the same name
  }
} }
```

Exactly one of `paramName` or `paramNames` — never both, never neither. `paramNames` takes no
`valuePrefix`; an upstream needing a prefix on one of several values is the case this does not
cover. `secrets` stays a flat object at a fixed path either way, which is what lets
`privateFields: ["$.auth.secrets"]` encrypt one credential or five without change.

`scheme` decides which of the other three may appear at all:

| `scheme` | the adapter does | then requires | and must not carry |
|---|---|---|---|
| `none` | sends no credential | — | `secrets`, `paramName`, `valuePrefix` |
| `apiKeyQuery` | appends `?<paramName>=<secret>` | `paramName`, exactly one `secrets` entry | `valuePrefix` |
| `apiKeyHeader` | sets `<paramName>: <valuePrefix><secret>` | `paramName`, exactly one `secrets` entry | — |
| `basic` | RFC 7617 from `secrets.username` / `secrets.password` | both keys | `paramName`, `valuePrefix` |

**`secrets` values take two forms and must say which** — a bare key is rejected at write
time. Prefer `env://`; the cost of `inline:` is in [examples.md](examples.md#participants).

```
"secrets": { "password": "env://MAUSAMGRAM_PASS" }   // pointer — resolved from the adapter's environment
"secrets": { "token":    "inline:a7f3c9d2e1b8..." }  // the credential itself, stored here
```

---

### 3.2 `SchemaRegistry`

Why: [Appendix A](#schemaregistry--rationale).

```jsonc
{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:WeatherObservation",   // required · the outcome @type
  "name":           "Weather Observation and Forecast", // required · 1–200 chars · display only
  "version":        "v0.1",                             // required · vN.N · the schema version in use
  "schemaUrl":      "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/WeatherObservation/v0.1/attributes.yaml",
  "status":         "active"                            // required · active | inactive
} }
```

**The version is in the path, so the branch does not have to be pinned.** `schemaUrl` must
match `network-specs/<ref>/schema/<Type>/vN.N/<file>.yaml`, and it is the `vN.N` segment that
makes the contract stable: `v0.1` on `main` is the same document next week, because a breaking
change to the schema is published as `v0.2` rather than as an edit in place. Pinning a commit
sha would make the registry a mirror of the specs repo — every push to `network-specs` would
be a registry write, for no gain over a version directory that never changes meaning.

`version` is stored as well as being in the URL so the version is a queryable field rather
than something a reader has to parse out of a 2000-character string. That duplication is the
reason for [§3.4](#34-five-rules-the-schema-cannot-express) rule 4.

> Outcome type or governed capability type is an **open alignment question**, and one seeded
> record matches neither — [dpg-fit.md](dpg-fit.md).

---

### 3.3 `ProviderSchema`

A record is **one call** — one shape, no alternatives. Why:
[Appendix A](#providerschema--rationale).

```jsonc
{ "ProviderSchema": {
  "bindingKey":      "imd-city-weather|openagrinet:WeatherObservation",  // required · participantId + "|" + capabilityCode
                                                     //   the unique index, and the adapter's first read
  "participantId":      "imd-city-weather",             // required · must equal segment 1
  "capabilityCode":  "openagrinet:WeatherObservation",  // required · must equal segment 2
  "status":          "active",                       // required · active | inactive
  "method":          "GET",                          // required · GET | POST
  "path":            "/citywx/city_weather_test.php",   // required · appended to Participant.baseUrl · no query string
  "requestMapping":  "mappings/imd-city-weather/select.request.jsonata",   // required
  "responseMapping": "mappings/imd-city-weather/select.response.jsonata",  // required
  "timeoutMs": 15000,                                // optional · 1000–120000, default 15000
  "retryMax":  0                                     // optional · 0–5, default 0 · not conditioned on method
} }
```

**There is no `enricher` field.** A per-provider transform step was in an earlier draft and is
out of milestone 1: a plugin name in the registry reads as a network-wide contract, and an
adapter that has never heard of `nearestStation` cannot honour it. The transforms still happen
— they are adapter-internal, and unnamed here. [Deferred](#deferred--out-of-scope) carries
what brings the field back.

**Mappings are file paths**, `mappings/<participant>/<action>.<request|response>.jsonata` —
lowercase, no `..`.

| mapping | input | output |
|---|---|---|
| `requestMapping` | `{beckn, _local}` | the upstream request |
| `responseMapping` | `{beckn, _local, response}` | Beckn v2 resources |

`_local` is whatever the adapter computed before the mapping ran. With no enricher field, what
fills it is the adapter's business; the mapping contract is unchanged either way.

---

### 3.4 Five rules the schema cannot express

JSON Schema cannot compare two fields, and RC enforces no reference between entities. These
run in the onboarding path and in the conformance suite:

1. `bindingKey` **must equal** `participantId` + `"|"` + `capabilityCode`.
2. Both must resolve to **live** records — an `active` `Participant` and an `active` `SchemaRegistry`.
3. Where `auth.paramNames` is used, its keys **must be exactly** the keys of `auth.secrets`.
   Draft-07 cannot reference a sibling field, and both mismatches fail quietly: a name with no
   secret sends a header with no value, a secret with no name is never sent at all.
4. `version` **must equal** the `vN.N` segment of `schemaUrl`. Both are stored, both are
   patterned, and draft-07 cannot check that they agree — so the failure mode is a record that
   validates while advertising `v0.1` and resolving `v0.2`.
5. `publicKeys[].keyId` **must be unique** within the array. `uniqueItems` compares whole
   objects, so two entries with the same `keyId` and different hashes both pass — and a
   verifier looking up a key by id then gets whichever it found first.

**Every participant that receives a signed network call must carry at least one
`publicKeys` entry.** That is network policy, and the schema cannot enforce it because it
cannot tell a participant that terminates Beckn calls from an upstream data API that our own
adapter reaches directly. None of the five v1 records carries one —
[Known gaps](#known-gaps-for-v1).

One more is unbuilt: `responseMapping` conformance. See [Known gaps](#known-gaps-for-v1).

---

## 4. Examples

One record per entity. The thirteen actually seeded are in **[examples.md](examples.md)**;
each one traced end to end is in **[usecases.md](usecases.md)**.

**`Participant`** — `apiKeyHeader` with a prefix, the shape a Bearer-token upstream takes

```json
{ "Participant": {
  "participantId": "example-participant",
  "name": "A provider behind a bearer token",
  "roles": ["provider"],
  "baseUrl": "https://api.example.gov.in/v2",
  "status": "active",
  "auth": { "scheme": "apiKeyHeader",
            "paramName": "Authorization",
            "valuePrefix": "Bearer ",
            "secrets": { "token": "env://EXAMPLE_TOKEN" } },
  "publicKeys": [ { "keyId": "example-participant.k1", "alg": "ed25519",
                    "hash": "sha256:04df206b469a7fefd868d6bf40bb592b4359cbfc49f51404dfabba25c4a7a5c1" } ]
} }
```

The other three schemes in full:

```json
{ "scheme": "none" }
{ "scheme": "apiKeyQuery",  "paramName": "token", "secrets": { "token": "env://EXAMPLE_TOKEN" } }
{ "scheme": "basic", "secrets": { "username": "env://EXAMPLE_USER", "password": "env://EXAMPLE_PASS" } }
```

**`SchemaRegistry`** — every property

```json
{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:WeatherObservation",
  "name": "Weather Observation and Forecast",
  "version": "v0.1",
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active"
} }
```

**`ProviderSchema`** — every property, which is also the shape all five v1 bindings use

```json
{ "ProviderSchema": {
  "bindingKey": "example-participant|openagrinet:WeatherObservation",
  "participantId": "example-participant",
  "capabilityCode": "openagrinet:WeatherObservation",
  "status": "active",

  "method": "GET",
  "path": "/get-daily",
  "requestMapping":  "mappings/example-participant/select.request.jsonata",
  "responseMapping": "mappings/example-participant/select.response.jsonata",

  "timeoutMs": 30000,
  "retryMax": 3
} }
```

There is no second, larger example to show. Every field either appears above or does not
exist.

---

## 5. APIs

Sunbird RC generates the REST surface from the three schemas. `<Entity>` is `Participant`,
`SchemaRegistry` or `ProviderSchema`.

| route | who | what |
|---|---|---|
| `POST /api/v1/<Entity>` | `registryOperator` | create |
| `POST /api/v1/<Entity>/search` | authenticated | look up by indexed field |
| `GET /api/v1/<Entity>/{osid}` | authenticated | read one |
| `PUT /api/v1/<Entity>/{osid}` | `registryOperator` | replace in full |
| `DELETE /api/v1/<Entity>/{osid}` | — | **disabled** — see [below](#delete--disabled) |

All three schemas declare `"roles": ["registryOperator"]`. RC's `_osConfig.roles` gates the
entity, not the verb, so the *who* column above is intent, not enforcement — see
[Known gaps](#known-gaps-for-v1).

`osid` is RC's row id, returned by the create. It is **not** `participantId` and not
`bindingKey` — so an update has to search first.

### Create

```http
POST /api/v1/Participant
Authorization: Bearer <operator-token>
Content-Type: application/json
```
```json
{ "Participant": {
  "participantId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "roles": ["provider"],
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
  "result": { "Participant": { "osid": "1-8f2c4e7a-3b91-4d0e-9c55-2a1f6b8e0d34" } } }
```

**The body is wrapped**, one level down under the entity name — which is exactly what each
schema's top level requires (`required: ["Participant"]`). The same form validates locally and
goes on the wire, so the records in [examples.md](examples.md) are write bodies as they
stand.

### Search

```http
POST /api/v1/ProviderSchema/search
Authorization: Bearer <read-token>
```
```json
{ "filters": { "bindingKey": { "eq": "agmarknet|openagrinet:MandiPrice" },
               "status":     { "eq": "active" } } }
```
```json
200 OK
[ { "osid": "1-4c7d...", "bindingKey": "agmarknet|openagrinet:MandiPrice",
    "participantId": "agmarknet", "capabilityCode": "openagrinet:MandiPrice",
    "method": "GET", "path": "/v1/fetch-agmarknet-vistaar-location",
    "requestMapping": "mappings/agmarknet/select.request.jsonata",
    "responseMapping": "mappings/agmarknet/select.response.jsonata",
    "timeoutMs": 20000, "retryMax": 2, "status": "active" } ]
```

> The two response bodies above are **illustrative shapes, not captured output** — whether
> RC returns the rows bare or wrapped, and what it puts around them, has not been checked
> against the pinned build. The requests are the part the archive corroborates.

**`search` is not public.** A record may hold an `inline:` credential, so a read of `Participant`
is a read of live key material.

> Assume this response carries the credential. `privateFields` should redact
> `$.auth.secrets` and that is unverified on the pinned build —
> [Known gaps](#known-gaps-for-v1).

### Update

Replace in full — RC's `PUT` is not a merge patch. Search for the `osid`, change the field,
send the whole record back.

```http
PUT /api/v1/Participant/1-8f2c4e7a-3b91-4d0e-9c55-2a1f6b8e0d34
Authorization: Bearer <operator-token>
```
```json
{ "Participant": {
  "participantId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "roles": ["provider"],
  "baseUrl": "https://api.agmarknet.gov.in",
  "status": "inactive",
  "auth": { "scheme": "apiKeyQuery",
            "paramName": "token",
            "secrets": { "token": "env://MANDI_TOKEN_2026" } } } }
```

Rotating an `env://` pointer is a registry write; rotating the value behind it is not.
Rotating an `inline:` credential is always a registry write. **Because `PUT` replaces, a
field you omit is a field you delete** — `publicKeys` most dangerously, since dropping it
turns signature verification off rather than failing loudly.

### Delete — disabled

**There is no delete.** The route is closed at the gateway; no token carries the right to call
it. Deactivate instead:

```
PUT /api/v1/Participant/{osid}     → the same record with "status": "inactive"
```

Three reasons, in order of what they cost when ignored:

1. **A delete orphans silently.** RC enforces no reference between entities, so removing a
   `Participant` leaves its `ProviderSchema` rows pointing at nothing. They still validate,
   and they still resolve by `bindingKey`. The call fails at request time with no clue that
   the cause was a registry write weeks earlier.
2. **Published catalogs outlive the record.** Resources already advertised through
   `/discover` carry a `provider.id`; deleting the row that explains that id makes them
   unresolvable without making them disappear.
3. **`inactive` is as complete, and leaves evidence.** Every read filters
   `status == "active"`, so flipping the flag takes a participant out of service just as
   totally — and leaves the row where an operator can see *what* was turned off.

A genuine erasure — an onboarding mistake, or a participant exercising a removal right — is
an operator task against the database with a reason recorded, not an API call anyone holds a
token for.

---

## 6. Do today's participants fit?

Yes — all four v1 categories, all five providers, all five bindings. No field left over and
nothing forced.

**Realtime Information**

| use case | capability | participant | transport | auth | binding | adapter must also |
|---|---|---|---|---|---|---|
| **Weather** — point forecast | `WeatherObservation` | `mausamgram` | HTTPS REST | `basic` | `GET /get-daily` | derive a point from the intent |
| **Weather** — city / station | `WeatherObservation` | `imd-city-weather` | HTTPS REST | `none` | `GET /citywx/city_weather_test.php` | resolve the nearest station (own datastore) |
| **Mandi prices** | `MandiPrice` | `agmarknet` | HTTPS REST | `apiKeyQuery` | `GET /v1/fetch-agmarknet-vistaar-location` | look up market and commodity codes |

**Advisory (Knowledge)**

| use case | capability | participant | transport | auth | binding | adapter must also |
|---|---|---|---|---|---|---|
| **Schemes** | `KnowledgeResource` | `hasura-content` | HTTPS GraphQL | `apiKeyHeader` | `POST /v1/graphql` | build the knowledge query params |
| **Crop & pest** | `KnowledgeResource` | `oan-vector` | HTTP REST | `none` | `POST /indexes/oan-index/search` | build the knowledge query params |

**The last column is not a registry field.** Each of the five needs a transform the Beckn
body cannot express — a private code namespace, or a lookup against something the adapter
owns. That used to be a named `enricher` on the binding; it is now adapter-internal, because
a plugin name in the registry reads as a contract the whole network can honour and it is not
one. Nothing about the five upstreams changed; only who is told about the step.

**Two categories share one capability.** Schemes and Crop & pest are both
`openagrinet:KnowledgeResource`; they are separated at `discover` by a category filter, not by
a second capability code. Two bindings, same `capabilityCode`, different `participantId`.

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
| 6 participants on one capability · 2 categories on one capability · 1 participant serving 2 capabilities | accept |
| Header injection via `valuePrefix` or `paramName`; `valuePrefix` with no separator; two credentials in one API-key scheme; `valuePrefix` on `basic` or `apiKeyQuery`; `paramName` on `none`; credential over plain HTTP; bare pasted secret; `bearer` as a scheme name; query string in `path`; `..` in a mapping path; `AgricultureResource` as a capability code; `PUT` on a binding | **reject** |
| CR, CRLF or a tab inside an `inline:` secret; `baseUrl` carrying a query string, userinfo, whitespace or `..`; a 100 000-character `name` or `path`; a `name` of only spaces | **reject** |

What it still accepts, checked and left accepted on purpose:

| accepted | why not a pattern's job |
|---|---|
| `baseUrl` of `https://localhost:8080/v1` or `https://169.254.169.254/…` | a regex cannot tell a metadata endpoint from a hostname, and `localhost` is what a developer's own deployment uses. An allowlist in the adapter can; see [§3.1](#31-participant) |
| an `apiKeyQuery` secret containing `&` or `#` | fixed by percent-encoding at call time. Banning the characters would reject a legitimate key instead |
| `path` of exactly `/` | a provider that answers at its root is not a mistake |
| a `participantId` that disagrees with its own `bindingKey` | JSON Schema cannot compare two fields — [§3.4](#34-five-rules-the-schema-cannot-express) rule 1 |

What the schema **cannot** catch is in [§3.4](#34-five-rules-the-schema-cannot-express), and
whether the resulting responses satisfy the OAN domain packs is in [dpg-fit.md](dpg-fit.md) —
where three of five bindings currently fail.

---

## Deferred / out of scope

Each of these was in an earlier draft and was measured out. **Re-adding any of them is a
schema addition, not a migration**: existing records keep validating, so nothing is bought by
carrying them before they have a user. That is the whole test applied below.

| absent | brought back by | cost when it arrives |
|---|---|---|
| **`enricher`** — a named transform run before the request mapping | an agreement that the *network* defines the transform set, not one adapter. Today a name like `nearestStation` in a shared registry claims every adapter can run it, and none can. The step itself has not gone anywhere — it is adapter-internal ([§6](#6-do-todays-participants-fit)) | one `Enricher` definition back on `ProviderSchema`, `Secret` shared into that file again, one `privateFields` path, and the resolution check that was never built. Additive: the five bindings validate with and without it |
| `publicKeys[].validFrom` / `validUntil` — key lifetime | the first key rotation that has to overlap, where the old key must stay verifiable while the new one propagates | two fields on `PublicKey` and one cross-field rule (`validUntil` after `validFrom`). Until then a rotation is a `PUT` and the overlap window is however long that takes |
| `signing` — an algorithm and key-id convention beyond `publicKeys` | a participant signing with something other than ed25519, or a `keyId` the network wants structured (`<participantId>.<n>.<alg>`) | `alg` is already an enum, so a new algorithm is one value; a structured `keyId` is a tighter pattern plus a cross-field rule against `participantId` |
| `auth.login` / an acquired-token scheme | a provider whose credential is fetched by calling it (login → token → cache for a TTL) | one `Acquire` definition on `Auth`, and `Path` + `MappingPath` become shared into `Participant.json` |
| **more than one auth *scheme* per participant** — not more than one credential, which `paramNames` already carries | a participant that genuinely needs two schemes at once, or a staged rotation across schemes | **fixed slots, never an array.** `$.auth.primary.secrets` and `$.auth.secondary.secrets` are literal paths that `privateFields` can redact; `$.authMethods[*].secrets` is a wildcard we have no evidence RC resolves, and its failure mode is key material stored in clear with no error ([§3.1](#31-participant)) |
| `encryptedEnvelope` / a body codec | a provider that encrypts request bodies (PM-Kisan's AES envelope) | one plugin reference on the binding — the same mechanism the `enricher` row above describes, so no new concept, but it lands after that one |
| `steps[]` — 2–6 upstream calls for one Beckn action | one action needing call 2 to read call 1's output (PM-Kisan: verify OTP, then fetch the benefit) | an ordered array whose members are the fields the binding already has, and a `steps.<id>` scope in the JSONata inputs |
| `sessionGate` / `sessionGrant` | a gated action, where one call proves something a later one requires | **place the grant on the step that earns it**, never on the record — any step failing NACKs the whole action while the upstream has already consumed the OTP |
| an **action segment** on `bindingKey` | one (participant, capability) pair answering several Beckn actions from different endpoints — `init` and `status` on PM-Kisan, PMFBY or Soil Health Card | this is the **only** entry here that is not free. `bindingKey` is the unique index, so two actions on one pair collide today and the second write **silently overwrites** the first — JSON Schema cannot see a cross-record duplicate. Undoing it means either rewriting every stored key or carrying a dual lookup |
| `baseTypes` — the abstract types a capability composes | a consumer that needs to match on `openagrinet:AgricultureResource` without enumerating its subtypes | nothing here. The type hierarchy lives in `network-specs` and is resolvable from `schemaUrl`; mirroring it into the registry meant a registry write on every specs push, which is what removed it |

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
| **`MandiPrice` may not be a real type** | The domain pack is named `MandiPriceObservation`; the docx's own information-mode examples say `MandiPrice`. One `SchemaRegistry`, one binding and every filter carry whichever loses. Needs a ruling from the network owners. |
| **`/discover` matches outcome types only** | The domain packs sanction advertising a governed **capability** type (`WeatherObservationCapability`) as well as an `OnDemand` outcome resource. Filters that match only the outcome type make conformant providers invisible. Lands on discovery-service. |
| No min/max qualifier on `parameter` | `WeatherObservation.parameters` items require `{parameter, value, unit}` and are not closed, so *tomorrow's high is 30.6, low 22.1* is inexpressible — and every Indian weather upstream reports `tmin`/`tmax`. Mappings emit a private `aggregation`, which validates while meaning nothing to anyone else. |
| `informationMode` is **proposed, not governed** | It appears in no pack schema — only in the *Information Modes* section's examples, marked *Proposed terminology*. It still validates, since the packs are open at the top level, but no filter can rely on it. |
| Nothing resolves `schemaUrl` | The three records name a version directory that is meant never to change meaning. Nothing fetches the URL to confirm it exists, and nothing checks [§3.4](#34-five-rules-the-schema-cannot-express) rule 4 — that `version` and the URL's `vN.N` segment agree. Both belong in the seeding path. |
| **Two capabilities on one participant collide on mapping filenames** | [§3.3](#33-providerschema) defines mapping paths as `mappings/<participant>/<action>.<request|response>.jsonata` — participant and action, no capability segment. A provider serving two capabilities from the same action resolves both to one filename while needing two different output shapes. Nothing rejects it: `MappingPath` is a pattern over a string, and [§6](#6-do-todays-participants-fit)'s matrix accepts *1 participant serving 2 capabilities* — it validated the records, not the filenames. Worked through in [usecases.md use case 6](usecases.md#use-case-6--weather-advisory-mausamgram--not-seeded). The fix is a capability segment in the convention; it is not a schema change. |
| Read access is not a distinct role | [§5](#5-apis) — `_osConfig.roles` gates the entity, not the verb. |
| `privateFields` is unverified | [§5](#5-apis) — whether RC redacts `$.auth.secrets` from `/search` on the pinned build has not been checked. |
| `oan-vector` on plain HTTP | Legal, but should move behind TLS. |
| **Path or subdomain for a second endpoint** | Open, and not decided. A participant with one identity may expose different capabilities on different *hosts*, not merely different paths — IMD is `imd.gov.in` but the weather API sits elsewhere. Today one `Participant` is one `baseUrl`, so this is two records sharing an identity prefix, which makes the same organisation look like two participants on the wire. The alternatives are a `baseUrl` per binding, or a host override on `ProviderSchema`; both are additive, and choosing before a real case arrives means inventing semantics. Recorded here so it is not rediscovered as a bug. |
| **No participant has a registered key** | `publicKeys` is in the schema and network policy requires it, but none of the five v1 records carries one — they are upstream data APIs our own adapter calls directly, not participants that sign anything. Under the distributed topology each of them runs its own adapter and does sign, at which point the field is mandatory and the seeding path must enforce it. This is the gap most likely to be read as *done* because the field exists. |
| The patterns need an ECMA-262 engine | Two of them use negative lookahead — `baseUrl` to refuse `..`, `MappingPath` to refuse traversal. Ajv and Java both compile them; **Go's RE2 does not**, so a Go adapter validating these records locally has to implement those two rules in code rather than reuse the pattern. `schemaUrl` used to be a third and is not: restricting the ref segment to `[A-Za-z0-9_-]+` excludes a dot, so `..` is unreachable without a lookahead at all. Nothing else in the three files is RE2-hostile — the length caps are written under RE2's 1000-repeat limit precisely so this stays a one-reason problem. |
| The evidence is not committed | Every rejection claimed in [§6](#6-do-todays-participants-fit) was produced by throwaway scripts in a scratchpad, and the `discover` dialect in [usecases.md](usecases.md) by a hand-run query against the dev database. Nothing re-runs them. A schema whose security controls are verified once, by hand, is a schema whose next edit is unchecked — this is the one gap on this page that will cost the most the soonest. |
| Shared definitions are copied, not referenced | RC loads each entity schema alone, so `Status`, `ParticipantId` and `CapabilityCode` are duplicated verbatim across files ([§3.0](#30-shared-definitions)). Identical names make a drift a diff, but nothing yet fails a build on it. |

---

## Appendix A — Why the schema is shaped this way

Rationale only. Nothing here is a constraint; every constraint is in [§3](#3-the-schemas)
and in `schemas/*.json`. This exists so that a later simplification is a decision rather
than an accident.

### `Participant` — rationale

**Why `baseUrl` refuses those five.** A leading `/` on `path` and none trailing on `baseUrl`
means exactly one `/` falls between them, so nothing normalises — and each refusal is a URL
`baseUrl + path` would otherwise have produced that nobody wrote:

| refused | because the concatenation would |
|---|---|
| `?` or `#` | `…/v1?tenant=a` + `/get-daily` puts the path **inside the query string** |
| `@` (userinfo) | `https://user:pass@host` is a credential outside `auth.secrets` — so outside `privateFields`, and into every `/search` response and every log line that prints the URL |
| whitespace | be unusable, and silently mangled differently by every HTTP client |
| `..` | traverse against the upstream, from a field nobody reads as a path |

`path` forbids `?` from the other side: a query string belongs to the `requestMapping`, which
builds it from the request, so no value reaches the wire by being concatenated into a stored
string.

**One `status` vocabulary across all three entities.** Every read filters
`status == "active"`; a `SchemaRegistry` that said `deprecated` instead of `inactive` meant the
same thing to the reader and a different string to the filter.

**Auth is on `Participant`, not on the binding.** One credential opens all of that
participant's endpoints — true for all five. On `ProviderSchema` it would be copied into every binding
row, and a rotation would touch N rows instead of one.

`Participant.baseUrl` is **where** the provider is; `method` + `path` on the binding is **which
endpoint** answers a capability. They are split because one provider serves several
capabilities from different paths — put `path` on `Participant` and each needs a duplicate
record, which means a duplicate credential.

```
https://mausamgram.imd.gov.in/nwpapi  +  /get-daily
└────────── Participant.baseUrl ───────┘     └─ ProviderSchema.path ─┘
```

**There is no `bearer` scheme, because `valuePrefix` is one.** `Authorization: Bearer <token>`
is `apiKeyHeader` with `paramName: "Authorization"`, `valuePrefix: "Bearer "` — and the same
field expresses `Token `, `ApiKey `, or whatever the next upstream invents. The trailing space
is part of the value so that the wire format is visible in the record rather than being a
separator convention living in adapter code.

**The three tight patterns are anti-header-injection, and that is their only purpose.**
`paramName`, `valuePrefix` and an `inline:` secret are written into an HTTP header verbatim, so
a `\r\n` in any of them appends attacker-chosen headers. None admits a control character.
Relaxing any of the three to "a non-empty string" reopens that.

**One credential per API-key scheme**, via `maxProperties: 1`. A provider needing both an API
key and a tenant token then arrives as a reviewed schema change, not an extra map entry — see
[Deferred](#deferred--out-of-scope).

**Why `auth` is on `Participant` and not on `SchemaRegistry` or the binding.** A credential
authenticates you to a *host*, and this is the only record that names one — splitting
`baseUrl` from `auth` would mean two records must both be right for one call to work.
`SchemaRegistry` names a data type, not an endpoint: `openagrinet:WeatherObservation` is served
by `mausamgram` (`basic`) and `imd-city-weather` (`none`), so one `auth` there would have to
be both at once. It is also network-governed and carries no secrets, so auth would put key
material on the most widely read entity. On `ProviderSchema` it would validate, but a
provider authenticates the same way for everything it serves, so the credential would be
duplicated across that provider's bindings and rotation would become an N-row write — the
row you miss is an outage with a stale credential. The test to apply to any future proposal:
*what changes when the upstream rotates the key?* Here, one row.

One host that needs different keys per endpoint — separately subscribed products on a shared
platform — is the case this shape does not cover. No v1 provider is like that, and it needs
no schema change when one appears: two `Participant` rows sharing a `baseUrl`. Since
`participantId` **is** the Beckn `provider.id`, separately credentialed products appearing as
separate providers on the wire is the more faithful representation anyway.

### `SchemaRegistry` — rationale

**Nothing names a participant here.** A capability is network vocabulary; the binding attaches
it to a participant.

**The stable thing is the version directory, not the git ref.** An earlier draft pinned a full
commit sha and rejected `main` outright, on the reasoning that a branch ref means the contract
you validated against last week is not the one you validate against today. That is true of a
branch and false of `schema/<Type>/v0.1/` on a branch: a version directory is immutable by
convention, and a breaking change is published as `v0.2`. The sha bought immutability at the
price of making this registry a mirror of `network-specs` — every push there would have been a
reviewed write here, in three records, forever. The pattern now requires the `vN.N` segment
and leaves the ref alone, which puts the guarantee where the specs repo can actually keep it.

**Why `version` is stored when it is already in the URL.** `indexFields` and every human
reader want a short comparable token, not a 2000-character string to substring. The
duplication is the reason for rule 4 in [§3.4](#34-five-rules-the-schema-cannot-express) — a
field that repeats another field always needs a rule saying they agree.

**Why there are no `baseTypes`.** They described the type hierarchy — that
`WeatherObservation` composes `AgricultureResource` — which is a fact about `network-specs`,
resolvable from `schemaUrl`. Mirrored here it becomes a second copy to keep true, and the copy
that rots: a new abstract type upstream is a registry write in every affected record. The
`CapabilityCode` pattern still refuses the two `Agriculture*` names, so a base type cannot be
bound as if it were a concrete capability.

### `ProviderSchema` — rationale

**Why `bindingKey` exists when it only repeats two other fields.** It is the row's identity.
RC's `uniqueIndexFields` takes single fields that are each unique on their own — there is no
composite unique index, and what support exists varies by release. So "one row per
(`participantId`, `capabilityCode`)" cannot be expressed without collapsing the pair into one
field. Without it the duplicate is not an error but an **overwrite**: two records that differ
in no indexed field both validate, and the second write silently replaces the first. JSON
Schema cannot detect a duplicate *across* records — it can only forbid the shape that allows
one, which is why [§3.4](#34-five-rules-the-schema-cannot-express) rule 1 checks the three
fields agree.

**Why there is no `enricher`, and what it would have been for.** The distinction is still
real: some things the Beckn body cannot express — a private code namespace (Agmarknet's
`marketcode`), or a lookup against something the adapter owns (a station table in Postgres) —
and *if a JSONata expression can do it, it is a mapping, not an enricher.* What the registry
cannot do is **name** that step. A registry is shared by every participant, so a plugin name
in it asserts that any adapter can run that plugin; an adapter that has never heard of
`nearestStation` reads a binding it cannot honour and has no way to say so. The step therefore
lives in the adapter, keyed off `participantId`, and the registry stores the two mappings
either side of it. [Deferred](#deferred--out-of-scope) says what would bring the field back —
a network-level agreement on the transform set, not a bigger schema.

**Why mappings are files and not rows.** The row stores the path; the file is reviewed and
diffed like source. The pattern rejects uppercase because a case-only difference resolves on
macOS and 404s on a Linux pod.

**`GET` and `POST` only.** Every binding here answers a read. A `PUT` or `DELETE` in a
discovery path is a bug, and an enum is a cheaper place to catch it than a review.

**Timeout and retry are registry columns, not constants in a service class.** IMD gets 30 s
and 3 retries; Hasura gets 15 s and none. Those are properties of the upstream, changed by an
operator.

> `retryMax` is not conditioned on `method`, deliberately. The obvious rule — *no retries on
> `POST`* — would forbid retrying a GraphQL **read**, which is the single most common POST in
> this network. The judgement stays with whoever writes the record.

---

## Appendix C — Adding an auth scheme

`scheme` is a closed enum, and deliberately: the set of methods the registry can express
should equal the set the adapter can perform. Expressible-but-not-performable is a runtime
failure against a live upstream; performable-but-not-expressible is dead code. So a new
method is a reviewed change in two places, and this is the order.

1. **Add the enum value** to `Auth.properties.scheme`.
2. **Give it a nested config object**, not new top-level fields on `auth` — `"oauth2": { … }`
   with `additionalProperties: false`. Then two conditional branches: one requiring the object
   for that scheme and forbidding the fields it does not use, one forbidding the object for
   every other scheme. Without the second branch `auth` becomes a junk drawer where every
   scheme carries every other scheme's fields.
3. **Reference `#/definitions/Secret`** for anything holding key material, and **add the
   `privateFields` path** — `$.auth.<scheme>.secrets`. Keep secrets in a flat object at a
   fixed path; an array needs a JSONPath wildcard and there is no evidence RC resolves one,
   so the failure mode is key material silently stored in clear.
4. **Write the adapter handler.** This is the real cost and no schema shape avoids it.

Measured on `oauth2ClientCredentials` as a prototype: 94 lines added to a 274-line file, all
five seeded `Participant` records still valid, **no migration**. The extension is purely
additive — nothing existing is edited. The guard rails came free: a bare pasted
`clientSecret` was refused without a new rule, because `Secret` was referenced rather than
copied.

**What does not belong here.** A per-request *computation* — HMAC or any request signing —
cannot be expressed declaratively at any level of generality, because the value depends on
the final method, path, body and timestamp. Note it could not have lived in an `enricher`
either, even when that field existed: enrichers run on the intent, before the mapping builds
the request, so there are no final bytes to sign. Signing belongs in the adapter's HTTP layer,
selected by `scheme`, with only the genuinely varying parameters in the record.

**Also not here: verifying an inbound signature.** That is `publicKeys`, a different field in
a different direction ([§3.1](#31-participant)). `auth` describes what we send; nothing in
`Auth` describes what we accept.

**A second URL** (an OAuth2 `tokenUrl`) belongs in the scheme's config object, not beside
`baseUrl`. `baseUrl` is the capability endpoint's host and the binding's `path` is resolved
against it; a token endpoint is neither.

And the case this shape does not cover, restated because it is the one most likely to be
mistaken for an oversight: **one host needing different credentials per capability.**
`Participant.auth` is one credential for every call to that provider. Model it as two
`Participant` records sharing a `baseUrl` — the cost is a duplicated host, paid on the rare
path rather than the routine one. If it ever becomes routine, the additive fix is an optional
`authOverride` on `ProviderSchema` with `Participant.auth` as the default: one more
`privateFields` path and a precedence rule in the adapter. Nothing needs it today.

---

## Appendix B — What the schema cannot reach, so the adapter must

**Three things the schema cannot reach, so the adapter must.** An `env://` secret's *value*
never passes through this schema — it arrives from the environment at call time — which is why
the first row is not optional:

| the adapter must | or else |
|---|---|
| re-check the **resolved** credential for control characters | the injection guard above covers only the `inline:` half |
| percent-encode the credential before it enters a query string | `apiKeyQuery` with a secret containing `&` appends a parameter |
| allowlist what a `baseUrl` may resolve to | the pattern cannot tell `169.254.169.254` from a hostname, and the adapter attaches a credential to whatever it is given |
