# OpenAgriNet registry

**The registry stores three things: providers, capabilities, and the mapping between
them.** That mapping is the call plan — given a provider and a capability, how do you
actually reach them.

It holds nothing else. Not catalogs, not resources, not search indexes, not participant
identity. A question it cannot answer is a question for something else: *who serves
weather?* is answered by the discovery service from its indexed catalog, and only once
that names a provider is the registry read at all.

**Who reads it.** The adapter, or the adopter's experience layer. **Not**
discovery-service — it answers `/discover` from its own catalog store and never opens the
registry.

**Scope.** The first OpenAgriNet release: five providers, five bindings, three
capabilities, across four categories. Written for that set, not as a general design; when
the set grows this page grows with it.

| | |
|---|---|
| **[1. Architecture](#1-architecture)** | The two calls, the flow, and where the registry sits in it |
| **[2. Deployment topologies](#2-deployment-topologies)** | Adapter at the centre, or one per layer — and who holds the credentials |
| **[3. Registry schema](#3-registry-schema)** | `Provider`, `Capability`, `ProviderCapability` |
| **[4. Registry APIs](#4-registry-apis)** | The routes and who may call them, the two reads, the boot load |
| **[→ Records to seed](examples.md)** | The thirteen records, in RC write form |
| **[→ Use case execution](usecases.md)** | One question traced end to end, with payloads |

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

The six steps below are the same under either deployment topology. What differs is which
box each arrow lands on — this trace is **topology A**; the same flow under **topology B**
is in [§2](#2-deployment-topologies).

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

The work at hop ② is **identical in both**. What changes is how many adapters are
deployed, which adapter each hop talks to, and therefore who holds the upstream
credentials.

### A — one adapter, at the centre

```
   ADOPTER                        NETWORK LAYER                    PROVIDER
 ┌───────────────┐      ┌────────────────────────────────┐       ┌──────────┐
 │               │  ①   │  ┌───────────┐   ┌─────────┐   │       │          │
 │ chatbot ·     ├─────▶│  │           ├──▶│discovery│   │       │          │
 │ call centre   │      │  │   ONIX    │   │ service │◀──┼───────┤   IMD    │
 │               │  ②   │  │  adapter  │   └─────────┘   │publish│          │
 │               ├─────▶│  │           │                 │       │          │
 │               │      │  │           ├─────────────────┼──────▶│          │
 │               │      │  │           ◀─────────────────┼───────┤          │
 │               │      │  └─────┬─────┘         ⑤       │       │          │
 │               │      │     ④  │  ┌──────────┐         │       │          │
 │               │      │        └─▶│ registry │         │       │          │
 │               │      │           └──────────┘         │       │          │
 └───────────────┘      └────────────────────────────────┘       └──────────┘
```

One ONIX instance is both the consumer's outbound point and the provider node, and the
**discovery service sits beside it in the network layer** — `discover` ① is answered
inside the network, from the catalog the provider published, without the provider being
called. Only `select` ② reaches upstream, and **the adapter is what calls the registry**
④; the experience layer never sees it. The full trace is
[§1](#1-architecture).

Fewest moving parts: one process to operate, one registry read, and signature
verification that resolves to the same party on both sides — so it proves nothing and
costs nothing. Fine while the adopter is the only participant. It stops being fine the
moment a second consumer wants these capabilities, because there is no real trust
boundary to enforce anything at.

### B — an adapter at every layer

The adopter, the network and the provider each run their own ONIX. **The two hops then go
to different adapters**, and that is the part worth getting right:

```
   EXPERIENCE LAYER           NETWORK LAYER              PROVIDER LAYER     
 ┌─────────────────┐        ┌────────────────┐        ┌────────────────────┐
 │ chatbot ·       │        │  NETWORK ONIX  │publish │  PROVIDER ONIX     │
 │ call centre     │        │       +        │◀───────┤  validate · route  │
 │       │         │   ①    │   discovery    │        │  ┌──────────┐      │
 │       ▼         │discover│    service     │        │  │ registry │      ├──▶ IMD
 │ EXPERIENCE ONIX ├───────▶│                │        │  └──────────┘      │◀───
 │                 │◀───────┤                │        │  map · respond     │
 │                 │        └────────────────┘        │                    │
 │                 │  ②  select · init · status       │                    │
 │                 ├─────────────────────────────────▶│                    │
 │                 │◀─────────────────────────────────┤                    │
 └─────────────────┘                                  └────────────────────┘
```

Four things change, and only the third is about the registry:

1. **The two hops go to different adapters.** `discover` is the only one that touches the
   network layer. Once it names a provider, `select` — and `init`, `status`, whatever
   follows — go from the experience-layer ONIX **straight to that provider's ONIX**. The
   network layer is not a proxy and never sits on the transaction.
2. **Signature verification becomes real.** Consumer and provider are now different
   parties, so verifying the caller is worth doing — and it is the *provider* ONIX that
   does it, against the experience-layer ONIX's key.
3. **The registry stays on the provider side, and only there.** This is the rule that
   matters.
4. **Half the adapter config is dormant.** The async callback route never fires under
   synchronous transport. Expected, not a misconfiguration.

> **The consumer side must never learn that `mausamgram` means
> `https://mausamgram.imd.gov.in/nwpapi`.** Resolving a call plan means holding the
> upstream credentials that go with it. A consumer-side adapter that resolves capabilities
> needs `env://MAUSAMGRAM_X_API_KEY` to be resolvable in *its* environment — and at that
> point the credential has left the provider's control, which is the one thing the
> `env://` pointer design exists to prevent.

The same six steps as [§1](#1-architecture), landing on different boxes:

```
  EXPERIENCE LAYER   EXPERIENCE ONIX   NETWORK ONIX + DS   PROVIDER ONIX   REGISTRY   UPSTREAM
          │                 │                  │                 │             │          │
          │ ① resolve meaning                  │                 │             │          │
          │                 │                  │                 │             │          │
          ├─────────────────▶                  │                 │             │          │
          │ ② discover      │                  │                 │             │          │
          │                 ├──────────────────▶                 │             │          │
          │                 ◀──────────────────┤                 │             │          │
          ◀─────────────────┤                  │                 │             │          │
          │ on_discover: catalogs — provider.id + @type + OnDemand             │          │
          │ an ADVERTISEMENT — no value in it  │                 │             │          │
          │                 │                  │                 │             │          │
          ├─────────────────▶                  │                 │             │          │
          │ ③ select        │                  │                 │             │          │
          │                 ├────────────────────────────────────▶             │          │
          │                 │ straight to the provider — the network layer is NOT on this hop
          │                 │                  │                 ├─────────────▶          │
          │                 │                  │                 │ ④ resolve → call plan + auth
          │                 │                  │                 ◀─────────────┤          │
          │                 │                  │                 ├────────────────────────▶
          │                 │                  │                 │ ⑤ enrich, map, authenticate, call
          │                 │                  │                 ◀────────────────────────┤
          │                 ◀────────────────────────────────────┤             │          │
          │                 │ ⑥ map response → Beckn v2          │             │          │
          ◀─────────────────┤                  │                 │             │          │
          │ on_select: 5 WeatherObservation resources, Direct    │             │          │
```

That is what makes point 1 more than a routing detail. The experience-layer ONIX addresses
the provider ONIX **by provider id**, not by upstream URL, precisely because it is not
allowed to know the upstream URL. Proxying the transaction through the network layer
instead would not fix that — it would move the same problem one hop sideways.

### Which to run

| | A — one adapter at the centre | B — an adapter at every layer |
|---|---|---|
| Adapters deployed | 1 | 3 — experience, network, provider |
| `discover` goes | experience layer → the adapter | experience ONIX → **network** ONIX |
| `select` · `init` · `status` go | experience layer → the same adapter | experience ONIX → **provider** ONIX, direct |
| Registry is read by | the single adapter | the **provider-side** adapter only |
| Experience layer knows | provider ids only | provider ids only |
| Upstream credentials live | in the one adapter | in the provider-side adapter |
| Signature check | resolves to self; proves nothing | a real trust boundary |

**v1 runs A**, and [use case execution](usecases.md) traces A. B is what a second consumer
forces, and nothing in the schema or the records changes when it happens — only where the
adapters are deployed and which side holds the secrets. That is the point of keeping the
call plan in a registry rather than in a service's config: the move is a deployment
change, not a rewrite.

## 3. Registry schema

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

```jsonc
{ "Provider": {                            // the wrapper is part of the schema: top level is required: ["Provider"]
  "providerId": "mausamgram",              // REQ  ^[a-z0-9][a-z0-9._:-]{2,63}$   — this is the Beckn provider.id
  "name": "IMD Mausamgram NWP",            // REQ  minLength 1
  "baseUrl": "https://mausamgram.imd.gov.in/nwpapi",
                                           // REQ  ^https?://[^/].*[^/]$ — no trailing slash.
                                           //      MUST be https whenever auth.scheme != "none"
  "status": "active",                      // REQ  active | inactive
  "auth": { "scheme": "basic", "…": "…" }, // REQ  how we authenticate upstream — shape below
  "authProfiles": { "bulk": { "…": "…" } } // opt  named alternates, same Auth shape; keys ^[a-z][a-zA-Z0-9]*$
} }
```

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

```jsonc
{ "Capability": {
  "capabilityCode": "openagrinet:WeatherObservation",
                                           // REQ  ^openagrinet:[A-Z][A-Za-z0-9]*$
                                           //      and NOT AgricultureCapability / AgricultureResource
  "name": "Weather Observation and Forecast",   // REQ  minLength 1
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/3e593b3.../schema/WeatherObservation/v0.1/attributes.yaml",
                                           // REQ  ^https://(?!.*/refs/heads/)(?!.*/(main|master|develop)/).+/attributes\.yaml$
                                           //      a branch ref is rejected — the pin must be a commit sha
  "status": "active",                      // REQ  active | deprecated
  "baseTypes": ["openagrinet:AgricultureResource"]
                                           // opt  items ^openagrinet:, uniqueItems
} }
```

`capabilityCode` **is the outcome type** — what the caller gets back. The two negative
lookaheads on `schemaUrl` reject a branch ref: a capability pinned to `main` means the
contract you validated against last week is not the one you validate against today.

Nothing names a provider here. `AgricultureResource` is the shared field set every pack
composes with `allOf` — it identifies nothing, so it cannot be a `capabilityCode`; it goes
on `baseTypes[]`, where a broad request can still fan out to it.

### `ProviderCapability` — the call plan

`uniqueIndexFields: [bindingKey]` · `indexFields: [providerId, capabilityCode, status]`

```jsonc
{ "ProviderCapability": {
  "bindingKey": "mausamgram|openagrinet:WeatherObservation",
                                           // REQ  exactly providerId + "|" + capabilityCode. Two segments, no action
  "providerId": "mausamgram",              // REQ  must equal segment 1
  "capabilityCode": "openagrinet:WeatherObservation",
                                           // REQ  must equal segment 2
  "status": "active",                      // REQ  active | inactive

  "method": "GET",                         // ─┐ SINGLE-CALL shape: all three, and no "steps"
  "path": "/get-daily",                    //  │ all five v1 bindings are this shape
  "requestMapping": "mappings/mausamgram/select.request.jsonata",   // ─┘
  "steps": [ "…", "…" ],                   // ── MULTI-STEP shape: 2–6 steps, and none of the three above.
                                           //    oneOf — a record is one shape or the other, never half of each

  "responseMapping": "mappings/mausamgram/select.response.jsonata",
                                           // REQ  ^mappings/(?!.*\.\.)[a-z0-9][a-z0-9._/-]*\.jsonata$
  "enricher": { "name": "pointFromIntent" },
                                           // opt  always the object form. name ^[a-z][a-zA-Z0-9]*$;
                                           //      config and secrets optional, secrets values env:// only
  "timeoutMs": 30000,                      // opt  1000–120000, default 15000
  "retryMax": 3                            // opt  0–5, default 0
} }
```

The `bindingKey` pattern, in full:

```
"pattern": "^[a-z0-9][a-z0-9._:-]{2,63}\\|openagrinet:[A-Z][A-Za-z0-9]*$"
"not":     { "pattern": "\\|openagrinet:Agriculture(Capability|Resource)$" }
```

**Two integrity rules no JSON Schema can express**, so they run in onboarding and in the
conformance suite: `bindingKey` must equal `providerId` + `"|"` + `capabilityCode`, and
both of those must resolve to live records.

**`providerId` and `capabilityCode` are stored** even though `bindingKey` contains them,
because RC indexes and searches whole fields, never a substring: a segment that lives only
inside the key cannot be queried. Both are in `indexFields`, and *list every binding for
this provider* is what onboarding and deactivation need. They earn the duplication.

**The key is two segments — `<providerId>|<capabilityCode>`.** BV's schema has a third,
`action`, plus a required column holding the same value. OAN v1 drops both.

*The column* went for the ordinary reason. It was never in `indexFields`, so it answered
no query. The adapter *builds* the key from the incoming request and does one exact-match
lookup — it never reads `action` back off the row. And the enum it declared merely
restated the alternation the key pattern carried at the time, so it validated nothing the
pattern did not. A third copy of a fact that no code reads is just a third thing that can
disagree with the other two.

*The segment* goes for a narrower reason: **there is nothing for it to discriminate.** A
third segment earns its place only when one provider serves one capability through
*different upstream calls per action* — `pm-kisan` checking eligibility on `select`,
starting an application on `init`, and reading that application's status on `status` are
three genuinely different call plans, and under a two-part key they collide. v1 has no
such provider. All five bindings are single-call `select`, and `providerId|capabilityCode`
is already unique across them.

Dropping it buys more than brevity. The key becomes **exactly** `providerId + "|" +
capabilityCode`, so *the key agrees with its own fields* becomes a total check instead of
a check on two segments out of three, and the adapter can compute a key from a request
without first deciding which action it is serving.

**What forces the third segment back**, so nobody has to rediscover it: a provider whose
`(provider, capability)` pair needs more than one call plan. When that arrives the choice
is between restoring the segment or modelling the actions inside a single row. Either way
it is cheap, because `bindingKey` is derived and nothing holds a reference to it —
restoring the segment is a re-seed of five rows, not a migration.

**`enricher` is always the object form.** BV's schema allows either a bare name or
`{name, config, secrets}`, and its own guidance says to prefer the object — which four of
its five records then don't. OAN v1 drops the `oneOf` and keeps only the object.

One shape means one code path: the adapter reads `.name` without first asking what type it
is holding. But the reason that decides it is `config`. It is **the only free-form object
in all three schemas** — the one remaining place a literal DSN could be pasted where an
`env://` pointer belongs, which the imported conformance page already names as the last
credential-shaped hole in this design. A field that has to be audited should have one
shape to recognise, not two.

The cost is a two-character wrapper on the four bindings that configure nothing:
`{"name": "pointFromIntent"}`. `secrets` keeps the `env://` pointer rule the rest of the
schema uses — the pointer is resolved by the adapter at call time, never stored.

**Mappings live in files, not in the row.** The Mausamgram response transform is 76 lines
of JSONata; stored in the row it is one string with every newline escaped, unreviewable in
a diff. The pattern pins the directory, rejects `..`, requires `.jsonata`, and allows
lowercase only — a path differing from disk by case resolves on macOS and 404s on Linux.

```
registry/mappings/<provider>/<action>.<request|response>.jsonata
```

`<action>` survives here as a *filename*, and only here. A file name is free to say more
than a key does, and `select.request.jsonata` is what someone opening the directory needs
to see. The row points at the path; nothing parses it.

---

## 4. Registry APIs

Sunbird RC generates the REST surface from the JSON Schemas in §3 — one set of routes per
entity, named after it. Nothing here is hand-written. `<entity>` below is `Provider`,
`Capability` or `ProviderCapability`.

| route | who may call | what it does |
|---|---|---|
| `POST /api/v1/<entity>` | Network Operator only | Create a record — a provider, a capability, or a binding between the two. |
| `POST /api/v1/<entity>/search` | anyone — public, no token | Look up records by indexed field. This is how a caller resolves a provider id to a base URL and a call plan. |
| `PUT /api/v1/<entity>/{osid}` | Network Operator only | Replace a record in full. Suspends or reactivates it by flipping `status`. |
| `DELETE /api/v1/<entity>/{osid}` | Network Operator only | Remove a record permanently. Not reversible. |

`osid` is RC's own row id, handed back by the create. It is not `providerId` and not
`bindingKey`, so an updater has to search before it can write.

**Prefer `status: "inactive"` over `DELETE`.** `status` is what every read filters on, so
flipping it takes a provider out of service just as completely, and it leaves the row
where an operator can see what was turned off and when. `DELETE` also orphans quietly:
removing a `Provider` leaves its bindings pointing at nothing, and RC enforces no
reference between the two — *both must resolve to live records* is our onboarding rule,
not the registry's.

**`search` is public and that is deliberate.** The registry holds no secrets — every
credential is an `env://` pointer resolved in the provider adapter's own environment, so a
full dump yields base URLs, auth *schemes* and parameter *names*, and nothing that
authenticates anyone. Writes are the Operator's alone.

### The reads the adapter needs

Exactly two, both single-field and both exact-match:

```
POST /api/v1/ProviderCapability/search
{ "filters": { "bindingKey": { "eq": "mausamgram|openagrinet:WeatherObservation" },
               "status":     { "eq": "active" } } }

POST /api/v1/Provider/search
{ "filters": { "providerId": { "eq": "mausamgram" },
               "status":     { "eq": "active" } } }
```

The first returns the call plan, the second the base URL and auth block. **No join, and no
second capability read** — `Capability` is vocabulary, not something the call path needs.

> The key carries **no action**. The adapter builds it from `provider.id` and
> `resourceAttributes."@type"` on the incoming request, and nothing else — see
> [§3](#3-registry-schema) for why the third segment went and what would bring it back.

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

## Known gaps for v1

| | |
|---|---|
| No min/max qualifier on `parameter` | The `parameters` item is `{parameter, value, unit}` with an eight-value enum and no aggregation field, so *tomorrow's high is 30.6 and low is 22.1* is inexpressible — and every Indian weather upstream reports `tmin`/`tmax`. The item is open, so mappings emit a private `aggregation` and it validates while meaning nothing to anyone else. Affects Weather, a v1 category. |
| `informationMode` is not in `docs/design/` | Zero mentions in our plan and zero in `src/`, yet it is `required` on every published resource and decides which half of each pack applies. Whether the DS stores it, indexes it, and lets a caller filter on it is an open decision — and the one item here that changes our code. |
| Nothing re-pins the `schemaUrl` sha | The three `Capability` records point at `3e593b3`. When network-specs moves, nothing here notices; the pin is correct and manual. A check that the sha resolves and that the pack still declares the expected `@type` belongs in the seeding path. |
| `oan-vector` on plain HTTP | Legal (`scheme: none`) but should move behind TLS before real traffic. |
| No JSON Schema files behind this page | The shapes in §3 are annotated examples, not schema files. Sunbird RC boots from JSON Schema, so `Provider`, `Capability` and `ProviderCapability` have to be authored before anything can be seeded. Nothing is being migrated — v1 stands the registry up from scratch — so these are ours to write, not BV's to send. |
