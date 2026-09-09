# Registry design

Three tables that tell an adapter who is on the network, what data types exist,
and how to call each provider.

This is the registry's design, not this service's. The discovery service never
reads it — it answers `discover` from its own published catalogs. The registry is
what the ONIX adapters around it read, and it is documented here because the two
halves only make sense together.

**Sunbird Registry is the persistence implementation, not the contract.** It sits
behind an OAN-owned Registry interface: external callers use the OAN envelope and
that interface, and everything below — the three entities, their fields, and the
Sunbird RC routes that carry them — is internal implementation detail. The
Provider Onboarding Platform creates and updates these records through the OAN
Registry wrapper, and the wrapper is what exposes a *resolved* invocation without
exposing the binding entity that produced it. This page documents the internals
because whoever operates the registry needs them; nothing outside the wrapper
should depend on them.

> [`design/registry/schemas/`](design/registry/schemas/) holds the draft-07 files
> and **those are the contract**. This page is the reading of them. Nothing
> mechanically checks that the two agree — the `verify/` checkers that did were
> removed when this folder moved, and they are in git history. When it matters,
> read the JSON.

## Contents

- [Deployment topology](#deployment-topology)
- [One flow, end to end](#one-flow-end-to-end)
- [The three entities](#the-three-entities)
- [The registry's own API](#the-registrys-own-api)
- [The records that seed v1](#the-records-that-seed-v1)
- [Six farmer questions](#six-farmer-questions)
- [Known gaps](#known-gaps)

## Deployment topology

Three ONIX adapters, one registry, one discovery service, on the **Bharat
Vistaar** subnet (`networkId: da.gov.in/vistaar`).

| Adapter | `participantId` | Sits | Does |
|---|---|---|---|
| **consumer node** | `seeker-network-vistaar.da.gov.in` | experience layer, beside the farmer app | signs `discover` and `select` |
| **network node** | `discovery-network-vistaar.da.gov.in` | network, alongside the discovery service | exposes publish and discover; answers `discover` from published catalogs |
| **provider node** | `provider-network-vistaar.da.gov.in` | provider side | terminates `select`, calls the external provider API, signs `on_select` |

The network node is **proposed** — Bharat Vistaar has no third subscriber today.

**Every transaction is synchronous.** `on_discover` and `on_select` are response
bodies, not inbound calls. See [Synchronous, and the spec has not caught
up](#synchronous-and-the-spec-has-not-caught-up).

**Signature verification happens in ONIX, never in the discovery service**
(`AUTH_ENABLE_SIGNATURE_VERIFICATION=false`, and `true` refuses the boot). The
discovery service must therefore have no route from outside: a `senderId` is only
worth reading downstream of the verifier.

## One flow, end to end

```
  farmer
    │
    ▼
┌──────────────┐   discover   ┌──────────────┐
│  consumer    │─────────────▶│ network node │──▶ discovery service
│    node      │◀─────────────│              │    answers from the published catalog
└──────────────┘  on_discover └──────────────┘
    │
    │  <action>   provider = mausamgram; any Beckn action the binding registers
    ▼
┌──────────────┐
│  provider    │   GET https://mausamgram.imd.gov.in/nwpapi/get-daily
│    node      │──────────────────────────────────▶  IMD Mausamgram NWP
└──────────────┘                                     (an ordinary HTTP API)
    │
    │  on_<action>   the typed forecast
    ▼
  consumer node
```

Five registry records carry that flow: the three nodes, plus `mausamgram` as an
upstream and its binding to `openagrinet:WeatherObservation`.

`discover` is answered by the network node from the published catalog — there is
no fan-out to providers. `select` goes from the consumer node **straight to the
provider node**; the network node is not in that path.

## The three entities

| Entity | One row is | Unique on |
|---|---|---|
| [`SchemaRegistry`](#schemaregistry) | a data type the network recognises | `capabilityCode` |
| [`Participant`](#participant) | someone the network deals with | `participantId` |
| [`ProviderSchema`](#providerschema) | how to call one provider for one capability | `bindingKey` |

Every row carries `status: "active" | "inactive"`, and every read filters on
`active`.

Which row a field goes on: `Participant` holds what is true of a provider
whatever you ask it for — its `baseUrl`. `ProviderSchema` holds what varies per
capability — the method, the path, the mapping file, the timeouts. A provider
serving two capabilities is one `Participant` and two `ProviderSchema` rows.
Neither holds a credential.

### `SchemaRegistry`

All five fields required. Vocabulary only — nothing in the call path reads it.

```jsonc
{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:WeatherObservation",   // the namespace is literal
  "name": "Weather Observation and Forecast",           // human label
  "version": "v0.1",                                    // must match the vN.N in schemaUrl
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active"
} }
```

### `Participant`

An admitted participant or service endpoint. Five fields always, then `type`
decides the rest. One level, no wrapper object:

| | Always | `network_adapter` | `upstream_api` |
|---|---|---|---|
| required | `participantId`, `name`, `type`, `status`, `baseUrl` | `role`, `keys` | — |
| refused | | | `role`, `keys` |

> The source text declared the `type` enum as `network_adapter` / `upstream_api`
> but wrote `"upstream"` in its second worked example. The declared enum wins:
> the example is normalised to `upstream_api` here and in the schema.

A **`network_adapter`** is a node — it speaks Beckn. It can sit at any of the
three layers, and `role` is what says which: the provider adapter, the consumer
adapter and the network node are all `network_adapter` rows, differing only
there. Its `participantId` *is* its network identity — what goes on the wire as
`senderId` / `receiverId`, and field 1 of the `Authorization` keyId. There is no
second id field, because an adapter id that is also a hostname is one name for
one thing. The schema enforces the hostname shape when `type` is
`network_adapter`, so `oan-provider` is refused there and
`provider-network-vistaar.da.gov.in` is not.

An **`upstream_api`** is the provider's actual external system — the ordinary API
an adapter calls. It has not heard of Beckn, so it has no role and no keys, and
its `participantId` is the `offer.provider.id` the farmer sees.

**`type` and `role` are two axes and neither substitutes for the other.** `type`
says how we integrate with it — Beckn, or plain HTTP through a binding. `role`
says where on the network it sits: `provider`, `consumer` or `network`. That is
why `role` applies only to a `network_adapter`: an `upstream_api` is not on the
network to have a place on it.

`baseUrl` is one field because it was always one idea: the base something is
appended to — a Beckn action for a `network_adapter`, a binding's `path` for an
`upstream_api`. It is `https` for a `network_adapter`, unconditionally.

```jsonc
{ "Participant": {
  "participantId": "provider-network-vistaar.da.gov.in",   // the only id, and it is the wire identity
  "name": "OpenAgriNet Network Adapter",
  "type": "network_adapter",                      // network_adapter | upstream_api
  "status": "active",                             // active | inactive
  "baseUrl": "https://provider-network-vistaar.da.gov.in/beckn",   // https only
  "role": "provider",                             // provider | consumer | network
  "keys": {                                       // ONE key; rotation replaces it
    "alg": "ed25519",                             // ed25519 signs, x25519 encrypts
    "key": "xq4+2oQ6MgSZdHHBMtNd1TmnPTmzY5UoZlqzf0yn6ZA=",          // 44 chars = 32 raw bytes
    "validFrom": "2026-08-01T00:00:00Z",
    "validUntil": "2026-11-01T00:00:00Z",         // optional; absent = open-ended. No successor
    "status": "active"                            // to overlap with, so this date is a deadline
  } } }
```

```jsonc
{ "Participant": {
  "participantId": "mausamgram",                  // also the Beckn offer.provider.id
  "name": "IMD Mausamgram NWP",
  "type": "upstream_api",                         // does not speak Beckn: no role, no keys
  "status": "active",
  "baseUrl": "https://mausamgram.imd.gov.in"      // the host; a binding's path is appended
} }                                               // no auth: the credential is the plugin's
```

**`keys` is one key, not a list.** A `network_adapter` therefore holds a signing
key *or* an encryption key, never both, and cannot hold an old and a new key at
once: rotation is a full replace and a hard cutover, and anything signed between
the write and the last verifier refreshing does not verify. `alg` is what says
which of the two it is. `keyId` and `use` survive as **optional** fields — `keyId`
still names the key in the `Authorization` header, one name out of one, which is
what makes that field survivable if a second key is ever needed.

**No credential we present lives in these schemas.** `keys` is *their* public
material, which we use to verify what they sent, and it is publishable — it is
reproduced in full below. *Our* credential for calling an upstream is not a field
here at all: the binding's plugin reads it from the adapter's own environment,
alongside the DSN it already reads there. So there is no secret in the registry
and no field for somebody to log whole — and **none of the three schemas carries
a `_osConfig.privateFields`**, because there is nothing left for one to redact.
That is the strongest form of the property: a read *cannot* leak a credential,
rather than being configured not to.

The cost is that authenticating becomes code rather than configuration. One
declarative `auth.scheme` used to let a single HTTP client serve every upstream;
now each plugin authenticates itself, so onboarding an upstream is a code change
and four plugins can get it wrong four ways.

Why a discriminator rather than a `oneOf` over two wrapper objects: `if/then`
tells a reader "`role` is a required property", where `oneOf` says "is not valid
under any of the given schemas" and leaves them to work out which half they were
in. It also makes `type` a real field, so a seeding-time check can refuse a
binding that points at a `network_adapter`, and Sunbird RC's `/search` — which indexes
top-level fields only — can filter on `baseUrl` and `type` at all.

An API and the adapter in front of it are separate deployables, so separate
records: `mausamgram` is IMD's API, `provider-network-vistaar.da.gov.in` is the
network adapter that calls it. Which upstreams a provider node fronts is that adapter's
config.

### `ProviderSchema`

One row is one provider and one capability — the internal binding between one
participant and one capability. The OAN Registry interface exposes the
**resolved** invocation details; it does not expose this entity. Everything that varies per **Beckn
action** — the URL, the method, the mapping file, the timeout — varies inside
`actions[]`, because a capability can need `select` on one endpoint and `confirm`
on another.

| | On the row | On an `actions[]` entry |
|---|---|---|
| required | `bindingKey`, `participantId`, `capabilityCode`, `status`, `actions` | `action`, `method`, `path`, `mappings`, `status` |
| optional | | `timeoutMs`, `retryMax` |

```jsonc
{ "ProviderSchema": {
  "bindingKey": "mausamgram|openagrinet:WeatherObservation",   // <participantId>|<capabilityCode>
  "participantId": "mausamgram",                  // an active upstream_api — read from HERE, never from the request
  "capabilityCode": "openagrinet:WeatherObservation",   // must be an active SchemaRegistry
  "status": "active",                             // retires the whole binding
  "actions": [ {                                  // 1–10, one entry per action
    "action": "select",                           // discover | select | init | confirm | status | track | cancel | update | rate | support
    "method": "GET",                              // GET | POST
    "path": "/nwpapi/get-daily",                  // appended to that upstream's baseUrl; any depth
    "mappings": "mappings/mausamgram/weather-observation.select.yaml",   // both directions, one file
    "timeoutMs": 30000,                           // optional, 1000–120000, default 15000
    "retryMax": 3,                                // optional, 0–5, default 0
    "status": "active"                            // retires this action only
  } ] } }
```

**A per-action `status` is why this is an array and not a keyed object.**
Retiring `confirm` is one field on one entry; the capability, the other actions
and every published resource are untouched.

**An `on_*` callback is not an action here.** `on_select` is the `response:` half
of the `select` entry — the same HTTP round trip — which is also why one file
holds both directions:

```yaml
# mappings/mausamgram/weather-observation.select.yaml
request: |
  { "lat": $string(_local.lat), "lon": $string(_local.lon) }

response: |
  $map([1..5], function($i) {{
    "@type": "openagrinet:WeatherObservation",
    "informationMode": "Direct",
    "observationType": "Forecast",
    "source": "IMD Mausamgram NWP",
    "location": { "type": "Point",
                  "coordinates": [ $number(location.lon), $number(location.lat) ] },
    "generatedAt": $now(),
    "parameters": [
      { "parameter": "Rainfall",    "value": $lookup($, "fcstday" & $i).rain, "unit": "mm" },
      { "parameter": "Temperature", "value": $lookup($, "fcstday" & $i).tmax, "unit": "Cel" }
    ] }})
```

The response mapping only works because the request swapped GeoJSON
`[lon, lat]` into `lat`/`lon`. Splitting them across two registry fields hid
that; one file per binding-action does not. YAML block scalars carry JSONata with
no escaping, and the filename's action segment must equal the `action` it sits
under — a mismatch would apply a correct mapping to the wrong call, silently.

**What the Beckn body cannot express is the plugin's, and no field here names
it.** `agmarknet` wants a market and commodity code, `imd-city-weather` a station
id; neither is derivable from the request. The adapter's plugin for this
binding-action produces them into `_local`, and the request mapping is evaluated
over `{ request, _local }`. Naming that plugin here would buy nothing:
`bindingKey` plus `action` already selects it, exactly as it selects the mapping,
so a name is a second way to say the same thing and a first way to disagree — and
unverifiable either way, since it would be a string, not a reference.

**Having the plugin is therefore a seeding prerequisite.** A binding whose plugin
does not exist validates, seeds and returns nothing useful.

### Five rules JSON Schema cannot express

These hold and nothing enforces them:

1. A binding's `participantId` must name an `active` `Participant` of type
   `upstream_api` — a binding pointing at a `network_adapter` resolves to a call
   that cannot be made.
2. A binding's `capabilityCode` must name an `active` `SchemaRegistry`.
3. `SchemaRegistry.version` must equal the `vN.N` segment of its `schemaUrl`.
   The schema cannot compare two fields.
4. A mapping filename's action segment must equal the `action` it sits under.
5. The plugin selected by `bindingKey` + `action` must exist.

## The registry's own API

**These routes are internal.** Sunbird RC generates them from the three schemas
and the OAN Registry wrapper is what calls them; an external caller uses the OAN
envelope and the Registry interface instead, and never sees an `osid` or a
Sunbird entity wrapper. `<Entity>` is `Participant`, `SchemaRegistry` or
`ProviderSchema`.

| Route | Who | What |
|---|---|---|
| `POST /api/v1/<Entity>` | `registryOperator` | create |
| `POST /api/v1/<Entity>/search` | authenticated | look up by an indexed field |
| `GET /api/v1/<Entity>/{osid}` | authenticated | read one |
| `PUT /api/v1/<Entity>/{osid}` | `registryOperator` | replace in full |
| `DELETE /api/v1/<Entity>/{osid}` | — | **disabled** |

- **The body is wrapped** one level under the entity name, so every record below
  is a write body exactly as it stands.
- **`osid` is RC's row id**, returned by the create. It is not `participantId`
  and not `bindingKey`, so an update has to search first.
- The *who* column is intent, not enforcement. `_osConfig.roles` gates the
  **entity, not the verb**, so on the pinned build any token that can read these
  records can also write them. No record holds a credential, so that is no longer
  a disclosure risk — it is still a write anybody with a read token can make, and
  it must be closed before v1 carries traffic.

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
  "type": "upstream_api",
  "status": "active",
  "baseUrl": "https://api.agmarknet.gov.in" } }
```
```json
200 OK
{ "id": "sunbird-rc.registry.create",
  "params": { "status": "SUCCESSFUL" },
  "result": { "Participant": { "osid": "1-8f2c4e7a-3b91-4d0e-9c55-2a1f6b8e0d34" } } }
```

### Search

```http
POST /api/v1/ProviderSchema/search
Authorization: Bearer <read-token>
```
```json
{ "filters": { "bindingKey": { "eq": "agmarknet|openagrinet:MandiPrice" },
               "status":     { "eq": "active" } } }
```

Only indexed fields can be filtered:

| Entity | Unique | Also indexed |
|---|---|---|
| `SchemaRegistry` | `capabilityCode` | `status` |
| `Participant` | `participantId` | `status`, `type`, `baseUrl` |
| `ProviderSchema` | `bindingKey` | `participantId`, `capabilityCode`, `status` |

`type` and `baseUrl` are indexed because flattening made them indexable — RC
filters on top-level fields only, so a nested `upstream_api.baseUrl` could not be
searched at all. `type` is the useful one: it separates the network adapters from
the upstream APIs in one `eq`.

**Search is still not public**, but it is no longer holding back a secret. What a
read does expose is the network's shape: who its participants are, which hosts
they front, and what each is bound to.

**The unique index is a single field, so a duplicate is a silent overwrite, not
an error.**

### Update

`PUT` replaces; it is not a merge patch. Search for the `osid`, change the field,
send the whole record back.

**Because `PUT` replaces, a field you omit is a field you delete.** Omitting
`keys` leaves a `network_adapter` with no key at all — there is one, so there is no second one
to fall back to — and dropping an entry from a binding's `actions` removes that
action. Both silently. Changing one action's timeout means sending the whole
array back, so read the record first and edit what you read.

Rotating a `network_adapter`'s key is such a `PUT`. Because it has no successor to
overlap with, every verifier must reload before the adapter signs with the new
material. Rotating an **`upstream_api`** credential is not a registry write at all — it touches no
record, only the adapter's environment.

### Delete is disabled

The route is closed at the gateway; no token carries the right to call it.
Deactivate instead — `PUT` the same record with `"status": "inactive"`. Three
reasons, worst first:

1. **A delete orphans silently.** RC enforces no reference between entities, so
   removing a `Participant` leaves its `ProviderSchema` rows resolving by
   `bindingKey` to nothing. The call fails at request time with no clue the cause
   was a registry write weeks earlier.
2. **Published catalogs outlive the record.** Resources already advertised carry
   a `provider.id`; deleting the row that explains it makes them unresolvable
   without making them disappear.
3. **`inactive` is as complete and leaves evidence.** Every read filters on
   `active`.

A genuine erasure is an operator task against the database with a reason
recorded.

### The runtime does not call these per request

All sixteen records are a few kilobytes. The adapter loads them at boot and
indexes them by `bindingKey` and `participantId`, so resolving a `select` is two
map lookups — the row, then its `actions[]` entry for the action — and the
registry contributes **zero** latency and zero availability risk to the request
path.

A registry change takes effect on the next reload. That is the tradeoff, and the
right one for records that change on operator action rather than on traffic. It
is also why `status: "revoked"` on a key is inert until that reload.

## The records that seed v1

Sixteen records: 3 `SchemaRegistry`, 8 `Participant`, 5 `ProviderSchema`. Seed in
that order — a binding's integrity rules need the other two to exist and be
`active`.

### Capabilities

```json
{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:WeatherObservation",
  "name": "Weather Observation and Forecast",
  "version": "v0.1",
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active" } }

{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:MandiPrice",
  "name": "Mandi Price",
  "version": "v0.1",
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/MandiPrice/v0.1/attributes.yaml",
  "status": "active" } }

{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:KnowledgeResource",
  "name": "Knowledge Resource",
  "version": "v0.1",
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/KnowledgeResource/v0.1/attributes.yaml",
  "status": "active" } }
```

`schemaUrl` is pinned to a **version directory**, not a commit: a breaking change
is published as `v0.2`, so `v0.1` means the same document next week. It still
points at `main` while the packs live on tag `schema-packs-v0.1` — a moving
target for a field whose purpose is to name something stable, and worth closing
before v1 carries traffic.

One record serves both Advisory categories: Schemes and Crop & Pest are the same
outcome type, told apart on the published resource by `subjectCategories`.

### Network adapters

Keys below are demo material.

```json
{ "Participant": {
  "participantId": "seeker-network-vistaar.da.gov.in",
  "name": "Kisan app consumer adapter",
  "type": "network_adapter", "status": "active",
  "baseUrl": "https://seeker-network-vistaar.da.gov.in/beckn",
  "role": "consumer",
  "keys": { "alg": "ed25519",
            "key": "s3Q/53+xYL/BgelYdsKd7DBgYDUFLsXE+GQDLSuPZ4c=",
            "validFrom": "2026-08-01T00:00:00Z", "status": "active" } } }

{ "Participant": {
  "participantId": "discovery-network-vistaar.da.gov.in",
  "name": "OpenAgriNet network node",
  "type": "network_adapter", "status": "active",
  "baseUrl": "https://discovery-network-vistaar.da.gov.in/beckn",
  "role": "network",
  "keys": { "alg": "ed25519",
            "key": "q7fEHdFO7wNpYBARwY+qvhGhRzlrlJWRR64NIwQhO2A=",
            "validFrom": "2026-08-01T00:00:00Z", "status": "active" } } }

{ "Participant": {
  "participantId": "provider-network-vistaar.da.gov.in",
  "name": "OpenAgriNet Network Adapter",
  "type": "network_adapter", "status": "active",
  "baseUrl": "https://provider-network-vistaar.da.gov.in/beckn",
  "role": "provider",
  "keys": { "alg": "ed25519",
            "key": "xq4+2oQ6MgSZdHHBMtNd1TmnPTmzY5UoZlqzf0yn6ZA=",
            "validFrom": "2026-08-01T00:00:00Z",
            "validUntil": "2026-11-01T00:00:00Z", "status": "active" } } }
```

The provider node is the case that shows what one key costs: its key carries
`validUntil` 1 November, and because there is no successor to overlap with, that
date is a deadline after which it cannot sign at all.

One provider node fronts all five upstreams below. Which ones is that adapter's
config.

### Upstream APIs

Five external APIs. None has heard of Beckn; each appears on the wire as
`offer.provider.id`.

```json
{ "Participant": { "participantId": "mausamgram",
  "name": "IMD Mausamgram NWP", "type": "upstream_api", "status": "active",
  "baseUrl": "https://mausamgram.imd.gov.in" } }

{ "Participant": { "participantId": "imd-city-weather",
  "name": "IMD City Weather", "type": "upstream_api", "status": "active",
  "baseUrl": "https://city.imd.gov.in" } }

{ "Participant": { "participantId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "type": "upstream_api", "status": "active",
  "baseUrl": "https://api.agmarknet.gov.in" } }

{ "Participant": { "participantId": "hasura-content",
  "name": "Vistaar Knowledge Content (Hasura)", "type": "upstream_api",
  "status": "active", "baseUrl": "https://content.internal" } }

{ "Participant": { "participantId": "oan-vector",
  "name": "OAN Vector Index", "type": "upstream_api", "status": "active",
  "baseUrl": "http://3.6.146.174:8882" } }
```

`mausamgram` and `imd-city-weather` are both IMD on different hosts, so two
records — one organisation looking like two participants. One `Participant` is
one **host**: `mausamgram`'s `baseUrl` stops at the hostname and `/nwpapi` moved
into the binding's `path`, so a second endpoint on the same host at any depth
needs nothing new. A second endpoint on a *different* host is still a second
`Participant`, now because the plugin's credential is chosen per binding and the
host it may reach is the one its own record names.

**`oan-vector` is a bare IP over plain HTTP.** That used to be legal *because*
`scheme: none` said no credential rode on it. With no `auth` field the schema
cannot distinguish a credentialled call from an uncredentialled one, so plaintext
is now permitted for every `upstream_api` and nothing refuses a credentialled plugin
pointed at an `http://` host. Keeping that true is the plugin's job, and nothing
checks it. This is a real loss and is listed under [Known gaps](#known-gaps).

### Bindings

```json
{ "ProviderSchema": {
  "bindingKey": "mausamgram|openagrinet:WeatherObservation",
  "participantId": "mausamgram",
  "capabilityCode": "openagrinet:WeatherObservation", "status": "active",
  "actions": [
    { "action": "select", "method": "GET", "path": "/nwpapi/get-daily",
      "mappings": "mappings/mausamgram/weather-observation.select.yaml",
      "timeoutMs": 30000, "retryMax": 3, "status": "active" } ] } }

{ "ProviderSchema": {
  "bindingKey": "imd-city-weather|openagrinet:WeatherObservation",
  "participantId": "imd-city-weather",
  "capabilityCode": "openagrinet:WeatherObservation", "status": "active",
  "actions": [
    { "action": "select", "method": "GET", "path": "/api/cityweather_loc.php",
      "mappings": "mappings/imd-city-weather/weather-observation.select.yaml",
      "timeoutMs": 15000, "status": "active" } ] } }

{ "ProviderSchema": {
  "bindingKey": "agmarknet|openagrinet:MandiPrice",
  "participantId": "agmarknet",
  "capabilityCode": "openagrinet:MandiPrice", "status": "active",
  "actions": [
    { "action": "select", "method": "GET", "path": "/v1/fetch-agmarknet-vistaar-location",
      "mappings": "mappings/agmarknet/mandi-price.select.yaml",
      "timeoutMs": 20000, "retryMax": 2, "status": "active" } ] } }

{ "ProviderSchema": {
  "bindingKey": "hasura-content|openagrinet:KnowledgeResource",
  "participantId": "hasura-content",
  "capabilityCode": "openagrinet:KnowledgeResource", "status": "active",
  "actions": [
    { "action": "select", "method": "POST", "path": "/v1/graphql",
      "mappings": "mappings/hasura-content/knowledge-resource.select.yaml",
      "timeoutMs": 15000, "retryMax": 0, "status": "active" } ] } }

{ "ProviderSchema": {
  "bindingKey": "oan-vector|openagrinet:KnowledgeResource",
  "participantId": "oan-vector",
  "capabilityCode": "openagrinet:KnowledgeResource", "status": "active",
  "actions": [
    { "action": "select", "method": "POST", "path": "/indexes/oan-index/search",
      "mappings": "mappings/oan-vector/knowledge-resource.select.yaml",
      "timeoutMs": 15000, "status": "active" } ] } }
```

**Every seeded action is `select`.** `discover` is answered from the published
catalog and never calls an upstream, so it has no binding. A second action on a
provider is a second entry in that provider's array, not a second row — the shape
is there because a subscription capability needs `confirm` on a different URL
from `select`, with its own timeout.

**All five need a step no field here names**: `mausamgram` a point lifted out of
the intent, `imd-city-weather` the nearest station, `agmarknet` market and
commodity codes, both `KnowledgeResource` bindings their query parameters. That
step is the adapter plugin's.

### Before seeding

- **Reads are authenticated.** Seeding needs the operator token, the adapter a
  read-only one.
- **The read-only role does not exist yet** — any token that can read these can
  also write them. Close it before v1 carries traffic.
- **Check `version` against `schemaUrl`.** The schema cannot compare two fields,
  and since the `verify/` checkers were removed nothing does.
- **There is no delete.** A correction is a full `PUT`, or `status: "inactive"`.
- `agmarknet`'s request mapping must emit `lat`, `long`, `commodity_id` and a
  single `date` — the older four-code endpoint is not what production calls.
- Three bindings emit responses the domain packs reject. Seeding is unaffected;
  the mappings are not.

## Six farmer questions

| # | Question | Capability | Provider |
|---|---|---|---|
| 1 | will it rain in the next five days, here? | `openagrinet:WeatherObservation` | `mausamgram` |
| 2 | …and for my city? | `openagrinet:WeatherObservation` | `imd-city-weather` |
| 3 | what is today's mandi price? | `openagrinet:MandiPrice` | `agmarknet` |
| 4 | which scheme am I eligible for? | `openagrinet:KnowledgeResource` | `hasura-content` |
| 5 | what is eating my crop? | `openagrinet:KnowledgeResource` | `oan-vector` |
| 6 | should I spray this week? | *weather advisory* | **not seeded** |

### Synchronous, and the spec has not caught up

Bharat Vistaar executes every transaction synchronously. `/discover` returns the
catalogs and `/select` returns the data, each on the connection the caller is
holding open. `on_discover` and `on_select` are response **bodies**, not inbound
calls.

`beckn.yaml` v2.0.0 does not describe that. `/discover` and `/select` declare
`200` → `Ack`, whose own description reads:

> The implementer has authenticated the request, accepted it for processing, and
> **MUST deliver the business outcome asynchronously via the corresponding
> callback endpoint.**

`202` → `AckNoCallback`, whose body is an `Error` explaining why no callback will
follow — not a payload either. Of the 27 paths in the document exactly two are
synchronous, `/catalog/subscription` and `/catalog/search`, and both are
Cataloging Service fabric endpoints rather than the transaction path.
`context.try` is not an escape hatch: it is `update`/`cancel` only, and receiving
actors "MUST ignore it if present" everywhere else.

So this is a deviation on a MUST — but a **decided** one. Synchronous is the
transport for every transaction here, and `beckn.yaml` is to be amended to
describe it; until that lands, the schema is the thing that is behind. Four
consequences hold either way:

| | |
|---|---|
| **Config** | `targetType: "url"` on every route. The transaction-callback halves of the ONIX config never fire — dormant by design, not misconfigured |
| **Counterparty URIs still matter** | They are how the counterparty is *resolved* — `context.networkId` names the subnet registry, which returns the subscriber URL and key material. Nothing is delivered to them |
| **The retry budget becomes the farmer's wait** | `mausamgram`'s `select` entry is seeded `timeoutMs: 30000, retryMax: 3` — up to **120 s** on an open connection. Async hides retries; sync bills them to the farmer. Whether to cap the sync path at one attempt is open |
| **Interop** | Until the amendment lands, a consumer built to the published schema waits for a callback that never arrives. Sync holds while both ends are BV-operated; a third party calling in needs the amended spec |

Errors are NACK-only on the open connection: no partial results, no silent
empties.

### Who signs what

| Hop | Signed by | Verified by |
|---|---|---|
| `discover` | consumer node | network node |
| `on_discover` — the 200 body | network node | consumer node |
| `select` | consumer node | provider node |
| `on_select` — the 200 body | provider node | consumer node |

`networkId` is `da.gov.in/vistaar`, the spec's RECOMMENDED
`namespace_id/registry_id` shape. Bharat Vistaar production today is **Beckn
v1.1.0 with `search`**, domain `schemes:vistaar`; v2.0.0 has no `domain` field at
all, and a single `search` has nowhere to carry the point and validity window
that `select` carries. Moving BV to v2.0.0 is a network-owner decision, not a
service one.

### Walkthrough — weather forecast for a point

*"पुढच्या पाच दिवसांत पाऊस पडेल का?"* Device location Nashik,
`[73.7898, 19.9975]`.

**The capability is `WeatherObservation`, not `WeatherAdvisory`.** A forecast
carrying values is an observation with `observationType: "Forecast"`.
`WeatherAdvisory` is a real pack — it is what question 6 needs — and nothing
seeds it.

#### 0. The experience layer resolves meaning

Before any Beckn call, the utterance becomes concepts:

| From | Concept |
|---|---|
| "पाऊस" | subject area `Weather`, parameter `Rainfall` |
| device GPS | point `[73.7898, 19.9975]` |
| "पुढच्या पाच दिवसांत" | validity `2026-08-26 .. 2026-08-30` |

This is the boundary the design rests on: **the experience layer owns meaning,
the adapter owns encoding.** The experience layer does not know that IMD calls
this `fcstday1..fcstday5`, and it must not.

#### 1. `discover`

```json
POST /discover
{
  "context": { "version": "2.0.0", "action": "discover",
    "networkId": "da.gov.in/vistaar",
    "senderId": "seeker-network-vistaar.da.gov.in",
    "receiverId": "discovery-network-vistaar.da.gov.in",
    "transactionId": "9f2c1a8e-4b70-4d31-9c55-6f2e0b1d7a44",
    "messageId": "1c0a55d7-8e64-4b19-9a2f-33b7c6e1d905",
    "timestamp": "2026-08-26T06:11:58.004Z" },
  "message": { "intent": {
    "textSearch": "weather forecast rain next five days",
    "filters": {
      "type": "jsonpath",
      "expression": "$.catalogs[*].resources[*] ? (@.resourceAttributes.\"@type\" == \"openagrinet:WeatherObservation\")"
    },
    "spatial": [{
      "op": "S_DWITHIN",
      "targets": "$['catalogs'][*]['provider']['availableAt'][*]['geo']",
      "geometry": { "type": "Point", "coordinates": [73.7898, 19.9975] },
      "distanceMeters": 250000, "quantifier": "ANY"
    }]
  } }
}
```

`Authorization` carries the consumer node's signature; the network node resolves
`seeker-network-vistaar.da.gov.in` against the subnet registry named by
`networkId` to get the key that verifies it. Only then is `senderId` worth
reading.

#### 2. The `200` — `on_discover` as a body

Synchronous, so this **is** the response to the call above. `context` is merged
rather than rebuilt, so `transactionId`, `messageId` and `timestamp` survive, and
the sender/receiver pair is swapped:

```jsonata
"context": request.context ~> |$|{ "action": "on_discover",
                                   "senderId": request.context.receiverId,
                                   "receiverId": request.context.senderId }|
```

Two providers match; which to pick is the app's call, not the registry's. One of
them, abridged:

```json
{ "id": "cat:mausamgram:weather",
  "descriptor": { "code": "IMD-NWP-01", "name": "IMD Mausamgram NWP" },
  "provider": {
    "id": "mausamgram",
    "descriptor": { "code": "IMD-NWP-01", "name": "IMD Mausamgram NWP" },
    "availableAt": [{ "geo": { "type": "Polygon", "coordinates": [[[68.1,8.0],[97.4,8.0],[97.4,37.1],[68.1,37.1],[68.1,8.0]]] } }]
  },
  "resources": [{
    "id": "res:mausamgram:point-forecast",
    "descriptor": { "name": "Five-day point forecast" },
    "resourceAttributes": {
      "@context": "https://schemas.openagrinet.global/schema/WeatherObservation/v0.1/context.jsonld",
      "@type": "openagrinet:WeatherObservation",
      "informationMode": "OnDemand",
      "supportedObservationTypes": ["Forecast"],
      "supportedParameters": ["Rainfall", "Temperature", "Humidity", "WindSpeed", "WindDirection"],
      "geographicGranularity": ["Point"],
      "forecastHorizon": "P5D",
      "updateFrequency": "PT12H",
      "subjectCategories": ["Weather"]
    }
  }],
  "offers": [{ "id": "offer:mausamgram:open-data",
               "resourceIds": ["res:mausamgram:point-forecast"] }] }
```

**`informationMode: OnDemand` and no values** — that is the difference between the
hops, and the pack enforces it: `OnDemand` *requires*
`supportedObservationTypes`, `supportedParameters` and `geographicGranularity`,
and carries `not: {required: [parameters]}`. An advertisement that leaked a value
would be rejected. The advertisement says what forms it *can* return.

#### 3. `select` — consumer node straight to the provider node

The **consumer node** builds this, not the network node. `DRAFT` and no price:
nothing is being committed. For an open-data provider the quote is zero-cost and
the payload *is* the data. New `messageId`, same `transactionId`.

```json
POST https://provider-network-vistaar.da.gov.in/beckn/select
{
  "context": { "version": "2.0.0", "action": "select",
    "networkId": "da.gov.in/vistaar",
    "senderId": "seeker-network-vistaar.da.gov.in",
    "receiverId": "provider-network-vistaar.da.gov.in",
    "transactionId": "9f2c1a8e-4b70-4d31-9c55-6f2e0b1d7a44",
    "messageId": "7d41b9e0-52a6-4c18-8b73-1e9f0a4c6d22",
    "timestamp": "2026-08-26T06:12:01.330Z" },
  "message": { "contract": { "commitments": [{
    "status": { "descriptor": { "code": "DRAFT", "name": "Draft" } },
    "resources": [{
      "id": "res:mausamgram:point-forecast",
      "resourceAttributes": {
        "@context": "https://schemas.openagrinet.global/schema/WeatherObservation/v0.1/context.jsonld",
        "@type": "openagrinet:WeatherObservation",
        "subjectCategories": ["Weather"],
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

#### 4. Resolve — the provider node reads the registry, twice

Two registries, and they are not the same store. The provider node has already
read the **subnet registry** to verify the signature — `networkId` → registry URL
→ lookup on `senderId` → key material. What follows is the **capability
registry**, these three tables, and it is read on the **provider side only**. If
the consumer side resolved capabilities it would need the upstream credentials,
which is the whole thing this split exists to prevent: the consumer must never
learn that `mausamgram` means `https://mausamgram.imd.gov.in`.

```
offer.provider.id            → "mausamgram"
resourceAttributes["@type"]  → "openagrinet:WeatherObservation"
bindingKey                   = "mausamgram|openagrinet:WeatherObservation"
context.action               → "select"                    → actions[] entry
```

**Read 1 — the call plan.** `ProviderSchema` by `bindingKey`, then the `actions[]`
entry whose `action` is `select`, giving `GET /nwpapi/get-daily`,
`timeoutMs: 30000`, `retryMax: 3`. Those are per-action registry fields, not
service constants — IMD is slow, and an operator changes this without a deploy.
An entry whose own `status` is `inactive` is not a match.

**Read 2 — where it is.** `Participant` by the `participantId` **from row 1, never
from the request**. `baseUrl` is the host and the entry's `path` is appended, so
the call can only ever reach the host its own record names. A request that could
name the participant could point a credentialled call at a host of its choosing.

**How to authenticate is not one of these reads.** No registry field holds it. The
plugin presents the credential, reading it from the adapter's own environment.

An empty result from either read is a hard failure — `BIZ_PROVIDER_NOT_FOUND`,
not a fallback. There is **no `SchemaRegistry` read**: a capability is
vocabulary, not part of the call path.

#### 5. Enrich, map, call

**Enrich first.** The plugin runs *before* the request mapping, because the
mapping is evaluated over `{ request, _local }` and `_local` is what the plugin
produces:

```
resourceAttributes.location.coordinates  →  _local = { "lat": 19.9975, "lon": 73.7898 }
```

Trivial here, because mausamgram takes a raw point — and this walkthrough is the
wrong place to look for the interesting case. A station resolver is question 2;
market and commodity codes are question 3. Both are PostGIS lookups off the
point, and both read their DSN from the plugin's own environment.

**Then map**, using the `request:` half of the file the `select` entry named:

```jsonata
{ "lat": $string(_local.lat), "lon": $string(_local.lon) }
```

**GeoJSON is `[longitude, latitude]`** and mausamgram wants `lat`/`lon`, so the
mapping swaps them — getting it backwards returns a forecast for the Arabian Sea
with no error. `$string()` is not cosmetic either; the endpoint rejects unquoted
numerics.

```
GET https://mausamgram.imd.gov.in/nwpapi/get-daily?lat=19.9975&lon=73.7898
Authorization: Basic <presented by the binding's plugin, from its own environment>
timeout 30000ms   retries 3
```

Captured response, one of its five day-blocks:

```json
{
  "lat_r": 19.875, "lon_r": 73.875,
  "fcstday1": {
    "date": "2026-08-26", "rain": 0.84, "tmax": 39.24, "tmin": 32.8,
    "wdir": 273.39, "wind": ["W", "Westerly"], "wspd": 2.83, "cloud": 74.44,
    "rhmax": 58.67, "rhmin": 40.81, "tmax_raw": 37.13, "tmin_raw": 32.92,
    "rain_message": "Light Rain", "cloud_message": "Generraly Cloudy Sky",
    "weather_warning": "Generally Cloudy Sky"
  },
  "location": { "lat": 19.9975, "lon": 73.7898 },
  "abbreviation": {
    "rain": "Rainfall (mm)",
    "tmax": "Maximum Temperatue (Celsius) - Real Time Bias Corrected",
    "wdir": "Wind Direction (degree)", "wspd": "Wind Speed (m/s)",
    "cloud": "Total Cloud Cover (%)", "rhmax": "Maximum Relative Humidity (%)"
  }
}
```

Four things in there that bite:

- **`lat_r`/`lon_r` is not `location`.** `lat_r`/`lon_r` is the model grid point
  the forecast was computed at; `location` echoes what was asked for. They differ
  by about 9 km here. This walkthrough publishes the **requested** point — a
  decision, not a fact.
- **`abbreviation` is the only source of units.** The pack requires a `unit` on
  every parameter and the values carry none. Read them from here; don't guess.
- **`tmax` is bias-corrected, `tmax_raw` is not.** Use `tmax`.
- **`cloud_message` is misspelled upstream** (`"Generraly"`). Match on numbers,
  not text.

#### 6. The `200` — `on_select` as a body

The same file's `response:` half runs over `{ request, response, _local }`;
`_local` stays in scope so the resolved point reaches the output. The provider
node signs the result and returns it on the still-open connection.

`on_select` requires a `contract`, so resources travel inside a commitment. One
of five days:

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
      { "parameter": "Rainfall",      "aggregation": "Total",   "unit": "mm",  "value": 0.84 },
      { "parameter": "Temperature",   "aggregation": "Maximum", "unit": "Cel", "value": 39.24 },
      { "parameter": "Temperature",   "aggregation": "Minimum", "unit": "Cel", "value": 32.8 },
      { "parameter": "Humidity",      "aggregation": "Maximum", "unit": "%",   "value": 58.67 },
      { "parameter": "WindSpeed",     "aggregation": "Mean",    "unit": "m/s", "value": 2.83 },
      { "parameter": "WindDirection", "aggregation": "Mean",    "unit": "deg", "value": 273.39 }
    ]
  }
}
```

Only `informationMode` changed between the hops — `OnDemand` → `Direct`. `@type`
is a **single string**; the two-element array form some examples show fails
validation.

### The other five

Same two hops, same envelope. What differs is the call plan and the upstream's
quirks.

| # | Provider | Call | The thing that bites |
|---|---|---|---|
| 2 | `imd-city-weather` | `GET /api/cityweather_loc.php?id=<station>` | Keyed by station id, which no Beckn field carries — the adapter owns the nearest-station table. Returns an **array**, `"NIL"` as a rainfall sentinel, `null`s, and every number as a **string**. Live path is not the documented `/citywx/city_weather_test.php` |
| 3 | `agmarknet` | `GET /v1/fetch-agmarknet-vistaar-location` | Needs `lat`, `long`, `commodity_id`, one `date`. Returns **Title Case keys with spaces** (`"Max Price"`, `"Modal Price"`, `"Price Unit": "Rs./Qtl"`); a mapping written against snake_case returns nothing at all, with no error |
| 4 | `hasura-content` | `POST /v1/graphql` | GraphQL `variables` block built by the adapter. Illustrative — this provider appears in no captured workbook row |
| 5 | `oan-vector` | `POST /indexes/oan-index/search` | Same capability as 4, different request shape: a vector search body, not a GraphQL one. Illustrative |
| 6 | *weather advisory* | — | **Not seeded**, but now seedable: a second capability on `mausamgram` is a second row, and its mapping file no longer collides with the forecast's. What is missing is the mapping and the pack reading, not a convention |

### Conformance

| Provider | Violations |
|---|---|
| `mausamgram` | **0.** All required pack fields present, every `parameter` in the governed enum. Caveat: `aggregation` is ours, not the pack's — it validates because the parameter object is open, and means nothing to another participant, which leaves *"tomorrow's high is 39.24, low 32.8"* with no conformant expression |
| `imd-city-weather` | **0** |
| `agmarknet` | **3** — omits three required fields |
| `hasura-content` | **6** — omits five required fields and uses `knowledgeType` values not in the enum |
| `oan-vector` | **6** — the same six |

These are response-mapping bugs, not registry ones, and seeding is unaffected.
They were found by reading; **nothing validates a mapping's output against the
pack it claims to produce**, so the counts above are a floor.

### Errors

| Code | Means | Who fixes it |
|---|---|---|
| `SCH_REQUIRED_FIELD_MISSING` | the request is malformed | the caller |
| `BIZ_PROVIDER_NOT_FOUND` | no `active` binding or participant | the registry operator |
| `NET_*` | the upstream is down or timed out | nobody — it is a fact |

Synchronous transport changes where these land: a NACK is the response to the
call that caused it, so the caller always sees it. It also means the farmer waits
out `timeoutMs` × attempts before seeing a `NET_*`.

### True in every use case

1. **Two hops, both synchronous.** `discover` finds who; `select` gets what; each
   answer comes back on the connection that asked.
2. **`bindingKey` plus the action is the only route.**
   `<provider>|<capability>`, then the `actions[]` entry matching
   `context.action` — and the participant comes off the binding row, never off
   the request.
3. **The registry never appears in the request path.** Records load at boot;
   resolution is two map lookups.
4. **Identity is `senderId` / `receiverId`.** v2.0.0 declares a second, legacy
   participant pair for backward compatibility; the discovery service models only
   `senderId`/`receiverId` (`src/beckn/types.go`) and its controllers build a
   reply by swapping them. A request carrying only the legacy spellings is
   accepted and answered — nothing rejects it — but the reply then names neither
   party, because there is nothing for the swap to read. Anything calling the
   discovery service should send `senderId`/`receiverId`.

## Known gaps

| | Gap | Status |
|---|---|---|
| 1 | **Prose and JSON are not checked against each other.** The `verify/` checkers were removed when this folder moved | Open. The JSON wins; read it when it matters |
| 2 | **Plaintext is permitted for every `upstream_api`.** With no `auth` field the schema cannot condition `https` on whether a credential rides along | Open. `oan-vector` is the only plaintext row today |
| 3 | **Roles gate the entity, not the verb.** Any token that can read these records can also write them | Must close before v1 carries traffic |
| 4 | **`schemaUrl` points at `main`**, not at the tag the packs live on | Open |
| 5 | **The five integrity rules above are unenforced** — no seeding-time checker exists | Open |
| 6 | **Nothing validates a mapping's output against its pack.** The conformance counts are a floor | Open |
| 7 | **A capability type is advertisable as well as an outcome type.** The packs sanction `WeatherAdvisoryCapability` beside `WeatherObservation`, so a `discover` filter matching only the outcome type makes a conformant provider invisible | Open, and this one lands on the discovery service |
| 8 | **Response body shapes are unverified.** The requests here are corroborated; whether RC returns search rows bare or wrapped is unchecked against the pinned build | Treat response bodies as illustrative |

## Where next

- [`design/registry/schemas/`](design/registry/schemas/) — the draft-07 files,
  which are the contract
- [`publish-and-discover.md`](publish-and-discover.md) — the discovery service
  this registry sits beside
