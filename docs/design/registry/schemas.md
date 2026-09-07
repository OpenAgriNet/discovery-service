# Schemas

Three entities. [`schemas/`](schemas) holds the draft-07 files and is the contract; this page is
the reading of them.

| entity | one row is | unique on |
|---|---|---|
| [`SchemaRegistry`](#schemaregistry) | a data type the network recognises | `capabilityCode` |
| [`Participant`](#participant) | someone the network deals with | `participantId` |
| [`ProviderSchema`](#providerschema) | how to call one provider for one capability | `bindingKey` |

Every row carries `status: "active" \| "inactive"`. Every read filters on `active`.

Which row a field goes on: `Participant` holds what is true of a provider whatever you ask it
for — its `baseUrl`. `ProviderSchema` holds what varies per capability — `method`, `path`, the
mapping file, the timeouts. So a provider serving two capabilities is one `Participant`
and two `ProviderSchema` rows. Neither holds a credential; see below.

---

## `SchemaRegistry`

All five required. Vocabulary only — nothing in the call path reads it.

One open item: the packs also sanction advertising a *capability* type — `schema/` carries
`WeatherAdvisoryCapability`, `AgricultureCapability` and `AdvisoryCapability` beside the outcome
types — so a `/discover` filter matching only the outcome type makes a conformant provider
invisible. That one lands on discovery-service.

```jsonc
{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:WeatherObservation",   // the namespace is literal
  "name": "Weather Observation and Forecast",           // human label
  "version": "v0.1",                                    // must match the vN.N in schemaUrl
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active"
} }
```

---

## `Participant`

Five fields always, then `type` decides the rest. One level, no wrapper object:

| | always | `node` | `upstream` |
|---|---|---|---|
| required | `participantId`, `name`, `type`, `status`, `baseUrl` | `role`, `keys` | — |
| refused | | | `role`, `keys` |

A **node** speaks Beckn. Its `participantId` *is* its network identity — what goes on the wire as
`context.bapId` / `context.bppId`, and field 1 of the `Authorization` keyId. There is no second id
field, because a node id that is also a hostname is one name for one thing; the schema enforces the
hostname shape when `type` is `node`, so `oan-provider` is refused there and
`provider-network-vistaar.da.gov.in` is not.

**`keys` is one key, not a list.** A node therefore holds a signing key *or* an encryption key,
never both, and cannot hold an old and a new key at once: rotation is a full replace and a hard
cutover, and anything signed between the write and the last verifier refreshing does not verify.
`keyId` still names the key in the `Authorization` header — one name out of one, which is what makes
that field survivable if a second key is ever needed.

An **upstream** is an ordinary API. It has not heard of Beckn, so it has no role and no keys, and
its `participantId` is the `offer.provider.id` the farmer sees.

`baseUrl` is one field because it was always one idea: the base something is appended to — a Beckn
action for a node, a binding's `path` for an upstream. It is `https` for a node, unconditionally. For
an upstream it may be plaintext, and that is a **loss**: the rule used to be conditional on
`auth.scheme` being `none`, so plaintext was permitted exactly where nothing could leak. With no
`auth` field the schema cannot tell a credentialled call from an uncredentialled one, so it cannot
condition anything on it. `oan-vector` over `http://` is still the only plaintext row, but nothing
refuses a second one that does carry a credential.

**No credential we present lives in these schemas.** `keys` is *their* public material, which we
use to verify what they sent, and it is publishable — it sits in [examples.md](examples.md) in full.
*Our* credential for calling an upstream is not a field here at all: the binding's plugin reads it
from the adapter's own environment, alongside the DSN it already reads there. So there is no secret
in the registry and no field for somebody to log whole — and **none of the three schemas carries a
`_osConfig.privateFields` at all**, because there is nothing left for one to redact. That is the
strongest form of the property: a read cannot leak a credential, rather than being configured not
to.

The cost is that authenticating becomes code rather than configuration. One declarative
`auth.scheme` used to let a single HTTP client serve every upstream; now each plugin authenticates
itself, so onboarding an upstream is a code change and four plugins can get it wrong four ways.

Why a discriminator rather than a `oneOf` over two wrapper objects: `if/then` tells a reader
"`role` is a required property", where `oneOf` says "is not valid under any of the given schemas"
and leaves them to work out which half they were in. It also makes `type` a real field — so
`verify/records.py` can refuse a binding that points at a node, and RC's `/search`, which indexes
top-level fields only, can filter on `baseUrl` and `type` at all.

```jsonc
{ "Participant": {
  "participantId": "provider-network-vistaar.da.gov.in",   // the only id — and this IS context.bppId
  "name": "OpenAgriNet provider adapter",
  "type": "node",                                   // node | upstream
  "status": "active",                               // active | inactive
  "baseUrl": "https://provider-network-vistaar.da.gov.in/beckn",   // where Beckn messages go; https only
  "role": "BPP",                                  // BAP = consumer node, BPP = provider node, NETWORK = the network node
  "keys": {                                       // ONE key; rotation replaces it
    "keyId": "k1",                                // field 2 of the Authorization keyId — the sender says which key it used
    "use": "sign",                                // sign | encrypt — one key, so one of them
    "alg": "ed25519",                             // fixed by use: sign→ed25519, encrypt→x25519
    "key": "base64:xq4+2oQ6MgSZdHHBMtNd1TmnPTmzY5UoZlqzf0yn6ZA=",   // 44 chars = 32 raw bytes
    "validFrom": "2026-08-01T00:00:00Z",
    "validUntil": "2026-11-01T00:00:00Z",         // optional; absent = open-ended. No successor
    "status": "active"                            // to overlap with, so this date is a deadline
  } } }
```
```jsonc
{ "Participant": {
  "participantId": "mausamgram",                    // also the Beckn offer.provider.id
  "name": "IMD Mausamgram NWP",
  "type": "upstream",                               // does not speak Beckn: no role, no keys
  "status": "active",
  "baseUrl": "https://mausamgram.imd.gov.in"        // the host; a binding's path is appended
} }                                               // no auth: the credential is the plugin's
```

An API and the adapter in front of it are separate deployables, so separate records:
`mausamgram` is IMD's API, `provider-network-vistaar.da.gov.in` is the BPP that calls it. Which
upstreams a provider node fronts is that adapter's config.

---

## `ProviderSchema`

One row is one provider and one capability. Everything that varies per **Beckn action** — the URL,
the method, the mapping file, the timeout — varies inside `actions[]`, because a capability can
need `select` on one endpoint and `confirm` on another.

| | on the row | on an `actions[]` entry |
|---|---|---|
| required | `bindingKey`, `participantId`, `capabilityCode`, `status`, `actions` | `action`, `method`, `path`, `mappings`, `status` |
| optional | | `timeoutMs`, `retryMax` |

```jsonc
{ "ProviderSchema": {
  "bindingKey": "mausamgram|openagrinet:WeatherObservation",   // <participantId>|<capabilityCode>
  "participantId": "mausamgram",                    // an active upstream Participant — read from HERE, never from the request
  "capabilityCode": "openagrinet:WeatherObservation",   // must be an active SchemaRegistry
  "status": "active",                               // retires the whole binding
  "actions": [ {                                    // 1–10, one entry per action
    "action": "select",                             // discover | select | init | confirm | status | track | cancel | update | rate | support
    "method": "GET",                                // GET | POST
    "path": "/nwpapi/get-daily",                    // appended to that upstream's baseUrl; any depth
    "mappings": "mappings/mausamgram/weather-observation.select.yaml",   // both directions, one file
    "timeoutMs": 30000,                             // optional, 1000–120000, default 15000
    "retryMax": 3,                                  // optional, 0–5, default 0
    "status": "active"                              // retires this action only
  } ] } }
```

Two actions on one capability, on different endpoints with different timeouts:

```jsonc
{ "ProviderSchema": {
  "bindingKey": "agmarknet|openagrinet:MandiPrice",
  "participantId": "agmarknet",
  "capabilityCode": "openagrinet:MandiPrice",
  "status": "active",
  "actions": [
    { "action": "select", "method": "GET", "path": "/v1/fetch-agmarknet-vistaar-location",
      "mappings": "mappings/agmarknet/mandi-price.select.yaml",
      "timeoutMs": 20000, "retryMax": 2, "status": "active" },
    { "action": "confirm", "method": "POST", "path": "/v1/alerts/subscribe",
      "mappings": "mappings/agmarknet/mandi-price.confirm.yaml",
      "timeoutMs": 5000, "status": "inactive" } ] } }   // built, not live
```

**A per-action `status` is why this is an array and not a keyed object.** Retiring `confirm` is one
field on one entry; the capability, the other actions and every published resource are untouched.

**An `on_*` callback is not an action here.** `on_select` is the `response:` half of the `select`
entry — the same HTTP round trip — which is also why one file holds both directions:

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

The response mapping only works because the request swapped GeoJSON `[lon, lat]` into `lat`/`lon`.
Splitting them across two registry fields hid that; one file per binding-action does not. YAML
block scalars carry JSONata with no escaping, and the filename's action segment must equal the
`action` it sits under — a mismatch would apply a correct mapping to the wrong call, silently.

**What the Beckn body cannot express is the plugin's, and no field here names it.** `agmarknet`
wants a market and commodity code, `imd-city-weather` a station id; neither is derivable from the
request. The adapter's plugin for this binding-action produces them into `_local`, and the request
mapping is evaluated over `{ request, _local }`. Naming that plugin here would buy nothing:
`bindingKey` plus `action` already selects it, exactly as it selects the mapping, so a name is a
second way to say the same thing and a first way to disagree — and unverifiable either way, since
it would be a string, not a reference. It also keeps the credentials out: a plugin reading PostGIS reads its DSN from its own
environment, and now reads its upstream credential from there too — which is why there is no
secret in these three schemas at all.

Having the plugin is therefore a **seeding prerequisite**. A binding whose plugin does not exist
validates, seeds and returns nothing useful.
