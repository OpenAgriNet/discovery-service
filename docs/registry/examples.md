# Records

The sixteen records that seed v1 — 3 `SchemaRegistry`, 8 `Participant`, 5 `ProviderSchema` — in
Sunbird RC write form. Fields: [schemas.md](schemas.md). Write endpoints: [api.md](api.md).

Seed in order: `SchemaRegistry`, then `Participant`, then `ProviderSchema` — a binding's
integrity rules need the other two to exist and be `active`.

## Schemas

```json
{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:WeatherObservation",
  "name": "Weather Observation and Forecast",
  "version": "v0.1",
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active" } }
```
```json
{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:MandiPrice",
  "name": "Mandi Price",
  "version": "v0.1",
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/MandiPrice/v0.1/attributes.yaml",
  "status": "active" } }
```
```json
{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:KnowledgeResource",
  "name": "Knowledge Resource",
  "version": "v0.1",
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/KnowledgeResource/v0.1/attributes.yaml",
  "status": "active" } }
```

`schemaUrl` is pinned to a **version directory**, not a commit: a breaking change is published as
`v0.2`, so `v0.1` means the same document next week. It still points at `main` while the packs
live on tag `schema-packs-v0.1` — a moving target for a field whose purpose is to name something
stable, and worth closing before v1 carries traffic. One record serves both Advisory
categories — Schemes and Crop & Pest are the same outcome type, told apart on the published
resource by `subjectCategories`.

## Nodes

The three adapters of the [deployment topology](README.md#deployment-topology). Keys below are
demo material.

```json
{ "Participant": {
  "participantId": "oan-consumer",
  "name": "Kisan app consumer adapter",
  "status": "active",
  "node": {
    "subscriberId": "seeker-network-vistaar.da.gov.in",
    "subscriberUrl": "https://seeker-network-vistaar.da.gov.in/beckn",
    "type": "BAP",
    "keys": [ { "keyId": "k1", "use": "sign", "alg": "ed25519",
                "key": "base64:s3Q/53+xYL/BgelYdsKd7DBgYDUFLsXE+GQDLSuPZ4c=",
                "validFrom": "2026-08-01T00:00:00Z", "status": "active" } ] } } }
```
```json
{ "Participant": {
  "participantId": "oan-network",
  "name": "OpenAgriNet network node",
  "status": "active",
  "node": {
    "subscriberId": "discovery-network-vistaar.da.gov.in",
    "subscriberUrl": "https://discovery-network-vistaar.da.gov.in/beckn",
    "type": "NETWORK",
    "keys": [ { "keyId": "k1", "use": "sign", "alg": "ed25519",
                "key": "base64:q7fEHdFO7wNpYBARwY+qvhGhRzlrlJWRR64NIwQhO2A=",
                "validFrom": "2026-08-01T00:00:00Z", "status": "active" } ] } } }
```
```json
{ "Participant": {
  "participantId": "oan-provider",
  "name": "OpenAgriNet provider adapter",
  "status": "active",
  "node": {
    "subscriberId": "provider-network-vistaar.da.gov.in",
    "subscriberUrl": "https://provider-network-vistaar.da.gov.in/beckn",
    "type": "BPP",
    "keys": [ { "keyId": "k1", "use": "sign", "alg": "ed25519",
                "key": "base64:xq4+2oQ6MgSZdHHBMtNd1TmnPTmzY5UoZlqzf0yn6ZA=",
                "validFrom": "2026-08-01T00:00:00Z",
                "validUntil": "2026-11-01T00:00:00Z", "status": "active" },
              { "keyId": "k2", "use": "sign", "alg": "ed25519",
                "key": "base64:1s/FVTLnowPxttooNqWzGCqMZqUrypASpL4jC9NWUA8=",
                "validFrom": "2026-10-01T00:00:00Z", "status": "active" },
              { "keyId": "e1", "use": "encrypt", "alg": "x25519",
                "key": "base64:SzmOZUXmi7jIzJQItBpX/vQA9O3IvEnzZUkc1J/iK7M=",
                "validFrom": "2026-08-01T00:00:00Z", "status": "active" } ] } } }
```

`oan-provider` shows a rotation: `k1` expires 1 Nov, `k2` is valid from 1 Oct, and the October
overlap is the window in which either signature verifies. The sender names the key it used in the
`Authorization` header, so nothing has to guess.

One provider node fronts all five upstreams below. Which ones is that adapter's config.

## Upstreams

Five external APIs. None of them has heard of Beckn; each appears on the wire as
`offer.provider.id`.

```json
{ "Participant": {
  "participantId": "mausamgram",
  "name": "IMD Mausamgram NWP",
  "status": "active",
  "upstream": {
    "baseUrl": "https://mausamgram.imd.gov.in/nwpapi",
    "auth": { "scheme": "basic",
              "secrets": { "username": "env://MAUSAMGRAM_USER",
                           "password": "env://MAUSAMGRAM_X_API_KEY" } } } } }
```
```json
{ "Participant": {
  "participantId": "imd-city-weather",
  "name": "IMD City Weather",
  "status": "active",
  "upstream": { "baseUrl": "https://city.imd.gov.in",
                "auth": { "scheme": "none" } } } }
```
```json
{ "Participant": {
  "participantId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "status": "active",
  "upstream": {
    "baseUrl": "https://api.agmarknet.gov.in",
    "auth": { "scheme": "apiKeyQuery",
              "paramName": "token",
              "secrets": { "token": "env://MANDI_TOKEN" } } } } }
```
```json
{ "Participant": {
  "participantId": "hasura-content",
  "name": "Vistaar Knowledge Content (Hasura)",
  "status": "active",
  "upstream": {
    "baseUrl": "https://content.internal",
    "auth": { "scheme": "apiKeyHeader",
              "paramName": "x-hasura-admin-secret",
              "secrets": { "adminSecret": "env://HASURA_GRAPHQL_ADMIN_SECRET" } } } } }
```
```json
{ "Participant": {
  "participantId": "oan-vector",
  "name": "OAN Vector Index",
  "status": "active",
  "upstream": { "baseUrl": "http://3.6.146.174:8882",
                "auth": { "scheme": "none" } } } }
```

`oan-vector` is a bare IP over plain HTTP, legal only because `scheme: none` — nothing is leaked.
`mausamgram` and `imd-city-weather` are both IMD on different hosts, so two records — one
organisation looking like two participants. One `Participant` is one `baseUrl`; whether a second
endpoint should be a path, a subdomain or a host override on the binding is open, and choosing
before a real case arrives invents semantics.

Every credential is an `env://` pointer resolved in the adapter's environment. No v1 record uses
`inline:`.

## Bindings

```json
{ "ProviderSchema": {
  "bindingKey": "mausamgram|openagrinet:WeatherObservation",
  "participantId": "mausamgram",
  "capabilityCode": "openagrinet:WeatherObservation",
  "method": "GET", "path": "/get-daily",
  "enricher": { "name": "pointFromIntent" },
  "requestMapping":  "mappings/mausamgram/select.request.jsonata",
  "responseMapping": "mappings/mausamgram/select.response.jsonata",
  "timeoutMs": 30000, "retryMax": 3, "status": "active" } }
```
```json
{ "ProviderSchema": {
  "bindingKey": "imd-city-weather|openagrinet:WeatherObservation",
  "participantId": "imd-city-weather",
  "capabilityCode": "openagrinet:WeatherObservation",
  "method": "GET", "path": "/api/cityweather_loc.php",
  "enricher": { "name": "stationFromPoint",
                "secrets": { "dsn": "env://GEO_DSN" } },
  "requestMapping":  "mappings/imd-city-weather/select.request.jsonata",
  "responseMapping": "mappings/imd-city-weather/select.response.jsonata",
  "timeoutMs": 15000, "status": "active" } }
```
```json
{ "ProviderSchema": {
  "bindingKey": "agmarknet|openagrinet:MandiPrice",
  "participantId": "agmarknet",
  "capabilityCode": "openagrinet:MandiPrice",
  "method": "GET", "path": "/v1/fetch-agmarknet-vistaar-location",
  "enricher": { "name": "marketAndCommodityCodes",
                "config":  { "maxDistanceMeters": 50000 },
                "secrets": { "dsn": "env://GEO_DSN" } },
  "requestMapping":  "mappings/agmarknet/select.request.jsonata",
  "responseMapping": "mappings/agmarknet/select.response.jsonata",
  "timeoutMs": 20000, "retryMax": 2, "status": "active" } }
```
```json
{ "ProviderSchema": {
  "bindingKey": "hasura-content|openagrinet:KnowledgeResource",
  "participantId": "hasura-content",
  "capabilityCode": "openagrinet:KnowledgeResource",
  "method": "POST", "path": "/v1/graphql",
  "enricher": { "name": "contentQueryFromIntent" },
  "requestMapping":  "mappings/hasura-content/select.request.jsonata",
  "responseMapping": "mappings/hasura-content/select.response.jsonata",
  "timeoutMs": 15000, "retryMax": 0, "status": "active" } }
```
```json
{ "ProviderSchema": {
  "bindingKey": "oan-vector|openagrinet:KnowledgeResource",
  "participantId": "oan-vector",
  "capabilityCode": "openagrinet:KnowledgeResource",
  "method": "POST", "path": "/indexes/oan-index/search",
  "enricher": { "name": "vectorQueryFromIntent" },
  "requestMapping":  "mappings/oan-vector/select.request.jsonata",
  "responseMapping": "mappings/oan-vector/select.response.jsonata",
  "timeoutMs": 15000, "status": "active" } }
```

`imd-city-weather`'s `path` is `/api/cityweather_loc.php` and not the documented
`/citywx/city_weather_test.php`; it takes `?id=<station code>` and returns an **array** with
`"NIL"` sentinels — [usecases.md](usecases.md#the-other-five).

**Four of the five need a step no field here names.** `mausamgram` a grid point,
`imd-city-weather` the nearest station, `agmarknet` market and commodity codes, both
`KnowledgeResource` bindings their query parameters. Adapter-internal, keyed off
`participantId`, and a seeding prerequisite.

## Forms no seeded record uses

Two probe records, kept so the schema's remaining branches stay exercised by `verify/shape.py`
rather than only claimed.

```json
{ "Participant": {
  "participantId": "probe-multi-secret",
  "name": "Upstream wanting two named credentials",
  "status": "active",
  "upstream": {
    "baseUrl": "https://probe.example/v1",
    "auth": { "scheme": "apiKeyHeader",
              "paramNames": { "client": "x-client-id", "key": "x-api-key" },
              "secrets":    { "client": "env://PROBE_CLIENT_ID",
                              "key":    "env://PROBE_API_KEY" } } } } }
```
```json
{ "Participant": {
  "participantId": "probe-bearer",
  "name": "Upstream wanting a bearer token",
  "status": "active",
  "upstream": {
    "baseUrl": "https://probe.example/v2",
    "auth": { "scheme": "apiKeyHeader",
              "paramName": "Authorization",
              "valuePrefix": "Bearer ",
              "secrets": { "token": "env://PROBE_BEARER_TOKEN" } } } } }
```

## Before seeding

- **Reads are authenticated.** Seeding needs the operator token, the adapter a read-only one.
- **The read-only role does not exist yet** — any token that can read these can also write them.
  Close that before seeding a credential.
- **Check `version` against `schemaUrl`.** Rule 4; the schema cannot compare them.
- **There is no delete.** A correction is a full `PUT`, or `status: "inactive"`.
- `agmarknet`'s request mapping must emit `lat`, `long`, `commodity_id` and a single `date` — the
  older four-code endpoint is not what production calls.
- Three bindings emit responses the domain packs reject. Seeding is unaffected; the mappings are
  not — [usecases.md](usecases.md#conformance).
