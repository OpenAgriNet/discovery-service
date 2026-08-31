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

The three adapters of the [deployment topology](README.md#deployment-topology). A node's
`participantId` is its wire identity, so these read as hostnames and are what `context.bapId` and
`context.bppId` carry. Keys below are demo material.

```json
{ "Participant": {
  "participantId": "seeker-network-vistaar.da.gov.in",
  "name": "Kisan app consumer adapter",
  "type": "node",
  "status": "active",
  "baseUrl": "https://seeker-network-vistaar.da.gov.in/beckn",
  "role": "BAP",
  "keys": [ { "keyId": "k1", "use": "sign", "alg": "ed25519",
              "key": "base64:s3Q/53+xYL/BgelYdsKd7DBgYDUFLsXE+GQDLSuPZ4c=",
              "validFrom": "2026-08-01T00:00:00Z", "status": "active" } ] } }
```
```json
{ "Participant": {
  "participantId": "discovery-network-vistaar.da.gov.in",
  "name": "OpenAgriNet network node",
  "type": "node",
  "status": "active",
  "baseUrl": "https://discovery-network-vistaar.da.gov.in/beckn",
  "role": "NETWORK",
  "keys": [ { "keyId": "k1", "use": "sign", "alg": "ed25519",
              "key": "base64:q7fEHdFO7wNpYBARwY+qvhGhRzlrlJWRR64NIwQhO2A=",
              "validFrom": "2026-08-01T00:00:00Z", "status": "active" } ] } }
```
```json
{ "Participant": {
  "participantId": "provider-network-vistaar.da.gov.in",
  "name": "OpenAgriNet provider adapter",
  "type": "node",
  "status": "active",
  "baseUrl": "https://provider-network-vistaar.da.gov.in/beckn",
  "role": "BPP",
  "keys": [ { "keyId": "k1", "use": "sign", "alg": "ed25519",
              "key": "base64:xq4+2oQ6MgSZdHHBMtNd1TmnPTmzY5UoZlqzf0yn6ZA=",
              "validFrom": "2026-08-01T00:00:00Z",
              "validUntil": "2026-11-01T00:00:00Z", "status": "active" },
            { "keyId": "k2", "use": "sign", "alg": "ed25519",
              "key": "base64:1s/FVTLnowPxttooNqWzGCqMZqUrypASpL4jC9NWUA8=",
              "validFrom": "2026-10-01T00:00:00Z", "status": "active" },
            { "keyId": "e1", "use": "encrypt", "alg": "x25519",
              "key": "base64:SzmOZUXmi7jIzJQItBpX/vQA9O3IvEnzZUkc1J/iK7M=",
              "validFrom": "2026-08-01T00:00:00Z", "status": "active" } ] } }
```

The provider node shows a rotation: `k1` expires 1 Nov, `k2` is valid from 1 Oct, and the October
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
  "type": "upstream",
  "status": "active",
  "baseUrl": "https://mausamgram.imd.gov.in",
  "auth": { "scheme": "basic",
            "secrets": { "username": "env://MAUSAMGRAM_USER",
                         "password": "env://MAUSAMGRAM_X_API_KEY" } } } }
```
```json
{ "Participant": {
  "participantId": "imd-city-weather",
  "name": "IMD City Weather",
  "type": "upstream",
  "status": "active",
  "baseUrl": "https://city.imd.gov.in",
  "auth": { "scheme": "none" } } }
```
```json
{ "Participant": {
  "participantId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "type": "upstream",
  "status": "active",
  "baseUrl": "https://api.agmarknet.gov.in",
  "auth": { "scheme": "apiKeyQuery",
            "paramName": "token",
            "secrets": { "token": "env://MANDI_TOKEN" } } } }
```
```json
{ "Participant": {
  "participantId": "hasura-content",
  "name": "Vistaar Knowledge Content (Hasura)",
  "type": "upstream",
  "status": "active",
  "baseUrl": "https://content.internal",
  "auth": { "scheme": "apiKeyHeader",
            "paramName": "x-hasura-admin-secret",
            "secrets": { "adminSecret": "env://HASURA_GRAPHQL_ADMIN_SECRET" } } } }
```
```json
{ "Participant": {
  "participantId": "oan-vector",
  "name": "OAN Vector Index",
  "type": "upstream",
  "status": "active",
  "baseUrl": "http://3.6.146.174:8882",
  "auth": { "scheme": "none" } } }
```

`oan-vector` is a bare IP over plain HTTP, legal only because `scheme: none` — nothing is leaked.
`mausamgram` and `imd-city-weather` are both IMD on different hosts, so two records — one
organisation looking like two participants. One `Participant` is one **host**: `mausamgram`'s
`baseUrl` stops at the hostname and `/nwpapi` moved into the binding's `path`, so a second endpoint
on the same host at any depth needs nothing new. A second endpoint on a *different* host is a
second `Participant`, because the credential and the https guard both hang off `baseUrl`.

Every credential is an `env://` pointer resolved in the adapter's environment. No v1 record uses
`inline:`.

## Bindings

One row per provider and capability. `actions[]` holds what varies per Beckn action — the URL, the
method, the mapping file, the timeout, and its own `status`.

```json
{ "ProviderSchema": {
  "bindingKey": "mausamgram|openagrinet:WeatherObservation",
  "participantId": "mausamgram",
  "capabilityCode": "openagrinet:WeatherObservation",
  "status": "active",
  "actions": [
    { "action": "select", "method": "GET", "path": "/nwpapi/get-daily",
      "mappings": "mappings/mausamgram/weather-observation.select.yaml",
      "timeoutMs": 30000, "retryMax": 3, "status": "active" } ] } }
```
```json
{ "ProviderSchema": {
  "bindingKey": "imd-city-weather|openagrinet:WeatherObservation",
  "participantId": "imd-city-weather",
  "capabilityCode": "openagrinet:WeatherObservation",
  "status": "active",
  "actions": [
    { "action": "select", "method": "GET", "path": "/api/cityweather_loc.php",
      "mappings": "mappings/imd-city-weather/weather-observation.select.yaml",
      "timeoutMs": 15000, "status": "active" } ] } }
```
```json
{ "ProviderSchema": {
  "bindingKey": "agmarknet|openagrinet:MandiPrice",
  "participantId": "agmarknet",
  "capabilityCode": "openagrinet:MandiPrice",
  "status": "active",
  "actions": [
    { "action": "select", "method": "GET", "path": "/v1/fetch-agmarknet-vistaar-location",
      "mappings": "mappings/agmarknet/mandi-price.select.yaml",
      "timeoutMs": 20000, "retryMax": 2, "status": "active" } ] } }
```
```json
{ "ProviderSchema": {
  "bindingKey": "hasura-content|openagrinet:KnowledgeResource",
  "participantId": "hasura-content",
  "capabilityCode": "openagrinet:KnowledgeResource",
  "status": "active",
  "actions": [
    { "action": "select", "method": "POST", "path": "/v1/graphql",
      "mappings": "mappings/hasura-content/knowledge-resource.select.yaml",
      "timeoutMs": 15000, "retryMax": 0, "status": "active" } ] } }
```
```json
{ "ProviderSchema": {
  "bindingKey": "oan-vector|openagrinet:KnowledgeResource",
  "participantId": "oan-vector",
  "capabilityCode": "openagrinet:KnowledgeResource",
  "status": "active",
  "actions": [
    { "action": "select", "method": "POST", "path": "/indexes/oan-index/search",
      "mappings": "mappings/oan-vector/knowledge-resource.select.yaml",
      "timeoutMs": 15000, "status": "active" } ] } }
```

**Every seeded action is `select`.** `discover` is answered from the published catalog and never
calls an upstream, so it has no binding. A second action on a provider is a second entry in that
provider's array, not a second row — the shape is there because a subscription capability needs
`confirm` on a different URL from `select`, with its own timeout.

`imd-city-weather`'s `path` is `/api/cityweather_loc.php` and not the documented
`/citywx/city_weather_test.php`; it takes `?id=<station code>` and returns an **array** with
`"NIL"` sentinels — [usecases.md](usecases.md#the-other-five).

**All five need a step no field here names.** `mausamgram` a point lifted out of the intent,
`imd-city-weather` the nearest station, `agmarknet` market and commodity codes, both
`KnowledgeResource` bindings their query parameters. That step is the adapter plugin's, selected
by the same `bindingKey` and action that select the mapping, so the registry gains nothing by
naming it — and a plugin that reads a PostGIS DSN reads it from its own environment, which is why
no secret but `auth` appears on these rows. Having the plugin is a seeding prerequisite.

## Forms no seeded record uses

Two probe records, kept so the schema's remaining branches stay exercised by `verify/shape.py`
rather than only claimed.

```json
{ "Participant": {
  "participantId": "probe-multi-secret",
  "name": "Upstream wanting two named credentials",
  "type": "upstream",
  "status": "active",
  "baseUrl": "https://probe.example/v1",
  "auth": { "scheme": "apiKeyHeader",
            "paramNames": { "client": "x-client-id", "key": "x-api-key" },
            "secrets":    { "client": "env://PROBE_CLIENT_ID",
                            "key":    "env://PROBE_API_KEY" } } } }
```
```json
{ "Participant": {
  "participantId": "probe-bearer",
  "name": "Upstream wanting a bearer token",
  "type": "upstream",
  "status": "active",
  "baseUrl": "https://probe.example/v2",
  "auth": { "scheme": "apiKeyHeader",
            "paramName": "Authorization",
            "valuePrefix": "Bearer ",
            "secrets": { "token": "env://PROBE_BEARER_TOKEN" } } } }
```

## Before seeding

- **Reads are authenticated.** Seeding needs the operator token, the adapter a read-only one.
- **The read-only role does not exist yet** — any token that can read these can also write them.
  Close that before seeding a credential.
- **Check `version` against `schemaUrl`.** The schema cannot compare two fields; `verify/records.py` does.
- **There is no delete.** A correction is a full `PUT`, or `status: "inactive"`.
- `agmarknet`'s request mapping must emit `lat`, `long`, `commodity_id` and a single `date` — the
  older four-code endpoint is not what production calls.
- Three bindings emit responses the domain packs reject. Seeding is unaffected; the mappings are
  not — [usecases.md](usecases.md#conformance).
