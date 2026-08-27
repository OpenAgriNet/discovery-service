# Registry schema

Three entities, defined field by field, then worked end to end. Read this once; the
[use cases](usecases/README.md) are lookup material.

*[Overview](01-overview.md) · [Adding a provider](03-adding-a-provider.md) · [Use cases](usecases/README.md) · [docs home](README.md)*

---

## What it stores

Three entities. Sunbird RC generates storage and REST from JSON Schema, and there are no
cross-entity joins, so normalising further buys nothing and costs a round trip per hop.

| Entity | Answers | Records |
|---|---|---|
| **Provider** | who they are, and how we authenticate **to them** | 6 seeded |
| **Capability** | network vocabulary: what the outcome is, which schema pack describes it | 3 seeded |
| **ProviderCapability** | **the call plan** — method, path, and both mappings | 4 seeded, tens at scale |

**Secrets are never in here.** The registry stores `env://MAUSAMGRAM_X_API_KEY` — a
pointer. The value lives in the **process environment of the provider-side adapter** and
nowhere else; there is no vault in this stack. This matters because the registry is a
plain Postgres database and anyone with a read connection sees every row. The pointer
form is enforced by schema at write time, so a pasted key cannot reach the database in
the first place.

What it deliberately does **not** hold: transaction state (flows are synchronous),
published catalogs (nothing is pre-published), result resource ids (unbounded, never
looked up by key), value maps like `ONION → 23` (provider-specific and code-shaped — that
belongs in an enricher), and Beckn subscriber keys (ONIX's own registry plugin owns
those).

## Schema

The schema files are `registry/schemas/*.json`, the seeded records
`registry/samples/*.json`. Both are validated on every run of the
[conformance suite](reference/conformance.md).

**The lookup key.** Three things identify a binding — which provider, which capability,
and which Beckn action:

```
bindingKey = "mausamgram|openagrinet:WeatherObservation|select"
```

All three come straight off the request body, so the adapter builds the key from the
request and does one exact-match lookup. No join, and no dependence on composite unique
indexes, which vary across Sunbird RC versions.

*Why the action is in the key.* PMFBY answers `discover`, `init` and `status` against
one provider+capability pair, and PM-Kisan answers `init` and `status`
([Use cases](usecases/README.md)). Under a two-part key those rows share a `bindingKey` —
and since `bindingKey` is the `uniqueIndexFields` entry, the collision is **silent**: the
second write overwrites the first. JSON Schema cannot detect a duplicate *across* records,
only forbid the shape that allows one. So the suffix is mandatory on every binding,
including the ones that answer exactly one action today.

**`@type` on the wire is a single string, and it is the same string on both calls.**
network-specs does not constrain `@type` at all: it appears only under `x-jsonld:` as pack
metadata, never in `properties` and never in `required`. Against the packs an array, a
bare string, an unrecognised string and an absent field all validate equally. **Beckn core
is the only schema that constrains it**, as `type: string`. So the array form has no
upstream requirement and one upstream prohibition, and BV emits the specific type alone.

What tells the two calls apart is `informationMode`, not the type:

```json
① discover  { "@type": "openagrinet:WeatherObservation", "informationMode": "OnDemand" }
② select    { "@type": "openagrinet:WeatherObservation", "informationMode": "Direct"   }
```

`OnDemand` says the provider *can* supply this and a call is needed; `Direct` says the
resource contains the answer. One type spanning both is why `capabilityCode` is a straight
copy of the outcome type rather than a lookup — and why there is no separate
`outcomeType` field.

Some network-specs *examples* show a two-element array instead, whose extra entry is
`openagrinet:AgricultureCapability` — the shared base type. At `c56ee68` that is 5 of the
29 `@type` occurrences across the repo, in 4 files, all of them capability advertisements
inside catalogs. It is an example convention rather than a contract: it identifies
nothing, and it fails Beckn core validation (issue 1 of
[Open issues](reference/open-issues.md)). `weather-provider-catalog.json` even carries
both forms in adjacent resources. BV does not emit the array. On the way *in*, the adapter
still tolerates either form, since a BAP may copy an example.

**The rule is one line: `capabilityCode` is the specific type.** If `@type` is a bare
string, that string is it; if a BAP copied an example and sent an array, take the entry
that is not the base. The schema refuses to
store the base type as a `capabilityCode` or inside a `bindingKey`, so getting it wrong
fails at write time rather than silently resolving to no provider at runtime. The base
type lives on `Capability.baseTypes[]` instead, so a broad request can still fan out
to it.

**The three definitions.** Sunbird RC generates storage and REST from these files, so the
JSON Schema *is* the table definition. All three share one envelope — the record nested a
level under its own name, plus the `_osConfig` block RC reads for indexing and roles:

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "title": "Provider",
  "required": ["Provider"],
  "properties": { "Provider": { "$ref": "#/definitions/Provider" } },
  "definitions": { "Provider": { … }, "Auth": { … }, "Login": { … } },
  "_osConfig": {
    "uniqueIndexFields": ["providerId"],
    "indexFields": ["status"],
    "roles": ["registryAdmin"],
    "systemFields": ["osCreatedAt", "osUpdatedAt", "osCreatedBy", "osUpdatedBy"]
  }
}
```

Every record definition sets `additionalProperties: false`. An unknown field is a rejected
write, not a column that quietly appears.

### `Provider`

`uniqueIndexFields: [providerId]` · `indexFields: [status]`

| field | type | constraint | req |
|---|---|---|---|
| `providerId` | string | `^[a-z0-9][a-z0-9._:-]{2,63}$` — Beckn `provider.id` / `bppId` | ✓ |
| `name` | string | `minLength: 1` | ✓ |
| `baseUrl` | string | `^https?://[^/].*[^/]$` — no trailing slash; a common path prefix is allowed (`mausamgram` is `…/nwpapi`), per-call paths are not. **`https` is required whenever the record carries a credential** — see below | ✓ |
| `status` | string | `active` \| `inactive` | ✓ |
| `auth` | object | → `Auth` | ✓ |
| `authProfiles` | object | `minProperties: 1`; keys `^[a-z][a-zA-Z0-9]*$`, values → `Auth` | |

Paths are not here — they belong to the binding, because one provider serves several.

**`Auth`** — how the adapter authenticates *to* the upstream. Not Beckn signing; ONIX
already does that.

| field | type | constraint | req |
|---|---|---|---|
| `scheme` | string | `none` \| `apiKeyQuery` \| `apiKeyHeader` \| `basic` \| `bearer` \| `loginToken` \| `encryptedEnvelope` | ✓ |
| `paramName` | string | `minLength: 1` — query-param name, or header name | |
| `secrets` | object | `minProperties: 1`; **every value** `^env://[A-Z][A-Z0-9_]*$` | |
| `extraHeaders` | object | `minProperties: 1`; every value `^env://[A-Z][A-Z0-9_]*$` | |
| `envelope` | object | `{ algorithm: aes-128-cbc \| aes-256-cbc \| aes-256-gcm }` | |
| `login` | object | → `Login` | |

Which of those are mandatory depends on the scheme, expressed as an `allOf` of `if`/`then`
rather than left to prose:

| `scheme` | then `required` |
|---|---|
| `apiKeyQuery` `apiKeyHeader` `bearer` | `paramName`, `secrets` |
| `basic` | `secrets` |
| `loginToken` | `paramName`, `secrets`, `login` |
| `encryptedEnvelope` | `secrets`, `envelope` |
| `none` | **must not** carry `secrets` |

```json
{ "if":   { "properties": { "scheme": { "const": "loginToken" } },
            "required": ["scheme"] },
  "then": { "required": ["paramName", "secrets", "login"] } }
```

That last row is the one worth having: `scheme: "none"` with a populated `secrets` block is
a credential nobody sends anywhere, sitting in a readable table.

**A credential implies TLS.** Read the table above in the other direction: every scheme
except `none` *requires* `secrets`, so `scheme != "none"` and "this record holds a
credential" are the same statement. `baseUrl` allows `http://`, and nothing related the
two — so an `apiKeyHeader` binding pointed at a plaintext base URL was a well-formed
record that put a live secret on the wire in clear, and no validation, no conformance
control and no review checklist would have said a word. One clause closes it:

```json
{ "if":   { "properties": { "auth": { "properties": { "scheme": { "not": { "const": "none" } } },
                                      "required": ["scheme"] } },
            "required": ["auth"] },
  "then": { "properties": { "baseUrl": { "pattern": "^https://[^/].*[^/]$" } } } }
```

A second clause requires `https` whenever `authProfiles` is present at all. That is
deliberately blunter than the first — it does not inspect each profile's scheme — because
a profile exists precisely to authenticate *differently* from `Provider.auth`, and a
profile set in which every entry is `none` has nothing to express. Over-strict in a case
that should not arise beats a per-profile conditional that draft-07 states awkwardly and
a reader checks wrongly.

`http://` stays legal for `scheme: "none"`, and one seeded provider needs it:
[`oan-vector`](usecases/README.md#oan-vector--knowledge-advisory) is a bare IP over plain
HTTP with no credential. The rule does not pretend that is good — it is still an internal
service reachable by anything that can route to it — it says only that no *secret* is
being leaked by it, which is the part a schema can decide. Moving it behind TLS is
onboarding work, not a schema change.

Two negative controls belong in [Conformance](reference/conformance.md) —
`apiKeyHeader` on a plaintext base URL, and an `authProfiles` block on one, both
rejected. **Neither exists yet**, because this clause was written here rather than
upstream and the suite lives with the schema files. Until they are added, the rule holds
only because someone remembered it, which is the condition it was written to end.

**`Login`** — the only pre-call this registry models: exchange stored secrets for a
short-lived token.

| field | type | constraint | req |
|---|---|---|---|
| `path` | string | `^/` | ✓ |
| `tokenPath` | string | `^[A-Za-z_][A-Za-z0-9_.]*$` — dotted path into the response, e.g. `data.token` | ✓ |
| `ttlSeconds` | integer | 30 – 86400 | ✓ |
| `method` | string | `GET` \| `POST`, default `POST` | |
| `bodyMapping` | string | JSONata evaluated against the resolved secrets map | |

### `Capability`

`uniqueIndexFields: [capabilityCode]` · `indexFields: [status]`

| field | type | constraint | req |
|---|---|---|---|
| `capabilityCode` | string | `^openagrinet:[A-Z][A-Za-z0-9]*$`, and `not` one of `AgricultureCapability` / `AgricultureResource` | ✓ |
| `name` | string | `minLength: 1` | ✓ |
| `schemaUrl` | string | `^https://(?!.*/refs/heads/)(?!.*/(main\|master\|develop)/).+/attributes\.yaml$` | ✓ |
| `status` | string | `active` \| `deprecated` | ✓ |
| `baseTypes` | array\<string\> | items `^openagrinet:`, `uniqueItems`, default `[]` | |

The `schemaUrl` pattern carries the whole rule: it must end at a pack's `attributes.yaml`
— the contract that validates, not `profile.json`, which holds indexing and privacy hints
only — and the two negative lookaheads reject a branch ref. A capability pinned to `main`
means the contract you validated against last week is not the one you validate against
today. The pattern does **not** check that the URL resolves (question 7 of
[Open issues](reference/open-issues.md)); it is a public raw URL, fetchable by anything
with network access.

**There is no `schemaSha`.** An earlier draft carried the commit hash in its own required
field, which made it a verbatim copy of a segment already in `schemaUrl` — two places to
change, and nothing forcing them to agree. Re-pinning a capability is now one edit. The
pattern keeps the guarantee that mattered: the URL cannot name a branch.

The `not` on `capabilityCode` is what keeps the vocabulary honest: `capabilityCode` **is**
the outcome type, so the shared base field set — which identifies nothing — cannot be
stored as one. It lives on `baseTypes[]` instead, where a broad request can still fan out
to it.

### `ProviderCapability`

`uniqueIndexFields: [bindingKey]` · `indexFields: [providerId, capabilityCode, status]`

| field | type | constraint | req |
|---|---|---|---|
| `bindingKey` | string | three-part key — pattern and `not` given verbatim below | ✓ |
| `providerId` | string | `^[a-z0-9][a-z0-9._:-]{2,63}$` | ✓ |
| `capabilityCode` | string | same pattern and `not` as on `Capability` | ✓ |
| `action` | string | `discover` `select` `init` `confirm` `status` `track` `update` `cancel` `rate` `support`, default `select`. **Dropped in OAN v1** — see below | ✓ |
| `responseMapping` | string | `^mappings/(?!.*\.\.)[a-z0-9][a-z0-9._/-]*\.jsonata$` | ✓ |
| `status` | string | `active` \| `inactive` | ✓ |
| `method` | string | `GET` \| `POST` | single |
| `path` | string | `^/` — appended to `Provider.baseUrl` | single |
| `requestMapping` | string | same `mappings/` pattern | single |
| `steps` | array | 2 – 6 → `Step` | multi |
| `enricher` | string \| object | `oneOf`: `^[a-z][a-zA-Z0-9]*$`, or `{ name, config, secrets }` | |
| `timeoutMs` | integer | 1000 – 120000, default 15000 | |
| `retryMax` | integer | 0 – 5, default 0 | |
| `sessionGate` | object | `{ scope }` | |
| `sessionGrant` | object | `{ scope, ttlSeconds }`, TTL 60 – 3600 | |

`bindingKey` is the one constraint too wide for a table cell. Verbatim from the schema:

```
"pattern": "^[a-z0-9][a-z0-9._:-]{2,63}\\|openagrinet:[A-Z][A-Za-z0-9]*\\|(discover|select|init|confirm|status|track|update|cancel|rate|support)$"

"not": { "pattern": "\\|openagrinet:Agriculture(Capability|Resource)(\\||$)" }
```

The `not` clause is the same guard as on `capabilityCode`, applied to the middle segment:
the shared base type cannot appear in a key either.


**`Step`** — `additionalProperties: false`, so a field the executor does not understand
cannot be smuggled in:

| field | type | constraint | req |
|---|---|---|---|
| `id` | string | `^[a-z][a-zA-Z0-9]*$` — later steps read this response as `steps.<id>` | ✓ |
| `method` | string | `GET` \| `POST` | ✓ |
| `path` | string | `^/` | ✓ |
| `requestMapping` | string | same `mappings/` pattern | ✓ |
| `authProfile` | string | names a key in `Provider.authProfiles`; omit to use `Provider.auth` | |
| `timeoutMs` `retryMax` | integer | as above, per step | |
| `sessionGrant` | object | `$ref` to the binding-level definition — one shape, two places | |

The `single` / `multi` column above is not documentation, it is a `oneOf`. A record is one
shape or the other, never half of each:

```json
"oneOf": [
  { "title": "single call",
    "required": ["method", "path", "requestMapping"],
    "not": { "required": ["steps"] } },
  { "title": "multi-step call",
    "required": ["steps"],
    "allOf": [ { "not": { "required": ["method"] } },
               { "not": { "required": ["path"] } },
               { "not": { "required": ["requestMapping"] } },
               { "not": { "required": ["sessionGrant"] } } ] }
]
```

The fourth clause is the load-bearing one — it is what pushes `sessionGrant` down onto the
step that earns it, and the reason is worked through below.

> **OAN v1 does not store `action`.** It is not in `indexFields`, so it answered no query;
> the adapter builds the key from the request and looks it up exactly, never reading the
> column back; and its enum was already enforced by the key pattern's own alternation. The
> key keeps its third segment — that is the discriminator between `…|init` and `…|status`
> — but the column is gone. See [OpenAgriNet registry — v1](oan-v1.md). BV's own records
> below are unchanged and still carry it.

**Two integrity rules no JSON Schema can express.** `bindingKey` must agree with its own
`providerId`, `capabilityCode` and `action`, and both ids must resolve to live records.
The three-part key deliberately carries what the fields carry, so the two can disagree.
Nothing in draft-07 relates one property to another this way, and RC will not do it
either — so it runs in the onboarding path and in the conformance suite, not here.

The `enricher`, `steps[]` and session fields are worked through below.

**One capability, end to end.** The three records behind the weather advisory traced on
the [Mausamgram page](usecases/mausamgram.md), and the two fields that join them.

`Capability` — network vocabulary, provider-independent. Nothing here names a provider:

```json
{
  "capabilityCode": "openagrinet:WeatherObservation",
  "name": "Weather Observation and Forecast",
  "baseTypes": ["openagrinet:AgricultureResource"],
  "schemaUrl": ".../network-specs/c56ee68.../schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active"
}
```

`Provider` — who they are, and how we authenticate to them. No capability, no path:

```json
{
  "providerId": "mausamgram",
  "name": "IMD Mausamgram NWP",
  "baseUrl": "https://mausamgram.imd.gov.in/nwpapi",
  "status": "active",
  "auth": {
    "scheme": "basic",
    "secrets": { "username": "env://MAUSAMGRAM_USER",
                 "password": "env://MAUSAMGRAM_X_API_KEY" }
  }
}
```

`ProviderCapability` — the call plan, and the only record that names both:

```json
{
  "bindingKey": "mausamgram|openagrinet:WeatherObservation|select",
  "providerId": "mausamgram",
  "capabilityCode": "openagrinet:WeatherObservation",
  "action": "select",
  "method": "GET",
  "path": "/get-daily",
  "enricher": "pointFromIntent",
  "requestMapping":  "mappings/mausamgram/select.request.jsonata",
  "responseMapping": "mappings/mausamgram/select.response.jsonata",
  "timeoutMs": 30000,
  "retryMax": 3,
  "status": "active"
}
```

**Mappings live in files, not in the row.** `requestMapping` and `responseMapping` hold a
path relative to the registry root, matching
`^mappings/(?!.*\.\.)[a-z0-9][a-z0-9._/-]*\.jsonata$`:

```
registry/mappings/<provider>/<action>[.<step>].<request|response>.jsonata
```

*Why not inline.* The Mausamgram response transform is **76 lines of JSONata**. Stored
in the row it is one JSON string with every newline escaped — unreviewable in a diff,
unhighlightable in an editor. As files they diff, review and lint like the code they are.
The adapter loads and caches them when it resolves the binding.

The pattern is strict on purpose:

- it pins the directory, so a mapping cannot point at an arbitrary file;
- it rejects `..` by negative lookahead, so a row cannot traverse out of `mappings/`;
- it requires the `.jsonata` extension;
- it allows **lowercase only** — a path differing from the file on disk only by case
  resolves on a macOS checkout and 404s on a Linux pod.

Each is a negative control in [Conformance](reference/conformance.md). The pattern cannot
check that the file *exists*; that is a referential check, and the same suite runs it
across every binding, including the ones in [Use cases](usecases/README.md).

**One action, several upstream calls.** Most bindings are one Beckn action → one HTTP
call. PM-Kisan and PMFBY are not: a beneficiary status needs an OTP verified and *then*
the benefit fetched, against two paths, and PMFBY needs three. `steps[]` models that:

```json
"steps": [
  { "id": "verifyOtp", "method": "POST", "path": "/ChatbotOTPVerified",
    "requestMapping": "mappings/pm-kisan/status.verify-otp.request.jsonata" },
  { "id": "benefit",   "method": "POST", "path": "/ChatbotBeneficiaryStatus",
    "requestMapping": "mappings/pm-kisan/status.benefit.request.jsonata" }
]
```

Steps run in order; each one's JSONata sees `{beckn, _local, steps}`, so a later step
reads an earlier response as `steps.verifyOtp`, and the single `responseMapping` sees all
of them. A step may name an `authProfile` when that endpoint authenticates differently
from the rest of the provider. Any step failing NACKs the whole action — there are no
partial results.

`steps[]` is data, not provider-specific code: the executor walks the array and knows
nothing about PM-Kisan or PMFBY — see [pm-kisan](usecases/pm-kisan.md).

**One action proving something for the next.** PMFBY will not return a claim to anyone
who has not answered an OTP. The OTP arrives by SMS, out of band, so the flow cannot be
one request: the farmer's app sends `init` (PMFBY texts a code), the farmer reads the SMS,
the app sends `status` carrying the code, and only then may it send `discover` for the
claim itself. Three Beckn actions, one `transaction_id` — and that constancy is the whole
mechanism. Two fields express it:

```json
"steps": [
  { "id": "verifyMobile", "method": "POST", "path": "/api/v1/services/nic/verifyMobile",
    "requestMapping": "mappings/pmfby/status.verify-mobile.request.jsonata",
    "sessionGrant": { "scope": "otpVerified", "ttlSeconds": 900 } },
  ...
]
```

```json
"bindingKey": "pmfby|openagrinet:InsuranceClaim|discover",
"action": "discover",
"sessionGate": { "scope": "otpVerified" }
```

A **grant** records that a scope was proven when *that call* succeeds. A **gate** refuses
the whole action unless a live grant for that scope exists against the incoming
`transaction_id`. They match on the scope string, nothing else.

The placement is asymmetric, on purpose:

- **The grant sits on the step that earns it**, not on the binding. Any step failing
  NACKs the whole action, so a binding-level grant would be discarded when some *later,
  unrelated* step 500s — and the OTP has already been consumed upstream, so the farmer
  would have to request a fresh one to recover from a failure that was not theirs.
- **The gate sits on the binding.** A gate on a step fails late: the executor would
  already have made a call it was never permitted to make.

The schema enforces both placements rather than documenting them — `sessionGrant` at
binding level on a multi-step record is rejected by the `oneOf`, `sessionGate` on a step
by `additionalProperties: false`. Six negative controls in
[Conformance](reference/conformance.md) cover the illegal placements, three positives the
legal ones.

`ttlSeconds` is **required** on every grant, and bounded to 60–3600. Required because the
current NestJS adapter holds this state in two module-level `Map`s that nothing ever
deletes — one of them, `pmfbyVerifiedTransactions`, is the live auth gate, and the
`OTP_EXPIRY_TIME` constant beside it is dead code. A proof of identity that never expires
is not a session. Bounded because a day-long grant is a different thing entirely.

**Where it is stored: ONIX's existing `cache` plugin.** No new table, no new plugin type.
`beckn-onix/pkg/plugin/definition/cache.go` already defines
`Set(ctx, key, value string, ttl time.Duration) error` — TTL is a required *argument*, so
the expiry cannot be forgotten the way a constant can. Key
`session:{scope}:{transaction_id}`, value the subject hash. The **generic executor** reads
and writes it; no provider plugin ever touches the store, exactly as no provider plugin
walks `steps[]`. The registry says *what* must be proven and for how long; the plugin
still knows only how to shape one upstream call.

The join is deliberate denormalisation:

```
Provider.providerId ───────┐
                           ├──▶ bindingKey ──▶ the call plan
Capability.capabilityCode ─┘
```

Both halves arrive in the `select` body, so the adapter builds the key from the request
and resolves the plan in one exact-match read, then fetches `Provider` by `providerId`
for the base URL and credentials. Two single-field reads, no join — see
[Search API](#search-api) below.

**Records are wrapped on the wire.** Each schema's top level is
`required: ["Provider"]` (or `Capability`, `ProviderCapability`), so RC write bodies —
and the files in `registry/samples/` — nest the record one level down:
`{"Provider": { "providerId": "mausamgram", ... }}`. The examples above show the inner
object.

`_osConfig` on each: `uniqueIndexFields` is the single key field, `roles:
["registryAdmin"]`, and `indexFields` covers `status` plus whatever that entity is looked
up by — `Provider` indexes `status` alone, `ProviderCapability` adds `providerId` and
`capabilityCode`.

## Search API

Sunbird RC generates the REST surface from the schema. The adapter needs exactly two
reads, both single-field indexed:

```
POST /api/v1/ProviderCapability/search
{ "filters": { "bindingKey": { "eq": "mausamgram|openagrinet:WeatherObservation|select" },
               "status":     { "eq": "active" } } }

POST /api/v1/Provider/search
{ "filters": { "providerId": { "eq": "mausamgram" },
               "status":     { "eq": "active" } } }
```

The first returns the call plan, the second the base URL and auth block. Writes
(`POST /api/v1/{Entity}`, `PUT /api/v1/{Entity}/{osid}`) are onboarding-path only — the
adapter never writes.

Two operational notes:

- **Preload `Provider` at boot; only the first read is on the hot path.** The whole
  table is 8 rows, about 1 KB. Load it once, refresh on a TTL, and a request costs one
  exact-match `ProviderCapability` read — the same single read the
  [merged schema](reference/merged-schema.md) buys by denormalising, but without
  duplicating credentials across bindings. Cache `ProviderCapability` on `bindingKey`
  too: both entities change on the order of weeks, and invalidation is a redeploy or a
  TTL, not a protocol.
- **Confirm the RC version.** `_osConfig` support and the exact search filter grammar
  differ across Sunbird RC releases. The `bindingKey` design deliberately sidesteps
  composite unique indexes, but `uniqueIndexFields`, `indexFields` and `roles` should be
  checked against the deployed build (see [Open issues](reference/open-issues.md)).
