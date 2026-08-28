# Registry records — what we store

The thirteen records that seed the v1 registry — 3 `SchemaRegistry`, 5 `Participant`,
5 `ProviderSchema` — in Sunbird RC write form.

Schema: [registry.md §3](registry.md#3-the-schemas) ·
[`schemas/`](schemas/). Write endpoints: [§5](registry.md#5-apis).

> `schemaUrl` points at [`OpenAgriNet/network-specs`](https://github.com/OpenAgriNet/network-specs),
> pinned to a **version directory** — `v0.1` here — and not to a commit. The version segment
> is what makes the contract stable: a breaking change is published as `v0.2`, so `v0.1` on
> `main` means the same document next week. Pinning a sha instead would make these three
> records a mirror of the specs repo, rewritten on every push there.

### Schemas

```json
{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:WeatherObservation",
  "name": "Weather Observation and Forecast",
  "version": "v0.1",
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active"
} }
```

```json
{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:MandiPrice",
  "name": "Mandi Price",
  "version": "v0.1",
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/MandiPrice/v0.1/attributes.yaml",
  "status": "active"
} }
```

```json
{ "SchemaRegistry": {
  "capabilityCode": "openagrinet:KnowledgeResource",
  "name": "Knowledge Resource",
  "version": "v0.1",
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/main/schema/KnowledgeResource/v0.1/attributes.yaml",
  "status": "active"
} }
```

One `SchemaRegistry` record serves both Advisory categories. Schemes and Crop & Pest are the
same outcome type; they are told apart on the published resource by `subjectCategories`
(`Scheme` vs `Crop`), not by the registry. See [use case execution](usecases.md).

### Participants

All five are `roles: ["provider"]` — each has declared a capability, none consumes one. A
consumer-side participant is the same record with `roles: ["consumer"]` and, typically, no
`auth`: there is nothing of theirs for us to call.

**No v1 participant carries a `publicKeys` entry.** All five are upstream data APIs that our
own adapter calls directly, so nothing of theirs is signed and there is no signature to
verify. Under the distributed topology each runs its own adapter and does sign — at which
point the field is mandatory network policy and the seeding path has to enforce it. See
[registry.md §3.4](registry.md#34-five-rules-the-schema-cannot-express).

**No v1 participant carries an `inline:` secret.** Every credential below is an `env://`
pointer resolved in the adapter's own environment. The `inline:` form is in the schema
([§3.1](registry.md#31-participant)) for operators who cannot set that environment, and it
costs three things: `/search` must be authenticated, the database holds live key material,
and rotation becomes a registry write.

The prefix is not decoration: a bare pasted key fails the pattern and is rejected at write
time, so storing a credential has to be deliberate — and because the prefix is literal,
*which participants hold a real key* is one query over the table.

```json
{ "Participant": {
  "participantId": "mausamgram",
  "name": "IMD Mausamgram NWP",
  "roles": ["provider"],
  "baseUrl": "https://mausamgram.imd.gov.in/nwpapi",
  "status": "active",
  "auth": { "scheme": "basic",
            "secrets": { "username": "env://MAUSAMGRAM_USER",
                         "password": "env://MAUSAMGRAM_X_API_KEY" } }
} }
```

```json
{ "Participant": {
  "participantId": "imd-city-weather",
  "name": "IMD City Weather",
  "roles": ["provider"],
  "baseUrl": "https://city.imd.gov.in",
  "status": "active",
  "auth": { "scheme": "none" }
} }
```

```json
{ "Participant": {
  "participantId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "roles": ["provider"],
  "baseUrl": "https://api.agmarknet.gov.in",
  "status": "active",
  "auth": { "scheme": "apiKeyQuery",
            "paramName": "token",
            "secrets": { "token": "env://MANDI_TOKEN" } }
} }
```

```json
{ "Participant": {
  "participantId": "hasura-content",
  "name": "Vistaar Knowledge Content (Hasura)",
  "roles": ["provider"],
  "baseUrl": "https://content.internal",
  "status": "active",
  "auth": { "scheme": "apiKeyHeader",
            "paramName": "x-hasura-admin-secret",
            "secrets": { "adminSecret": "env://HASURA_GRAPHQL_ADMIN_SECRET" } }
} }
```

```json
{ "Participant": {
  "participantId": "oan-vector",
  "name": "OAN Vector Index",
  "roles": ["provider"],
  "baseUrl": "http://3.6.146.174:8882",
  "status": "active",
  "auth": { "scheme": "none" }
} }
```

> `oan-vector` is a bare IP over plain HTTP. It is legal only because `scheme: none` —
> nothing is leaked by it. Moving it behind TLS with a real hostname is onboarding work,
> not a schema change, and should happen before v1 carries real traffic.

> `mausamgram` and `imd-city-weather` are both IMD, on different hosts, so they are two
> records. That is the open path-or-subdomain question in
> [registry.md Known gaps](registry.md#known-gaps-for-v1), visible here as two participants
> where the network has one organisation.

### Bindings

```json
{ "ProviderSchema": {
  "bindingKey": "mausamgram|openagrinet:WeatherObservation",
  "participantId": "mausamgram",
  "capabilityCode": "openagrinet:WeatherObservation",
  "method": "GET",
  "path": "/get-daily",
  "requestMapping":  "mappings/mausamgram/select.request.jsonata",
  "responseMapping": "mappings/mausamgram/select.response.jsonata",
  "timeoutMs": 30000,
  "retryMax": 3,
  "status": "active"
} }
```

```json
{ "ProviderSchema": {
  "bindingKey": "imd-city-weather|openagrinet:WeatherObservation",
  "participantId": "imd-city-weather",
  "capabilityCode": "openagrinet:WeatherObservation",
  "method": "GET",
  "path": "/citywx/city_weather_test.php",
  "requestMapping":  "mappings/imd-city-weather/select.request.jsonata",
  "responseMapping": "mappings/imd-city-weather/select.response.jsonata",
  "timeoutMs": 15000,
  "status": "active"
} }
```

```json
{ "ProviderSchema": {
  "bindingKey": "agmarknet|openagrinet:MandiPrice",
  "participantId": "agmarknet",
  "capabilityCode": "openagrinet:MandiPrice",
  "method": "GET",
  "path": "/v1/fetch-agmarknet-vistaar-location",
  "requestMapping":  "mappings/agmarknet/select.request.jsonata",
  "responseMapping": "mappings/agmarknet/select.response.jsonata",
  "timeoutMs": 20000,
  "retryMax": 2,
  "status": "active"
} }
```

```json
{ "ProviderSchema": {
  "bindingKey": "hasura-content|openagrinet:KnowledgeResource",
  "participantId": "hasura-content",
  "capabilityCode": "openagrinet:KnowledgeResource",
  "method": "POST",
  "path": "/v1/graphql",
  "requestMapping":  "mappings/hasura-content/select.request.jsonata",
  "responseMapping": "mappings/hasura-content/select.response.jsonata",
  "timeoutMs": 15000,
  "retryMax": 0,
  "status": "active"
} }
```

```json
{ "ProviderSchema": {
  "bindingKey": "oan-vector|openagrinet:KnowledgeResource",
  "participantId": "oan-vector",
  "capabilityCode": "openagrinet:KnowledgeResource",
  "method": "POST",
  "path": "/indexes/oan-index/search",
  "requestMapping":  "mappings/oan-vector/select.request.jsonata",
  "responseMapping": "mappings/oan-vector/select.response.jsonata",
  "timeoutMs": 15000,
  "status": "active"
} }
```

**Four of the five need a step no field here names.** `mausamgram` needs a point derived from
the intent; `imd-city-weather` needs the nearest station, from a table the adapter owns;
`agmarknet` needs market and commodity codes in its own namespace; both `KnowledgeResource`
bindings need query parameters built. That step used to be a named `enricher` on the binding
and is now adapter-internal, keyed off `participantId` — see
[registry.md §3.3](registry.md#33-providerschema). **It is a seeding prerequisite, not an
optional extra:** a binding whose adapter has no such step returns nothing useful.

### Before seeding

- **Reads are authenticated.** Seeding needs the Operator token; the adapter needs a
  read-only one. `/search` is not open — see
  [registry.md §5](registry.md#5-apis).
- Seed in order: `SchemaRegistry`, then `Participant`, then `ProviderSchema`. The binding's
  two integrity rules require the other two to exist and be `active`.
- **Check `version` against `schemaUrl` before writing.** Rule 4 in
  [§3.4](registry.md#34-five-rules-the-schema-cannot-express) — the schema cannot compare
  them, so a record advertising `v0.1` while resolving `v0.2` validates.
- `agmarknet`'s `select.request.jsonata` must emit `lat`, `long`, `commodity_id` and a
  single `date`. The location endpoint above takes those; the older four-code endpoint the
  mapping was written for is not what production calls.
- Both `KnowledgeResource` bindings need the same query-parameter step but *not* the same
  request mapping — one shapes a Hasura GraphQL `variables` block, the other a vector search
  body.
- The **read-only role does not exist yet.** RC's `_osConfig.roles` gates the entity, not the
  verb, so on the pinned build any token that can read these records can also write them.
  Close that before seeding a credential of any kind.
- **There is no delete.** Correcting a mistake after seeding is a `PUT` of the whole record,
  or `status: "inactive"` — [registry.md §5](registry.md#delete--disabled).
- Three of the five bindings emit responses the OAN domain packs reject — both
  `KnowledgeResource` ones and `agmarknet`. Seeding is unaffected; the response mappings are
  not. Each violation is in [dpg-fit.md](dpg-fit.md).
