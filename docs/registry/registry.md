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
| **[1. Architecture](#1-architecture)** | The two calls, and where the registry sits in them |
| **[2. Deployment topologies](#2-deployment-topologies)** | Adapter at the centre, or one per layer — and who holds the credentials |
| **[3. Registry schema](#3-registry-schema)** | `Provider`, `Capability`, `ProviderCapability` |
| **[4. Registry APIs](#4-registry-apis)** | The reads, the writes, and the boot load |
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

## Known gaps for v1

| | |
|---|---|
| No min/max qualifier on `parameter` | The `parameters` item is `{parameter, value, unit}` with an eight-value enum and no aggregation field, so *tomorrow's high is 30.6 and low is 22.1* is inexpressible — and every Indian weather upstream reports `tmin`/`tmax`. The item is open, so mappings emit a private `aggregation` and it validates while meaning nothing to anyone else. Affects Weather, a v1 category. |
| `informationMode` is not in `docs/design/` | Zero mentions in our plan and zero in `src/`, yet it is `required` on every published resource and decides which half of each pack applies. Whether the DS stores it, indexes it, and lets a caller filter on it is an open decision — and the one item here that changes our code. |
| Nothing re-pins the `schemaUrl` sha | The three `Capability` records point at `3e593b3`. When network-specs moves, nothing here notices; the pin is correct and manual. A check that the sha resolves and that the pack still declares the expected `@type` belongs in the seeding path. |
| `oan-vector` on plain HTTP | Legal (`scheme: none`) but should move behind TLS before real traffic. |
| No JSON Schema files behind this page | Everything above is prose. Sunbird RC boots from JSON Schema, not from a table, so `Provider`, `Capability` and `ProviderCapability` have to be authored before anything can be seeded. Nothing is being migrated — v1 stands the registry up from scratch — so these are ours to write, not BV's to send. |
