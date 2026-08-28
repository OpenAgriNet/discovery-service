# Schemas

Three entities. [`schemas/`](schemas) holds the draft-07 files and is the contract; this page is
the reading of them.

| entity | one row is | unique on |
|---|---|---|
| [`SchemaRegistry`](#schemaregistry) | a data type the network recognises | `capabilityCode` |
| [`Participant`](#participant) | someone on the network | `participantId` |
| [`ProviderSchema`](#providerschema) | how to call one provider for one capability | `bindingKey` |

Every row carries `status: "active" \| "inactive"`. Every read filters on `active`.

---

## `SchemaRegistry`

All five fields required.

```jsonc
{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:WeatherObservation",   // the namespace is literal
  "name": "Weather Observation and Forecast",           // human label
  "version": "v0.1",                                    // must match the vN.N in schemaUrl
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active"
} }
```

Vocabulary only. Nothing in the call path reads it — a capability is a name, not a route.

---

## `Participant`

Three required fields, then **exactly one** of `node` or `upstream` (`oneOf`). A node speaks
Beckn; an upstream is an ordinary HTTP API our adapter calls.

```jsonc
{ "Participant": {
  "participantId": "oan-provider",                  // stable id
  "name": "OpenAgriNet provider adapter",
  "status": "active",                               // active | inactive
  "node": {                                         // this one speaks Beckn
    "subscriberId": "bpp.openagrinet.gov.in",       // context.bppId, and field 1 of the Authorization keyId
    "subscriberUrl": "https://bpp.openagrinet.gov.in/beckn",   // where Beckn messages go; https only
    "type": "BPP",                                  // BAP | BPP | NETWORK
    "keys": [ {                                     // 1–8 keys; plural for rotation
      "keyId": "k1",                                // field 2 of the Authorization keyId
      "use": "sign",                                // sign | encrypt
      "alg": "ed25519",                             // fixed by use: sign→ed25519, encrypt→x25519
      "key": "base64:xq4+2oQ6MgSZdHHBMtNd1TmnPTmzY5UoZlqzf0yn6ZA=",   // 44 chars = 32 raw bytes
      "validFrom": "2026-08-01T00:00:00Z",
      "validUntil": "2026-11-01T00:00:00Z",         // optional; absent = open-ended
      "status": "active"                            // active | revoked
    } ] } } }
```
```jsonc
{ "Participant": {
  "participantId": "mausamgram",                    // also the Beckn offer.provider.id
  "name": "IMD Mausamgram NWP",
  "status": "active",
  "upstream": {                                     // this one does not speak Beckn
    "baseUrl": "https://mausamgram.imd.gov.in/nwpapi",   // the binding's path is appended to it
    "auth": {                                       // how WE authenticate TO it
      "scheme": "basic",                            // none | apiKeyQuery | apiKeyHeader | basic
      "secrets": { "username": "env://MAUSAMGRAM_USER",
                   "password": "env://MAUSAMGRAM_X_API_KEY" } } } } }
```

That is the whole answer to *what am I looking at*:

| you read | it is |
|---|---|
| `upstream` | a data provider — an ordinary HTTP API |
| `node.type: "BAP"` | a consumer node |
| `node.type: "BPP"` | a provider node |
| `node.type: "NETWORK"` | the network node |

`type` states direction: BAP asks, BPP answers, NETWORK is infrastructure that both sides call
and that asks nothing of anyone.

The API and the adapter in front of it are separate deployables, so they are separate records —
`mausamgram` is the IMD API, `oan-provider` is the BPP that calls it. Which upstreams a provider
node fronts is that adapter's own config, not a registry field.

`keys` is plural because rotation needs an overlap: the old key stays `active` with a
`validUntil` while the new one is already valid. The sender names which one it used, so the
verifier does not guess. The material is held here, not a fingerprint of it — a public key is
public, and a fingerprint can pin a key without being able to distribute one.

`baseUrl` must be `https` unless `auth.scheme` is `none`. A credential over plaintext is a leaked
credential.

### `auth` — the four schemes

This one stays a table, because what matters is which combinations are *refused*.

| `scheme` | needs | forbids |
|---|---|---|
| `none` | — | `secrets`, `paramName`, `paramNames`, `valuePrefix` |
| `apiKeyQuery` | `secrets` + `paramName` (one secret) or `paramNames` | `valuePrefix` |
| `apiKeyHeader` | `secrets` + `paramName` (one secret) or `paramNames` | `paramNames` excludes `valuePrefix` |
| `basic` | `secrets` with `username` and `password` | |

The header *name* goes in `paramName`. `valuePrefix` keeps its trailing space — `"Bearer "`. Both
forms are shown in [examples.md](examples.md#forms-no-seeded-record-uses).

### `secrets` are pointers, never material

`env://MAUSAMGRAM_USER` resolves in the adapter's own environment. `inline:` also validates, for
operators who cannot set that environment, and it costs three things: `/search` must be
authenticated, the database holds live key material, and rotation becomes a registry write.

The prefix is load-bearing: a bare pasted key fails the pattern, so storing a credential is
deliberate — and *which participants hold real key material* is one query over the table.

`_osConfig.privateFields` is `["$.upstream.auth.secrets"]`.

---

## `ProviderSchema`

Everything but `timeoutMs` and `retryMax` is required.

```jsonc
{ "ProviderSchema": {
  "bindingKey": "mausamgram|openagrinet:WeatherObservation",   // <participantId>|<capabilityCode>
  "participantId": "mausamgram",                    // must be an active upstream Participant
  "capabilityCode": "openagrinet:WeatherObservation",   // must be an active SchemaRegistry
  "method": "GET",                                  // GET | POST
  "path": "/get-daily",                             // appended to that upstream's baseUrl
  "requestMapping":  "mappings/mausamgram/select.request.jsonata",
  "responseMapping": "mappings/mausamgram/select.response.jsonata",
  "timeoutMs": 30000,                               // optional, 1000–120000, default 15000
  "retryMax": 3,                                    // optional, 0–5, default 0
  "status": "active"
} }
```

Resolving a `select` is two reads: this row by `bindingKey`, then its `Participant` by the
`participantId` **found in this row** — never the one in the request.

The two mappings are the only transform the registry describes. Anything else an upstream
needs — a grid point from a lat/long, a station code, a market code — is adapter-internal, keyed
off `participantId`. **It is a seeding prerequisite:** a binding whose adapter has no such step
returns nothing useful.

---

## Five rules the schema cannot express

JSON Schema cannot compare two fields, and RC enforces no reference between entities. Each of
these is a record that passes every pattern and still fails weeks later. Checked by
`verify/records.py`.

1. **`bindingKey` equals `participantId` + `"|"` + `capabilityCode`.**
2. **Both halves resolve to `active` records.**
3. **Where `auth.paramNames` is used, its keys are exactly the keys of `auth.secrets`.** A name
   with no secret sends an empty header; a secret with no name is never sent.
4. **`version` equals the `vN.N` segment of `schemaUrl`.** Otherwise a record advertises `v0.1`
   and resolves `v0.2`.
5. **`node.keys[].keyId` is unique within the array.** `uniqueItems` compares whole objects, so
   two entries with the same `keyId` and different material both pass, and a verifier gets
   whichever it found first.

---

## Known gaps

| | |
|---|---|
| **Advisory and mandi responses do not conform** | Both `KnowledgeResource` bindings omit five required pack fields and use `knowledgeType` values not in the enum; `agmarknet` omits three. Largest open item — see [usecases.md](usecases.md#conformance). |
| **`MandiPrice` may not be a real type** | The pack is `MandiPriceObservation`; the network spec's examples say `MandiPrice`. Needs a network-owner ruling. |
| **`/discover` matches outcome types only** | The packs also sanction advertising a capability type (`WeatherObservationCapability`). Filters that match only the outcome type make conformant providers invisible. Lands on discovery-service, not here. |
| **`schemaUrl` points at `main`** | The packs live on tag `schema-packs-v0.1`. `main` is a moving target for a field whose purpose is to name something stable. |
| **Revocation is inert** | The adapter preloads records at boot and never reads the registry per request, so `status: "revoked"` takes effect on the next reload. Needs a refresh path. |
| **No min/max qualifier on `parameter`** | Every Indian weather upstream reports `tmin`/`tmax`; the pack cannot express *high 39.2, low 32.8*. Mappings emit a private `aggregation`, which validates and means nothing to anyone else. |
| **Read access is not a distinct role** | `_osConfig.roles` gates the entity, not the verb — [api.md](api.md). |
| **`privateFields` is unverified** | Whether RC redacts `$.upstream.auth.secrets` from `/search` on the pinned build has not been checked. |
| **Two capabilities on one participant collide on mapping filenames** | Paths have no capability segment, so one action serving two capabilities resolves to one filename needing two output shapes. Fix is the convention, not the schema. |
| **Path or subdomain for a second endpoint** | One `Participant` is one `baseUrl`, so IMD is two records. The alternatives are additive; choosing before a real case arrives invents semantics. |
| **`oan-vector` is on plain HTTP** | Legal because `scheme: none`. Should move behind TLS before v1 carries traffic. Onboarding work. |
| **The patterns need an ECMA-262 engine** | `baseUrl` and `MappingPath` use negative lookahead. Ajv and Java compile them; **Go's RE2 does not**, so a Go adapter implements those two rules in code. |
| **Shared definitions are copied** | RC loads each entity schema alone, so `Status`, `ParticipantId`, `CapabilityCode` are duplicated across the three files. A drift is a diff, but nothing fails a build. |
| **`responseMapping` conformance is unbuilt** | Nothing validates a mapping's output against the pack it claims to produce. |
