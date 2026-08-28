# Schemas

Three entities. The machine-readable draft-07 files are in [`schemas/`](schemas) and are the
contract; this page is the readable version of them, plus the rules JSON Schema cannot express.

| entity | answers | unique key |
|---|---|---|
| [`SchemaRegistry`](#schemaregistry) | *what does this capability mean?* | `capabilityCode` |
| [`Participant`](#participant) | *where is this provider and how do I authenticate?* | `participantId` |
| [`ProviderSchema`](#providerschema) | *how do I call this provider for this capability?* | `bindingKey` |

**Why three and not one.** The capability is shared by every provider that offers it; the
participant is shared by every capability that provider offers; only the binding is specific to
the pair. Adding a provider to an existing capability is then one `Participant` row and one
`ProviderSchema` row, with nothing else touched — see
[usecases.md](usecases.md#one-capability-many-providers).

```
SchemaRegistry ──┐
                 ├── ProviderSchema  ←  bindingKey = participantId + "|" + capabilityCode
Participant   ───┘
```

The join is by value, not by foreign key — RC enforces no reference between entities, which is
why [rule 2](#five-rules-the-schema-cannot-express) exists.

All three entities share `status`, which is `"active"` or `"inactive"`. **Every read filters on
`active`**, so that flag is the on/off switch for everything here.

---

## `SchemaRegistry`

The vocabulary. One row per capability code the network recognises.

Required: `capabilityCode`, `name`, `version`, `schemaUrl`, `status`. Nothing else is allowed.

| field | type | rule |
|---|---|---|
| `capabilityCode` | string | `openagrinet:` + a PascalCase name |
| `name` | string | 1–200 chars, not blank — human-facing |
| `version` | string | `vN.N` |
| `schemaUrl` | string | must be a `raw.githubusercontent.com/OpenAgriNet/network-specs/…/schema/<Pack>/vN.N/<file>.yaml` URL |
| `status` | enum | `active` \| `inactive` |

```json
{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:WeatherObservation",
  "name": "Weather Observation and Forecast",
  "version": "v0.1",
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active"
} }
```

`schemaUrl` is pattern-locked to one host and one repository on purpose: it is the one field
here that names something fetched over the network, and a free-form URL in a shared registry is
a place to point somebody's adapter at content you control.

**This entity is never read on the call path.** A capability is vocabulary. Resolving a `select`
reads the binding and the participant, and nothing else.

---

## `Participant`

Who a provider is, where it lives, and how to authenticate to it.

Required: `participantId`, `name`, `roles`, `baseUrl`, `status`, `auth`. `publicKeys` is optional.
Nothing else is allowed.

| field | type | rule |
|---|---|---|
| `participantId` | string | lowercase, 3–64 chars, `[a-z0-9._:-]`. **This is the Beckn `provider.id`.** |
| `name` | string | 1–200 chars, not blank |
| `roles` | array | 1–2 unique values from `provider`, `consumer` |
| `baseUrl` | string | `http(s)://host[/path]`. No `?`, `#`, `@`, whitespace or `..` |
| `status` | enum | `active` \| `inactive` |
| `auth` | object | see below |
| `publicKeys` | array | 1–8 entries of `{keyId, alg, hash}` |

**`baseUrl` must be `https` unless `auth.scheme` is `none`.** The schema enforces it with a
conditional: a credential over plain HTTP is a credential on the wire.

The five characters `baseUrl` refuses are each a URL that `baseUrl + path` would otherwise have
produced that nobody wrote:

| refused | because the concatenation would |
|---|---|
| `?` or `#` | put the path **inside the query string** — `…/v1?tenant=a` + `/get-daily` |
| `@` | carry `user:pass@host` — a credential outside `auth.secrets`, so outside `privateFields`, so into every log line that prints the URL |
| whitespace | be mangled differently by every HTTP client |
| `..` | traverse against the upstream, from a field nobody reads as a path |

`path` on the binding forbids `?` from the other side, so no value reaches the wire by being
concatenated into a stored string. A leading `/` on `path` and none trailing on `baseUrl` means
exactly one `/` falls between them and nothing needs normalising.

### `auth`

`scheme` is required and is one of four. What else is allowed depends on it, enforced by
conditionals in the schema:

| `scheme` | requires | forbids |
|---|---|---|
| `none` | — | `secrets`, `paramName`, `paramNames`, `valuePrefix` |
| `apiKeyQuery` | `secrets`, and exactly one of `paramName` / `paramNames` | `valuePrefix` |
| `apiKeyHeader` | `secrets`, and exactly one of `paramName` / `paramNames` | — |
| `basic` | `secrets` with **both** `username` and `password` | `paramName`, `paramNames`, `valuePrefix` |

- **`paramName` + one secret** is the ordinary case: one header or one query parameter.
- **`paramNames`** is a map for an upstream that needs several, and it excludes `valuePrefix`.
- **`valuePrefix`** must end in a space (`"Bearer "`), and is header-only — a `Bearer ` prefix in
  a query string means nothing.

```json
{ "scheme": "none" }
{ "scheme": "apiKeyQuery",  "paramName": "token", "secrets": { "token": "env://EXAMPLE_TOKEN" } }
{ "scheme": "apiKeyHeader", "paramName": "Authorization", "valuePrefix": "Bearer ",
                            "secrets": { "token": "env://EXAMPLE_TOKEN" } }
{ "scheme": "basic", "secrets": { "username": "env://EXAMPLE_USER",
                                  "password": "env://EXAMPLE_PASS" } }
```

A whole record using `valuePrefix` — the shape a bearer-token upstream API takes. It holds no
`publicKeys`, because it signs nothing:

```json
{ "Participant": {
  "participantId": "example-upstream-api",
  "name": "An upstream data API behind a bearer token",
  "roles": ["provider"],
  "baseUrl": "https://api.example.gov.in/v2",
  "status": "active",
  "auth": { "scheme": "apiKeyHeader",
            "paramName": "Authorization",
            "valuePrefix": "Bearer ",
            "secrets": { "token": "env://EXAMPLE_TOKEN" } }
} }
```

And one using `publicKeys` — a participant that runs its own adapter, terminates Beckn calls and
signs them. `scheme: "none"` is right here and not an omission: **this participant is not
authenticated by a shared credential, it is authenticated by its signature**, and the key hash is
what verifies it. See [Node, or provider inside a catalog?](#node-or-provider-inside-a-catalog).

```json
{ "Participant": {
  "participantId": "example-network-node",
  "name": "A participant that runs its own adapter",
  "roles": ["provider", "consumer"],
  "baseUrl": "https://beckn.example.gov.in",
  "status": "active",
  "auth": { "scheme": "none" },
  "publicKeys": [ { "keyId": "example-network-node.k1", "alg": "ed25519",
                    "hash": "sha256:04df206b469a7fefd868d6bf40bb592b4359cbfc49f51404dfabba25c4a7a5c1" } ]
} }
```

And `paramNames`, for an upstream that wants two headers. Note there is no `paramName` and no
`valuePrefix` — the schema forbids both alongside it, and the keys match `secrets` exactly:

```json
{ "Participant": {
  "participantId": "example-two-headers",
  "name": "A provider wanting a key and a tenant",
  "roles": ["provider"],
  "baseUrl": "https://api.example.gov.in",
  "status": "active",
  "auth": { "scheme": "apiKeyHeader",
            "paramNames": { "key": "X-Api-Key", "tenant": "X-Tenant-Id" },
            "secrets":    { "key": "env://EXAMPLE_KEY", "tenant": "env://EXAMPLE_TENANT" } }
} }
```

No v1 participant uses `paramNames`, `valuePrefix` or `publicKeys`. These three blocks exist so
those fields stay exercised and documented rather than becoming the parts of the schema nobody
has ever written.

### Node, or provider inside a catalog?

**The record does not tell you, and `roles` is not the field that would.** Two independent
questions are being asked of one field:

| | question | what encodes it |
|---|---|---|
| **What it does** | offers capabilities, or consumes them? | `roles` — `provider`, `consumer`, or both |
| **What it *is*** | does it speak Beckn, or is it an upstream API we call? | **nothing** |

The second axis is the one that changes how you treat the record, and it is not stored:

| | a **network node** (BAP / BPP) | a **provider inside a catalog** |
|---|---|---|
| speaks Beckn | yes — terminates `/select`, calls back `/on_select` | **no.** Has never heard of Beckn |
| who reaches it | the network, at its subscriber URI | **our adapter**, over ordinary HTTP |
| authenticated by | its signature — so `publicKeys` is mandatory | an API key or basic auth in `auth.secrets` |
| what `baseUrl` means | the Beckn subscriber URI | the upstream API base, used as `baseUrl + path` |
| appears in `context` as | `bppId` / `bapId` / `receiverId` | `offer.provider.id` |

**All five v1 records are the right-hand column.** IMD, Agmarknet, a Hasura instance and a vector
index are upstream data APIs; our adapter is the only BPP on the network, and it reaches them
directly. That is why `participantId` is defined as the Beckn `provider.id` — catalog
granularity, not node granularity — and why
[`context.bppId` and `context.receiverId` are read and discarded](#what-select-must-supply).

Three fields *hint* at which column a record is in, and none of them is a rule:

- **`publicKeys` present** → it signs → it is a node. But the field is optional, so absence
  proves nothing, and none of the five v1 records carries one.
- **`auth.scheme` is not `none`** → it is reached with a shared credential → it is an upstream
  API. A node is not reached that way.
- **`baseUrl`** is semantically overloaded between the two columns and looks identical in both.

So a record combining `publicKeys` with `apiKeyHeader` describes something that cannot exist —
signature-authenticated *and* bearer-token-authenticated, node *and* upstream API. The schema
accepts it. **This page shipped exactly that record**, written to exercise two optional fields at
once; it is now two records, one per column.

Deciding this properly means new vocabulary — a `participantKind`, or Beckn's own `BAP`/`BPP`
alongside `roles` — and that decision belongs with the network owners at the point a real node
onboards, not before. Recorded in [Known gaps](#known-gaps).

### Secrets and key hashes are references, not material

Neither `auth.secrets` nor `publicKeys[].hash` holds security material. Both hold a **reference**
to material held outside the registry, in one `<scheme>:<locator>` grammar:

| form | means | legal in |
|---|---|---|
| `env://NAME` | resolved from the adapter's own environment | `secrets` |
| `inline:…` | the literal value, in the record | `secrets` |
| `sha256:<64 hex>` | the fingerprint of a key delivered out of band | `publicKeys[].hash` |

The narrowing is the point. Adding a `vault://` scheme later is one edit to the grammar plus one
deliberate allow-list entry — and widening the grammar alone does **not** make `vault://` legal
as a public-key hash.

The prefix is also not decoration: a bare pasted key fails the pattern and is rejected at write
time, so storing a credential has to be deliberate. And because the prefix is literal, *which
participants hold real key material* is one query over the table.

**No v1 participant carries an `inline:` secret, and none should.** The form is in the schema for
an operator with no environment to point at, and it costs three things: `/search` must be
authenticated, the database holds live key material, and rotation becomes a registry write. A
reviewed file in git is never that operator — this is checked by `verify/records.py`.

### Why `secrets` and `publicKeys` are not one field

They are the same *concept* and cannot be the same *field*. `privateFields` redacts by path, so a
merged `credentials[]` array would need `$.credentials[*].secrets` — a wildcard we have no
evidence RC resolves — and even resolved, the rule would have to redact the outbound element and
not the inbound one. RC cannot express that. Fully redacted makes public keys unreadable and
verification impossible; not redacted puts secrets in clear. Both fail silently.

Cardinality agrees: `publicKeys` is plural because rotation needs an overlap window where old and
new are both valid, while a credential rotates by changing an environment variable.

---

## `ProviderSchema`

The call plan: one row per (provider, capability) pair.

Required: `bindingKey`, `participantId`, `capabilityCode`, `status`, `method`, `path`,
`requestMapping`, `responseMapping`. `timeoutMs` and `retryMax` are optional. Nothing else is
allowed.

| field | type | rule |
|---|---|---|
| `bindingKey` | string | `<participantId>\|openagrinet:<Capability>`. Refuses `AgricultureCapability` and `AgricultureResource` — those are abstract base types, not bindable |
| `participantId` | string | same pattern as on `Participant` |
| `capabilityCode` | string | same pattern as on `SchemaRegistry` |
| `method` | enum | `GET` \| `POST` |
| `path` | string | starts `/`, no `?` |
| `requestMapping` | string | `mappings/…/*.jsonata`, lowercase, no `..` |
| `responseMapping` | string | same |
| `timeoutMs` | integer | 1000–120000, default **15000** |
| `retryMax` | integer | 0–5, default **0** |
| `status` | enum | `active` \| `inactive` |

```json
{ "ProviderSchema": {
  "bindingKey": "mausamgram|openagrinet:WeatherObservation",
  "participantId": "mausamgram",
  "capabilityCode": "openagrinet:WeatherObservation",
  "status": "active",
  "method": "GET",
  "path": "/get-daily",
  "requestMapping":  "mappings/mausamgram/select.request.jsonata",
  "responseMapping": "mappings/mausamgram/select.response.jsonata",
  "timeoutMs": 30000,
  "retryMax": 3
} }
```

**`timeoutMs` and `retryMax` are per-provider registry fields, not service constants.** IMD is
slow and flaky; a Hasura instance on the same network is not. `retryMax` defaults to `0` because a
retry is only safe on an idempotent read, and the operator who knows that is the one seeding the
row.

### Mappings are the only transform the registry describes

The two JSONata files are the whole contract for shaping a call and its response. **Everything
else the adapter does is adapter-internal, on purpose:** nearest-station lookups, commodity-code
tables, GraphQL query construction.

An earlier draft put that step on the binding as a named plugin with its own config and secrets.
That is what a plugin reference in a *shared* registry costs: the name asserts every adapter on
the network can run it, the config asserts they all take the same knobs, and a DSN asserts they
all reach the same database. None of the three is true of anyone but us.

**It is still a seeding prerequisite, not an optional extra.** Four of the five v1 bindings need
such a step, and a binding whose adapter has no such step returns nothing useful. Which ones, and
what each does, is in [usecases.md](usecases.md).

Mapping paths follow `mappings/<participant>/<action>.<request|response>.jsonata`. **There is no
capability segment**, which is a known gap — see [below](#known-gaps).

---

## Five rules the schema cannot express

JSON Schema cannot compare two fields, and RC enforces no reference between entities. These run
in the onboarding path and in the conformance suite, and each one is a record that passes every
pattern in its schema and still produces a failed call, or a silently unverifiable signature,
some weeks later.

1. **`bindingKey` must equal `participantId` + `"|"` + `capabilityCode`.**
2. **Both halves must resolve to live records** — an `active` `Participant` and an `active`
   `SchemaRegistry`.
3. **Where `auth.paramNames` is used, its keys must be exactly the keys of `auth.secrets`.**
   Both mismatches fail quietly: a name with no secret sends a header with no value, a secret
   with no name is never sent at all.
4. **`version` must equal the `vN.N` segment of `schemaUrl`.** Both are stored, both are
   patterned, and draft-07 cannot check that they agree — so the failure mode is a record that
   validates while advertising `v0.1` and resolving `v0.2`.
5. **`publicKeys[].keyId` must be unique within the array.** `uniqueItems` compares whole
   objects, so two entries with the same `keyId` and different hashes both pass — and a verifier
   looking up a key by id gets whichever it found first.

**And one policy the schema cannot enforce at all: every participant that receives a signed
network call must carry at least one `publicKeys` entry.** The schema cannot tell a participant
that terminates Beckn calls from an upstream data API our own adapter reaches directly. None of
the five v1 records carries one — [Known gaps](#known-gaps).

Rules 1–5 are checked by `verify/records.py`. See [`verify/README.md`](verify/README.md).

---

## What `select` must supply

The adapter reads exactly four values off a `select` request, and nothing else.

| what | path | used for |
|---|---|---|
| provider id | `message.contract.commitments[0].offer.provider.id` | left half of `bindingKey` |
| capability | `…resources[0].resourceAttributes."@type"` | right half of `bindingKey` |
| invocation parameters | the rest of that `resourceAttributes` — `location`, `validity`, … | the request mapping |
| action | `context.action` | must be `select`; not part of the key |

**`participantId` is never read from the request.** It comes off the `ProviderSchema` row
returned by the first lookup. A request that could name the participant directly could point a
credentialled call at a host of its choosing — the same rule, and the same reason, as
`EXT_ALLOW_NETWORK_FETCH=false`.

`context.bppId` and `context.receiverId` are read and discarded: they name a network *node*, and
in v1's central topology the adapter is the only BPP, so they discriminate nothing.
`offer.provider.id` names a provider *inside* that node's catalog, which is the granularity a
binding has.

### Why the adapter must validate before it concatenates

Checked against `tests/testdata/beckn-v2.0.0.yaml`:

| node | declaration | consequence |
|---|---|---|
| `Contract.required` | `["commitments"]`, `minItems: 1` | at least one commitment — **guaranteed** |
| `Commitment.required` | `["status","resources","offer"]` | `offer` present — **guaranteed** |
| `Commitment.offer` | `$ref` + `required: ["id","resourceIds"]` | offer has an id — **guaranteed** |
| `Offer.required` | `["id"]` | **`provider` is optional** |
| `Commitment.resources` | no `minItems` | **`resources: []` is valid** |
| `Resource.required` | `["id"]` | **`resourceAttributes` is optional** |
| `Context.required` | *none* | **`action` may be absent** |

So the containers are mandatory and **both halves of the key are not**. A schema-valid request
can carry no derivable `bindingKey`, by three independent routes: a missing `provider`, an empty
`resources`, or a resource with no `resourceAttributes`.

Two rules close it, both in the envelope layer rather than the mapping:

1. **Arity — exactly one.** Reject `commitments.length != 1` or `resources.length != 1`. Silently
   taking `[0]` answers one commitment and drops the rest with a `200`. A batch is a later
   feature, not a tolerated shape.
2. **A distinct error code.** A missing `offer.provider` or `@type` is
   `SCH_REQUIRED_FIELD_MISSING` and must **not** share a code with *no such binding*, which is
   `BIZ_PROVIDER_NOT_FOUND`. Concatenating blindly yields `"|"` or `"mausamgram|"`, neither of
   which resolves — so the caller is told *this provider cannot answer* and the operator is sent
   to audit a registry that is correct, when the fault was in the request.

---

## Known gaps

Open items, worst first.

| | |
|---|---|
| **Advisory and mandi responses do not conform** | Both `KnowledgeResource` bindings omit five required pack fields and use `knowledgeType` values that are not in the enum; `agmarknet` omits three. Each violation and its fix is in [usecases.md](usecases.md) under the relevant use case. **This is the largest open item here.** |
| **`MandiPrice` may not be a real type** | The domain pack is named `MandiPriceObservation`; the network spec's own information-mode examples say `MandiPrice`. One `SchemaRegistry` row, one binding and every filter carry whichever loses. Needs a ruling from the network owners. |
| **`/discover` matches outcome types only** | The domain packs sanction advertising a governed *capability* type (`WeatherObservationCapability`) as well as an `OnDemand` outcome resource. Filters that match only the outcome type make conformant providers invisible. This one lands on discovery-service, not on the registry. |
| **`schemaUrl` points at `main`** | All the seeded records reference the `main` branch while the packs live on tag `schema-packs-v0.1`. `main` is a moving target for a field whose whole purpose is to name something that never changes meaning. |
| **No min/max qualifier on `parameter`** | `WeatherObservation.parameters` items require `{parameter, value, unit}` and are not closed, so *tomorrow's high is 39.2, low 32.8* is inexpressible — and every Indian weather upstream reports `tmin`/`tmax`. Mappings emit a private `aggregation`, which validates while meaning nothing to anyone else. |
| **`informationMode` is proposed, not governed** | It appears in no pack schema — only in the *Information Modes* section's examples, marked *Proposed terminology*. It validates because the packs are open at the top level, but no filter can rely on it. |
| **Two capabilities on one participant collide on mapping filenames** | Paths are `mappings/<participant>/<action>.<…>.jsonata` — no capability segment. A provider serving two capabilities from the same action resolves both to one filename while needing two different output shapes. Nothing rejects it. Worked through in [usecases.md use case 6](usecases.md#6-weather-advisory--not-seeded). The fix is a capability segment in the convention; it is not a schema change. |
| **Nothing distinguishes a network node from a provider inside a catalog** | `roles` encodes *offers vs consumes*, not *speaks Beckn vs is an upstream API we call*. All five v1 records are the latter, and the schema would accept a record that is incoherently both — signature-authenticated and bearer-token-authenticated at once. Today the distinction is carried by convention and by which fields happen to be set; `publicKeys` present is the closest thing to a discriminator and it is optional. The fix is new vocabulary (`participantKind`, or Beckn's `BAP`/`BPP` alongside `roles`) and it needs a network-owner ruling at the point a real node onboards — deciding earlier means inventing semantics. Worked through in [Node, or provider inside a catalog?](#node-or-provider-inside-a-catalog). |
| **No participant has a registered key** | `publicKeys` is in the schema and network policy requires it, but none of the five v1 records carries one — they are upstream data APIs our adapter calls directly, not participants that sign anything. Under the distributed topology each runs its own adapter and does sign, at which point the field is mandatory and the seeding path must enforce it. **This is the gap most likely to be read as *done* because the field exists.** |
| **Read access is not a distinct role** | `_osConfig.roles` gates the entity, not the verb — [api.md](api.md). |
| **`privateFields` is unverified** | Whether RC redacts `$.auth.secrets` from `/search` on the pinned build has not been checked — [api.md](api.md#what-is-not-verified). |
| **Path or subdomain for a second endpoint** | Open, and not decided. A participant with one identity may expose capabilities on different *hosts*, not merely different paths — IMD is `imd.gov.in` but the weather API sits elsewhere. Today one `Participant` is one `baseUrl`, so this is two records sharing an identity prefix, which makes one organisation look like two participants on the wire. The alternatives — a `baseUrl` per binding, or a host override on `ProviderSchema` — are both additive, and choosing before a real case arrives means inventing semantics. |
| **`oan-vector` is on plain HTTP** | Legal, because `scheme: none`. Should move behind TLS with a real hostname before v1 carries traffic. That is onboarding work, not a schema change. |
| **The patterns need an ECMA-262 engine** | Two use negative lookahead — `baseUrl` to refuse `..`, `MappingPath` to refuse traversal. Ajv and Java compile them; **Go's RE2 does not**, so a Go adapter validating these records locally has to implement those two rules in code rather than reuse the pattern. Nothing else in the three files is RE2-hostile, and the length caps are written under RE2's 1000-repeat limit precisely so this stays a one-reason problem. |
| **Shared definitions are copied, not referenced** | RC loads each entity schema alone, so `Status`, `ParticipantId` and `CapabilityCode` are duplicated verbatim across the three files. Identical names make a drift a diff, but nothing fails a build on it. |
| **`responseMapping` conformance is unbuilt** | Nothing validates a mapping's output against the pack it claims to produce. |
