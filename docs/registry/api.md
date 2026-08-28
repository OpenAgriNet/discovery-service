# Registry API

Sunbird RC generates the REST surface from the three schemas in [`schemas/`](schemas). `<Entity>`
is `Participant`, `SchemaRegistry` or `ProviderSchema`.

| route | who | what |
|---|---|---|
| `POST /api/v1/<Entity>` | `registryOperator` | create |
| `POST /api/v1/<Entity>/search` | authenticated | look up by an indexed field |
| `GET /api/v1/<Entity>/{osid}` | authenticated | read one |
| `PUT /api/v1/<Entity>/{osid}` | `registryOperator` | replace in full |
| `DELETE /api/v1/<Entity>/{osid}` | — | **disabled** — [why](#delete--disabled) |

- **The body is wrapped** one level under the entity name, so the records in
  [examples.md](examples.md) are write bodies exactly as they stand.
- **`osid` is RC's row id**, returned by the create. It is not `participantId` and not
  `bindingKey`, so an update has to search first.
- The *who* column is intent, not enforcement: `_osConfig.roles` gates the **entity, not the
  verb**, so on the pinned build any token that can read these records can also write them. Close
  that before seeding a credential.

## Create

```http
POST /api/v1/Participant
Authorization: Bearer <operator-token>
Content-Type: application/json
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
200 OK
{ "id": "sunbird-rc.registry.create",
  "params": { "status": "SUCCESSFUL" },
  "result": { "Participant": { "osid": "1-8f2c4e7a-3b91-4d0e-9c55-2a1f6b8e0d34" } } }
```

## Search

```http
POST /api/v1/ProviderSchema/search
Authorization: Bearer <read-token>
```
```json
{ "filters": { "bindingKey": { "eq": "agmarknet|openagrinet:MandiPrice" },
               "status":     { "eq": "active" } } }
```

Only indexed fields can be filtered:

| entity | unique | also indexed |
|---|---|---|
| `SchemaRegistry` | `capabilityCode` | `status` |
| `Participant` | `participantId` | `status` |
| `ProviderSchema` | `bindingKey` | `participantId`, `capabilityCode`, `status` |

**`search` is not public.** A record may hold an `inline:` credential, so a read of `Participant`
can be a read of live key material. `privateFields` *should* redact
`$.upstream.auth.secrets`; that is unverified on the pinned build, so assume it does not.

**The unique index is a single field, so a duplicate is a silent overwrite, not an error.**

## Update

`PUT` replaces; it is not a merge patch. Search for the `osid`, change the field, send the whole
record back.

```http
PUT /api/v1/Participant/1-8f2c4e7a-3b91-4d0e-9c55-2a1f6b8e0d34
Authorization: Bearer <operator-token>
```
```json
{ "Participant": {
  "participantId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "status": "inactive",
  "upstream": {
    "baseUrl": "https://api.agmarknet.gov.in",
    "auth": { "scheme": "apiKeyQuery",
              "paramName": "token",
              "secrets": { "token": "env://MANDI_TOKEN_2026" } } } } }
```

**Because `PUT` replaces, a field you omit is a field you delete.** On a node record `keys` is the
dangerous one: dropping it removes every key that node may sign with, silently.

Rotating an `env://` pointer is a registry write. Rotating the *value behind* it is not — that is
an environment change in the adapter.

## Delete — disabled

The route is closed at the gateway; no token carries the right to call it. Deactivate instead —
`PUT` the same record with `"status": "inactive"`. Three reasons, worst first:

1. **A delete orphans silently.** RC enforces no reference between entities, so removing a
   `Participant` leaves its `ProviderSchema` rows resolving by `bindingKey` to nothing. The call
   fails at request time with no clue the cause was a registry write weeks earlier.
2. **Published catalogs outlive the record.** Resources already advertised carry a `provider.id`;
   deleting the row that explains it makes them unresolvable without making them disappear.
3. **`inactive` is as complete and leaves evidence.** Every read filters on `active`.

A genuine erasure is an operator task against the database with a reason recorded.

## The runtime does not call these per request

All sixteen records are a few kilobytes. The adapter loads them at boot and indexes them by
`bindingKey` and `participantId`, so resolving a `select` is two map lookups and the registry
contributes **zero** latency and zero availability risk to the request path. A registry change
takes effect on the next reload — the tradeoff, and the right one for records that change on
operator action rather than on traffic. It is also why `status: "revoked"` on a key is inert
until that reload.

## What is not verified

| | |
|---|---|
| **Response body shapes** | The requests are corroborated. Whether RC returns search rows bare or wrapped is unchecked against the pinned build — treat every response body here as illustrative. |
| **`DELETE` being closed** | Policy. That it is closed *at the gateway* is a deployment fact this page cannot assert about your environment — confirm it. |
| **`schemaUrl` resolvability** | Nothing fetches these URLs, and nothing checks `version` against the URL's `vN.N` segment. Both belong in the seeding path. |
