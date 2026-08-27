# Registry records — what we store

The thirteen records that seed the v1 registry — 3 `Capability`, 5 `Provider`,
5 `ProviderCapability` — in Sunbird RC write form.

Schema: [registry.md §3](registry.md#3-the-schemas) ·
[`schemas/`](schemas/). Write endpoints: [§5](registry.md#5-apis).

> `schemaUrl` points at [`OpenAgriNet/network-specs`](https://github.com/OpenAgriNet/network-specs),
> pinned to a **full commit sha** — `3e593b3` here. The schema's two negative lookaheads
> reject a branch ref on purpose: pinned to `main`, the contract you validated against last
> week is not the one you validate against today and nothing tells you it moved. Bumping
> the sha is a reviewed change to these three records.

### Capabilities

```json
{ "Capability": {
  "capabilityCode": "openagrinet:WeatherObservation",
  "name": "Weather Observation and Forecast",
  "baseTypes": ["openagrinet:AgricultureResource"],
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/3e593b3627acae6f416382e6d4bd58f641f309e8/schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active"
} }
```

```json
{ "Capability": {
  "capabilityCode": "openagrinet:MandiPrice",
  "name": "Mandi Price",
  "baseTypes": ["openagrinet:AgricultureResource"],
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/3e593b3627acae6f416382e6d4bd58f641f309e8/schema/MandiPrice/v0.1/attributes.yaml",
  "status": "active"
} }
```

```json
{ "Capability": {
  "capabilityCode": "openagrinet:KnowledgeResource",
  "name": "Knowledge Resource",
  "baseTypes": ["openagrinet:AgricultureResource"],
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/3e593b3627acae6f416382e6d4bd58f641f309e8/schema/KnowledgeResource/v0.1/attributes.yaml",
  "status": "active"
} }
```

One `Capability` serves both Advisory categories. Schemes and Crop & Pest are the same
outcome type; they are told apart on the published resource by `subjectCategories`
(`Scheme` vs `Crop`), not by the registry. See [use case execution](usecases.md).

### Providers

**No v1 provider carries a `signing` block, and none carries an `inline:` secret.** All
five upstreams are plain HTTP APIs that sign nothing, and every credential below is an
`env://` pointer resolved in the adapter's own environment. Both are in the schema
([§3.1](registry.md#31-provider)) for providers that need them.

A record that did need them — shown once, seeded nowhere:

```json
{ "Provider": {
  "providerId": "example-signed",
  "name": "A provider that signs, and whose key we cannot hold in the environment",
  "baseUrl": "https://api.example.gov.in",
  "status": "active",
  "auth": { "scheme": "apiKeyHeader",
            "paramName": "x-api-key",
            "secrets": { "apiKey": "inline:a7f3c9d2e1b84f60a1c5" } },
  "signing": { "keyId": "example-signed|key-1|ed25519",
               "publicKey": "MCowBQYDK2VwAyEAGb9ECWmEzf6FQbrBZ9w7lshQhqowtrbLDFw4rXAxZuE=",
               "algorithm": "ed25519",
               "validFrom": "2026-08-01T00:00:00Z",
               "validUntil": "2027-08-01T00:00:00Z" }
} }
```

The `inline:` prefix is not decoration: a bare pasted key fails the pattern and is rejected
at write time, so storing a credential has to be deliberate — and because the prefix is
literal, *which providers hold a real key* is one query over the table.

```json
{ "Provider": {
  "providerId": "mausamgram",
  "name": "IMD Mausamgram NWP",
  "baseUrl": "https://mausamgram.imd.gov.in/nwpapi",
  "status": "active",
  "auth": { "scheme": "basic",
            "secrets": { "username": "env://MAUSAMGRAM_USER",
                         "password": "env://MAUSAMGRAM_X_API_KEY" } }
} }
```

```json
{ "Provider": {
  "providerId": "imd-city-weather",
  "name": "IMD City Weather",
  "baseUrl": "https://city.imd.gov.in",
  "status": "active",
  "auth": { "scheme": "none" }
} }
```

```json
{ "Provider": {
  "providerId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "baseUrl": "https://api.agmarknet.gov.in",
  "status": "active",
  "auth": { "scheme": "apiKeyQuery",
            "paramName": "token",
            "secrets": { "token": "env://MANDI_TOKEN" } }
} }
```

```json
{ "Provider": {
  "providerId": "hasura-content",
  "name": "Vistaar Knowledge Content (Hasura)",
  "baseUrl": "https://content.internal",
  "status": "active",
  "auth": { "scheme": "apiKeyHeader",
            "paramName": "x-hasura-admin-secret",
            "secrets": { "adminSecret": "env://HASURA_GRAPHQL_ADMIN_SECRET" } }
} }
```

```json
{ "Provider": {
  "providerId": "oan-vector",
  "name": "OAN Vector Index",
  "baseUrl": "http://3.6.146.174:8882",
  "status": "active",
  "auth": { "scheme": "none" }
} }
```

> `oan-vector` is a bare IP over plain HTTP. It is legal only because `scheme: none` —
> nothing is leaked by it. Moving it behind TLS with a real hostname is onboarding work,
> not a schema change, and should happen before v1 carries real traffic.

### Bindings

```json
{ "ProviderCapability": {
  "bindingKey": "mausamgram|openagrinet:WeatherObservation",
  "providerId": "mausamgram",
  "capabilityCode": "openagrinet:WeatherObservation",
  "method": "GET",
  "path": "/get-daily",
  "enricher": { "name": "pointFromIntent" },
  "requestMapping":  "mappings/mausamgram/select.request.jsonata",
  "responseMapping": "mappings/mausamgram/select.response.jsonata",
  "timeoutMs": 30000,
  "retryMax": 3,
  "status": "active"
} }
```

```json
{ "ProviderCapability": {
  "bindingKey": "imd-city-weather|openagrinet:WeatherObservation",
  "providerId": "imd-city-weather",
  "capabilityCode": "openagrinet:WeatherObservation",
  "method": "GET",
  "path": "/citywx/city_weather_test.php",
  "enricher": { "name": "nearestStation",
                "config": { "maxDistanceKm": 50, "maxStationAttempts": 5 },
                "secrets": { "dsn": "env://IMD_DB_DSN" } },
  "requestMapping":  "mappings/imd-city-weather/select.request.jsonata",
  "responseMapping": "mappings/imd-city-weather/select.response.jsonata",
  "timeoutMs": 15000,
  "status": "active"
} }
```

```json
{ "ProviderCapability": {
  "bindingKey": "agmarknet|openagrinet:MandiPrice",
  "providerId": "agmarknet",
  "capabilityCode": "openagrinet:MandiPrice",
  "method": "GET",
  "path": "/v1/fetch-agmarknet-vistaar-location",
  "enricher": { "name": "marketAndCommodityCodes" },
  "requestMapping":  "mappings/agmarknet/select.request.jsonata",
  "responseMapping": "mappings/agmarknet/select.response.jsonata",
  "timeoutMs": 20000,
  "retryMax": 2,
  "status": "active"
} }
```

```json
{ "ProviderCapability": {
  "bindingKey": "hasura-content|openagrinet:KnowledgeResource",
  "providerId": "hasura-content",
  "capabilityCode": "openagrinet:KnowledgeResource",
  "method": "POST",
  "path": "/v1/graphql",
  "enricher": { "name": "knowledgeQueryParams" },
  "requestMapping":  "mappings/hasura-content/select.request.jsonata",
  "responseMapping": "mappings/hasura-content/select.response.jsonata",
  "timeoutMs": 15000,
  "retryMax": 0,
  "status": "active"
} }
```

```json
{ "ProviderCapability": {
  "bindingKey": "oan-vector|openagrinet:KnowledgeResource",
  "providerId": "oan-vector",
  "capabilityCode": "openagrinet:KnowledgeResource",
  "method": "POST",
  "path": "/indexes/oan-index/search",
  "enricher": { "name": "knowledgeQueryParams" },
  "requestMapping":  "mappings/oan-vector/select.request.jsonata",
  "responseMapping": "mappings/oan-vector/select.response.jsonata",
  "timeoutMs": 15000,
  "status": "active"
} }
```

### Before seeding

- **Reads are authenticated.** Seeding needs the Operator token; the adapter needs a
  read-only one. `/search` is not open — see
  [registry.md §4](registry.md#5-apis).
- Seed in order: `Capability`, then `Provider`, then `ProviderCapability`. The binding's
  two integrity rules require the other two to exist and be `active`.
- `agmarknet`'s `select.request.jsonata` must emit `lat`, `long`, `commodity_id` and a
  single `date`. The location endpoint above takes those; the older four-code endpoint the
  mapping was written for is not what production calls.
- Both `KnowledgeResource` bindings share `enricher.name: knowledgeQueryParams`. Their request
  mappings do not — one shapes a Hasura GraphQL `variables` block, the other a vector
  search body.

