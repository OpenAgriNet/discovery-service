# Registry API

Sunbird RC generates the REST surface from the three schemas in [`schemas/`](schemas). `<Entity>`
is `Participant`, `SchemaRegistry` or `ProviderSchema`.

| route | who | what |
|---|---|---|
| `POST /api/v1/<Entity>` | `registryOperator` | create |
| `POST /api/v1/<Entity>/search` | authenticated | look up by an indexed field |
| `GET /api/v1/<Entity>/{osid}` | authenticated | read one |
| `PUT /api/v1/<Entity>/{osid}` | `registryOperator` | replace in full |
| `DELETE /api/v1/<Entity>/{osid}` | — | **disabled** — see [Delete](#delete--disabled) |

Two things to know before you use any of them:

- **The body is wrapped** one level under the entity name. Each schema's top level requires it
  (`required: ["Participant"]`), so the records in [examples.md](examples.md) are write bodies
  exactly as they stand.
- **`osid` is RC's row id**, returned by the create. It is *not* `participantId` and *not*
  `bindingKey`, so an update has to search first.

The *who* column is intent, not enforcement. All three schemas declare
`"roles": ["registryOperator"]`, and RC's `_osConfig.roles` gates the **entity, not the verb** —
so on the pinned build any token that can read these records can also write them. Close that
before seeding a credential ([Known gaps](schemas.md#known-gaps)).

---

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
  "roles": ["provider"],
  "baseUrl": "https://api.agmarknet.gov.in",
  "status": "active",
  "auth": { "scheme": "apiKeyQuery",
            "paramName": "token",
            "secrets": { "token": "env://MANDI_TOKEN" } } } }
```
```json
200 OK
{ "id": "sunbird-rc.registry.create",
  "params": { "status": "SUCCESSFUL" },
  "result": { "Participant": { "osid": "1-8f2c4e7a-3b91-4d0e-9c55-2a1f6b8e0d34" } } }
```

Seed in order: `SchemaRegistry`, then `Participant`, then `ProviderSchema` — a binding's
integrity rules require the other two to exist and be `active`
([schemas.md § Five rules](schemas.md#five-rules-the-schema-cannot-express)).

## Search

```http
POST /api/v1/ProviderSchema/search
Authorization: Bearer <read-token>
```
```json
{ "filters": { "bindingKey": { "eq": "agmarknet|openagrinet:MandiPrice" },
               "status":     { "eq": "active" } } }
```
```json
200 OK
[ { "osid": "1-4c7d5e91-2a08-4f6b-8d13-77e0c9a4b521",
    "bindingKey": "agmarknet|openagrinet:MandiPrice",
    "participantId": "agmarknet", "capabilityCode": "openagrinet:MandiPrice",
    "method": "GET", "path": "/v1/fetch-agmarknet-vistaar-location",
    "requestMapping": "mappings/agmarknet/select.request.jsonata",
    "responseMapping": "mappings/agmarknet/select.response.jsonata",
    "timeoutMs": 20000, "retryMax": 2, "status": "active" } ]
```

You can only filter on an indexed field. Those are:

| entity | unique | also indexed |
|---|---|---|
| `SchemaRegistry` | `capabilityCode` | `status` |
| `Participant` | `participantId` | `status` |
| `ProviderSchema` | `bindingKey` | `participantId`, `capabilityCode`, `status` |

**`search` is not public.** A record may hold an `inline:` credential, so a read of
`Participant` can be a read of live key material. Assume the response carries it: `privateFields`
*should* redact `$.auth.secrets`, and that is unverified on the pinned build
([Known gaps](schemas.md#known-gaps)).

**The unique index is a single field, so a duplicate is a silent overwrite, not an error.**
Writing a second `ProviderSchema` with an existing `bindingKey` replaces the first.

## Update

RC's `PUT` replaces; it is not a merge patch. Search for the `osid`, change the field, send the
whole record back.

```http
PUT /api/v1/Participant/1-8f2c4e7a-3b91-4d0e-9c55-2a1f6b8e0d34
Authorization: Bearer <operator-token>
```
```json
{ "Participant": {
  "participantId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "roles": ["provider"],
  "baseUrl": "https://api.agmarknet.gov.in",
  "status": "inactive",
  "auth": { "scheme": "apiKeyQuery",
            "paramName": "token",
            "secrets": { "token": "env://MANDI_TOKEN_2026" } } } }
```

**Because `PUT` replaces, a field you omit is a field you delete.** `publicKeys` is the dangerous
one: dropping it turns signature verification off rather than failing loudly.

Rotating an `env://` pointer is a registry write. Rotating the *value behind* the pointer is not —
that is an environment change in the adapter. Rotating an `inline:` credential is always a
registry write, which is one of several reasons not to use `inline:`.

## Delete — disabled

**There is no delete.** The route is closed at the gateway; no token carries the right to call
it. Deactivate instead:

```
PUT /api/v1/Participant/{osid}     → the same record with "status": "inactive"
```

Three reasons, in order of what they cost when ignored:

1. **A delete orphans silently.** RC enforces no reference between entities, so removing a
   `Participant` leaves its `ProviderSchema` rows pointing at nothing. They still validate and
   still resolve by `bindingKey`. The call fails at request time with no clue that the cause was
   a registry write weeks earlier.
2. **Published catalogs outlive the record.** Resources already advertised through `/discover`
   carry a `provider.id`; deleting the row that explains that id makes them unresolvable without
   making them disappear.
3. **`inactive` is as complete, and leaves evidence.** Every read filters `status == "active"`,
   so flipping the flag takes a participant out of service just as totally — and leaves the row
   where an operator can see *what* was turned off.

A genuine erasure — an onboarding mistake, or a participant exercising a removal right — is an
operator task against the database with a reason recorded, not an API call anyone holds a token
for.

---

## The runtime does not call these per request

The walkthroughs in [usecases.md](usecases.md) show two `/search` calls per `select` because that
is the clearest way to show which value comes from where. **That is not how it runs.**

All 13 records are a few kilobytes. The adapter loads them at boot and indexes them by
`bindingKey` and `participantId`, so resolving a `select` is two map lookups and the registry
contributes **zero** latency and zero availability risk to the request path. A registry change
takes effect on the next reload, not the next request — which is the tradeoff, and the right one
for records that change on operator action rather than on traffic.

## What is not verified

Honest limits of this page, so nobody builds on the wrong half of it:

| | |
|---|---|
| **Response body shapes** | The requests above are corroborated. Whether RC returns search rows bare or wrapped, and what envelope it puts around them, has **not** been checked against the pinned build. Treat every response body here as an illustrative shape. |
| **`privateFields` redaction** | Whether RC actually strips `$.auth.secrets` from a `/search` response on the pinned build is unchecked. Until it is, assume it does not. |
| **Read-only role** | Does not exist. See the note at the top. |
| **`DELETE` being closed** | Stated as policy. That it is closed *at the gateway* is a deployment fact this page cannot assert about your environment — confirm it. |
| **`schemaUrl` resolvability** | Nothing fetches these URLs to confirm they exist, and nothing checks `version` against the URL's `vN.N` segment. Both belong in the seeding path. |
