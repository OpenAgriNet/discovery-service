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
  "keys": { "keyId": "k1", "use": "sign", "alg": "ed25519",
            "key": "base64:s3Q/53+xYL/BgelYdsKd7DBgYDUFLsXE+GQDLSuPZ4c=",
            "validFrom": "2026-08-01T00:00:00Z", "status": "active" } } }
```
```json
{ "Participant": {
  "participantId": "discovery-network-vistaar.da.gov.in",
  "name": "OpenAgriNet network node",
  "type": "node",
  "status": "active",
  "baseUrl": "https://discovery-network-vistaar.da.gov.in/beckn",
  "role": "NETWORK",
  "keys": { "keyId": "k1", "use": "sign", "alg": "ed25519",
            "key": "base64:q7fEHdFO7wNpYBARwY+qvhGhRzlrlJWRR64NIwQhO2A=",
            "validFrom": "2026-08-01T00:00:00Z", "status": "active" } } }
```
```json
{ "Participant": {
  "participantId": "provider-network-vistaar.da.gov.in",
  "name": "OpenAgriNet provider adapter",
  "type": "node",
  "status": "active",
  "baseUrl": "https://provider-network-vistaar.da.gov.in/beckn",
  "role": "BPP",
  "keys": { "keyId": "k1", "use": "sign", "alg": "ed25519",
            "key": "base64:xq4+2oQ6MgSZdHHBMtNd1TmnPTmzY5UoZlqzf0yn6ZA=",
            "validFrom": "2026-08-01T00:00:00Z",
            "validUntil": "2026-11-01T00:00:00Z", "status": "active" } } }
```

`keys` is one key, not a list, so **a node cannot hold two at once and rotation is a hard
cutover**: replace the object and every verifier must have picked up the new material by the moment
the node starts signing with it. The provider node is the case that shows the cost — its `k1` carries
`validUntil` 1 Nov, and because there is no successor to overlap with, that date is a deadline after
which it cannot sign at all. The sender still names the key in the `Authorization` header, which is
now one name out of one.

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
  "baseUrl": "https://mausamgram.imd.gov.in" } }
```
```json
{ "Participant": {
  "participantId": "imd-city-weather",
  "name": "IMD City Weather",
  "type": "upstream",
  "status": "active",
  "baseUrl": "https://city.imd.gov.in" } }
```
```json
{ "Participant": {
  "participantId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "type": "upstream",
  "status": "active",
  "baseUrl": "https://api.agmarknet.gov.in" } }
```
```json
{ "Participant": {
  "participantId": "hasura-content",
  "name": "Vistaar Knowledge Content (Hasura)",
  "type": "upstream",
  "status": "active",
  "baseUrl": "https://content.internal" } }
```
```json
{ "Participant": {
  "participantId": "oan-vector",
  "name": "OAN Vector Index",
  "type": "upstream",
  "status": "active",
  "baseUrl": "http://3.6.146.174:8882" } }
```

**None of these rows says how to authenticate.** There is no `auth` field: the credential for
calling an upstream belongs to the binding's plugin and is read from the adapter's own environment,
so no record here holds a secret or a pointer to one. What that buys is the strongest property of
these three schemas — a read of a `Participant` can never be a read of live key material. No
`_osConfig.privateFields` entry has to hold for that to be true, which matters, because none of the
three schemas declares one.

What it costs is one guard. `oan-vector` is a bare IP over plain HTTP, and it used to be legal
*because* `scheme: none` said no credential rode on it. The schema can no longer make that
distinction for an upstream, so plaintext is now permitted for all of them and nothing refuses a
credentialled plugin pointed at an `http://` host. Keeping that true is the plugin's job, and
nothing checks it.

`mausamgram` and `imd-city-weather` are both IMD on different hosts, so two records — one
organisation looking like two participants. One `Participant` is one **host**: `mausamgram`'s
`baseUrl` stops at the hostname and `/nwpapi` moved into the binding's `path`, so a second endpoint
on the same host at any depth needs nothing new. A second endpoint on a *different* host is still a
second `Participant`, now because the plugin's credential is chosen per binding and the host it may
reach is the one its own record names.

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
naming it — and a plugin that reads a PostGIS DSN reads it from its own environment, which is now
also where its upstream credential comes from. That is why no row in this file holds a secret at
all. Having the plugin is a seeding prerequisite.

## Before seeding

- **Reads are authenticated.** Seeding needs the operator token, the adapter a read-only one.
- **The read-only role does not exist yet** — any token that can read these can also write them.
  Close it before v1 carries traffic. It is no longer a credential-disclosure risk, because no
  record holds a credential; it is still a write anybody with a read token can make.
- **Check `version` against `schemaUrl`.** The schema cannot compare two fields; `verify/records.py` does.
- **There is no delete.** A correction is a full `PUT`, or `status: "inactive"`.
- `agmarknet`'s request mapping must emit `lat`, `long`, `commodity_id` and a single `date` — the
  older four-code endpoint is not what production calls.
- Three bindings emit responses the domain packs reject. Seeding is unaffected; the mappings are
  not — [usecases.md](usecases.md#conformance).
