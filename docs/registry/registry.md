# OpenAgriNet registry

The registry stores **providers, capabilities, and the binding between them**. The binding
is the call plan: given a provider and a capability, how do you reach them.

Nothing else lives here — no catalogs, no resources, no search index.

**Who reads it:** the adapter (or the adopter's experience layer).
**Who does not:** discovery-service. It answers `/discover` from its own catalog store.

| | | |
|---|---|---|
| 1 | [How it fits](#1-how-it-fits) | Two hops, and which one reads the registry |
| 2 | [Three schemas](#2-three-schemas) | What each one answers |
| 3 | [The schemas](#3-the-schemas) | JSON Schema — the contract |
| 4 | [Examples](#4-examples) | One record per entity, every property used |
| 5 | [APIs](#5-apis) | Create, search, update, delete — with payloads |
| 6 | [Do today's providers fit?](#6-do-todays-providers-fit) | All 5 v1 providers, validated |
| → | [usecases.md](usecases.md) | Each use case end to end |

---

## 1. How it fits

Two calls. **The first finds who. The second gets the data.**

| hop | call | goes to | reads registry |
|---|---|---|---|
| ① | `discover` | discovery service | **no** |
| ② | `select` | adapter → provider | **yes** — twice |

```
FARMER ─▶ EXPERIENCE LAYER ─① discover ──▶ DISCOVERY SVC   "mausamgram serves WeatherObservation"
                           ─② select ───▶ ADAPTER ─▶ REGISTRY   read binding + provider
                                                   └─▶ PROVIDER  call, map back to Beckn
```

Hop ① returns an **advertisement** (`informationMode: OnDemand`) — no values in it.
Hop ② returns the **data** (`informationMode: Direct`). Same `@type`, same `@context`;
the mode is the only difference, and it is why a second call exists.

**Adapter placement.** Either one adapter at the centre, or one adapter per layer
(experience-layer adapter calls `/discover`, then calls the provider adapter for
`select`/`init`/`status`). Hop ② is identical in both. What changes is **who holds the
upstream credentials** — with one central adapter, it does; with per-layer adapters, each
provider adapter holds its own.

---

## 2. Three schemas

| schema | answers | v1 records |
|---|---|---|
| **`Provider`** | Where is this provider, and how do we authenticate to it? | 5 |
| **`Capability`** | What outcome is this, in network vocabulary? | 3 |
| **`ProviderCapability`** | Which endpoint of that provider answers that capability, and how is it shaped? | 5 |

They join on ids, not on foreign keys — Sunbird RC has none:

```
Provider.providerId ─┐
                     ├─▶ ProviderCapability.bindingKey = "<providerId>|<capabilityCode>"
Capability.capabilityCode ─┘
```

Schema files: [`schemas/Provider.json`](schemas/Provider.json) ·
[`schemas/Capability.json`](schemas/Capability.json) ·
[`schemas/ProviderCapability.json`](schemas/ProviderCapability.json).
All three are draft-07 with `additionalProperties: false` and an RC `_osConfig` block.

---

## 3. The schemas

### 3.0 Shared definitions

RC loads each entity schema on its own, so a `$ref` across files is not available. The six
building blocks below are therefore repeated **verbatim** in every file that uses them,
under the same name — so a mismatch is a diff, not a judgement call.

| definition | value | used by |
|---|---|---|
| `Status` | `active` \| `inactive` | all three |
| `ProviderId` | `^[a-z0-9][a-z0-9._:-]{2,63}$` | `Provider`, `ProviderCapability` |
| `TypeCode` | `^openagrinet:[A-Z][A-Za-z0-9]*$` | `Capability.baseTypes` |
| `CapabilityCode` | `TypeCode`, and not `AgricultureResource`/`AgricultureCapability` | `Capability`, `ProviderCapability` |
| `Path` | `^/[A-Za-z0-9._~%/-]*$` — leading slash, no query string | `Provider.auth.login`, `ProviderCapability`, `Step` |
| `Secret` | `^(env://[A-Z][A-Z0-9_]*\|inline:.+)$` | `Provider.auth`, `Enricher` |

One `status` vocabulary across all three entities is deliberate. Every read filters
`status == "active"`; a `Capability` that said `deprecated` instead of `inactive` meant the
same thing to the reader and a different string to the filter.

### 3.1 `Provider`

| field | type | constraint | req |
|---|---|---|---|
| `providerId` | string | `^[a-z0-9][a-z0-9._:-]{2,63}$` — this is the Beckn `provider.id` | ✓ |
| `name` | string | `minLength: 1`, display only | ✓ |
| `baseUrl` | string | `^https?://[^/].*[^/]$` — no trailing slash. `https` required if any credential is held | ✓ |
| `status` | string | `active` \| `inactive` | ✓ |
| `auth` | object | → `Auth` — the credential for every call to this provider | ✓ |
| `signing` | object | → `Signing` — the provider's **public** key, for verifying what it sends back | |

**Two key blocks, opposite directions.**

| | direction | holds | v1 |
|---|---|---|---|
| `auth` | **outbound** — us → provider | a credential | all 5 providers |
| `signing` | **inbound** — provider → us | a public key | unused |

`signing` is not a third auth mode. A public key is not a secret, so it has no `env://`
indirection and nothing to redact; publishing it is what it is for.

**When `signing` is actually needed:** only when the provider is itself the sender — it
runs its own adapter and signs its replies, or it POSTs back to a callback URL of ours.
In that case TLS proves nothing about *who* sent the body, and the public key is the only
check.

In v1 none of that happens. We run the adapter, we call out, and HTTPS already proves we
reached the host we meant to. So `signing` stays **optional and empty** — all 13 seed
records omit it. It is in the schema so a provider that later joins as a signing
participant needs no migration; it is not something an operator fills in today.

**Auth is on `Provider`, not on the binding.** One credential opens all of that provider's
endpoints — true for all five. On `ProviderCapability` it would be copied into every
binding row, and a rotation would touch N rows instead of one.

If a provider ever needs a *different* credential on some endpoint, the addition is an
optional `auth` on the binding or on a `Step`, overriding `Provider.auth` — same shape,
precedence step > binding > provider. That is additive and needs no migration, so v1 does
not carry it.

`Provider.baseUrl` is **where** the provider is; `method` + `path` on the binding is
**which endpoint** answers a capability. They are split because one provider serves several
capabilities from different paths — put `path` on `Provider` and each needs a duplicate
record, which means a duplicate credential.

```
https://mausamgram.imd.gov.in/nwpapi  +  /get-daily
└────────── Provider.baseUrl ───────┘     └─ ProviderCapability.path ─┘
```
`baseUrl` forbids a trailing slash, `path` requires a leading one — exactly one `/` falls
between them, so no code normalises. `path` also forbids `?`: a query string belongs to the
`requestMapping`, which builds it from the request, so a value never reaches the wire by
being concatenated into a stored string.

**`Auth`**

| field | constraint | req |
|---|---|---|
| `scheme` | `none` \| `apiKeyQuery` \| `apiKeyHeader` \| `basic` \| `bearer` \| `loginToken` \| `encryptedEnvelope` | ✓ |
| `paramName` | the query-parameter or header name | per scheme |
| `secrets` | every value `^(env://[A-Z][A-Z0-9_]*\|inline:.+)$` | per scheme |
| `extraHeaders` | `Secret` — a **second credential**, when one header is not enough | |
| `envelope` | `{ "algorithm": "aes-128-cbc" \| "aes-256-cbc" \| "aes-256-gcm" }` | per scheme |
| `login` | → `Login`, only for `loginToken` | per scheme |

`extraHeaders` is `Secret`-valued because it is for a **second credential** — a provider
that wants both an API key and a tenant token. A constant, non-secret header is not auth
and does not belong here; the `requestMapping` builds the upstream request, headers
included, and that is where it goes.

| `scheme` | the adapter does | then requires |
|---|---|---|
| `none` | sends no credential | **must not** carry `secrets` |
| `apiKeyQuery` | appends `?<paramName>=<secret>` | `paramName`, `secrets` |
| `apiKeyHeader` | sets `<paramName>: <secret>` | `paramName`, `secrets` |
| `bearer` | sets `<paramName>: Bearer <secret>` | `paramName`, `secrets` |
| `basic` | RFC 7617 from `secrets.username` / `secrets.password` | `secrets.username`, `secrets.password` |
| `loginToken` | calls `login`, caches the token for `ttlSeconds`, sends it as `<paramName>` | `paramName`, `secrets`, `login` |
| `encryptedEnvelope` | encrypts the body under a key from `secrets` | `secrets`, `envelope` |

**A secret has exactly two legal forms, and must say which:**

```
"secrets": { "password": "env://MAUSAMGRAM_PASS" }   // pointer — resolved from the adapter's environment
"secrets": { "token":    "inline:a7f3c9d2e1b8..." }  // the credential itself, stored here
```

A bare pasted key matches neither and is rejected at write time. **Prefer `env://`** — the
registry then holds no credential at all. `inline:` exists for operators who cannot set the
adapter's environment, and it costs three things: `/search` is authenticated
([§5](#5-apis)), the database holds live key material, and rotation becomes a registry
write.

**A credential implies TLS.** Every scheme except `none` requires `secrets`, so the schema
forces `baseUrl` to `https` whenever `auth.scheme != "none"`.
Plaintext stays legal for `scheme: "none"` only.

<details><summary><b>Provider.json</b> — full JSON Schema</summary>

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "Provider",
  "type": "object",
  "required": ["Provider"],
  "properties": { "Provider": { "$ref": "#/definitions/Provider" } },

  "definitions": {

    "Provider": {
      "type": "object",
      "additionalProperties": false,
      "required": ["providerId", "name", "baseUrl", "status", "auth"],
      "properties": {
        "providerId": { "$ref": "#/definitions/ProviderId" },
        "name":       { "type": "string", "minLength": 1 },
        "baseUrl":    { "type": "string", "pattern": "^https?://[^/].*[^/]$" },
        "status":     { "$ref": "#/definitions/Status" },
        "auth":       { "$ref": "#/definitions/Auth" },
        "signing":    { "$ref": "#/definitions/Signing" }
      },
      "allOf": [
        {
          "if": {
            "required": ["auth"],
            "properties": { "auth": { "required": ["scheme"],
                                      "properties": { "scheme": { "not": { "const": "none" } } } } }
          },
          "then": { "properties": { "baseUrl": { "pattern": "^https://[^/].*[^/]$" } } }
        }
      ]
    },

    "Auth": {
      "type": "object",
      "additionalProperties": false,
      "required": ["scheme"],
      "properties": {
        "scheme":       { "type": "string",
                          "enum": ["none", "apiKeyQuery", "apiKeyHeader", "basic",
                                   "bearer", "loginToken", "encryptedEnvelope"] },
        "paramName":    { "type": "string", "minLength": 1 },
        "secrets":      { "type": "object", "minProperties": 1,
                          "additionalProperties": { "$ref": "#/definitions/Secret" } },
        "extraHeaders": { "type": "object", "minProperties": 1,
                          "additionalProperties": { "$ref": "#/definitions/Secret" } },
        "envelope":     { "type": "object", "additionalProperties": false,
                          "required": ["algorithm"],
                          "properties": { "algorithm": { "type": "string",
                                          "enum": ["aes-128-cbc", "aes-256-cbc", "aes-256-gcm"] } } },
        "login":        { "$ref": "#/definitions/Login" }
      },
      "allOf": [
        { "if":   { "required": ["scheme"], "properties": { "scheme": { "const": "none" } } },
          "then": { "not": { "required": ["secrets"] } } },
        { "if":   { "required": ["scheme"], "properties": { "scheme": { "enum": ["apiKeyQuery", "apiKeyHeader", "bearer"] } } },
          "then": { "required": ["paramName", "secrets"] } },
        { "if":   { "required": ["scheme"], "properties": { "scheme": { "const": "basic" } } },
          "then": { "required": ["secrets"],
                    "properties": { "secrets": { "required": ["username", "password"] } } } },
        { "if":   { "required": ["scheme"], "properties": { "scheme": { "const": "loginToken" } } },
          "then": { "required": ["paramName", "secrets", "login"] } },
        { "if":   { "required": ["scheme"], "properties": { "scheme": { "const": "encryptedEnvelope" } } },
          "then": { "required": ["secrets", "envelope"] } }
      ]
    },

    "Login": {
      "type": "object",
      "additionalProperties": false,
      "required": ["path", "tokenPath", "ttlSeconds"],
      "properties": {
        "path":        { "$ref": "#/definitions/Path" },
        "tokenPath":   { "type": "string", "pattern": "^[A-Za-z_][A-Za-z0-9_.]*$" },
        "ttlSeconds":  { "type": "integer", "minimum": 30, "maximum": 86400 },
        "method":      { "type": "string", "enum": ["GET", "POST"], "default": "POST" },
        "bodyMapping": { "type": "string", "minLength": 1 }
      }
    },

    "Signing": {
      "type": "object",
      "additionalProperties": false,
      "required": ["keyId", "publicKey", "algorithm", "validFrom", "validUntil"],
      "properties": {
        "keyId":      { "type": "string",
                        "pattern": "^[a-z0-9][a-z0-9._:-]{2,63}\\|[a-z0-9-]{1,32}\\|[a-z0-9-]{1,32}$" },
        "publicKey":  { "type": "string", "pattern": "^[A-Za-z0-9+/]+={0,2}$" },
        "algorithm":  { "type": "string", "enum": ["ed25519", "ed25519-raw"] },
        "validFrom":  { "type": "string", "format": "date-time" },
        "validUntil": { "type": "string", "format": "date-time" }
      }
    },

    "ProviderId": { "type": "string", "pattern": "^[a-z0-9][a-z0-9._:-]{2,63}$" },
    "Status": { "type": "string", "enum": ["active", "inactive"] },
    "Path": { "type": "string", "pattern": "^/[A-Za-z0-9._~%/-]*$" },
    "Secret": { "type": "string", "pattern": "^(env://[A-Z][A-Z0-9_]*|inline:.+)$" }
  },

  "_osConfig": {
    "uniqueIndexFields": ["providerId"],
    "indexFields":       ["status"],
    "privateFields":     ["$.auth.secrets", "$.auth.extraHeaders"],
    "systemFields":      ["osCreatedAt", "osUpdatedAt", "osCreatedBy", "osUpdatedBy"]
  }
}
```
</details>

---

### 3.2 `Capability`

| field | type | constraint | req |
|---|---|---|---|
| `capabilityCode` | string | `^openagrinet:[A-Z][A-Za-z0-9]*$`, and not `AgricultureResource`/`AgricultureCapability`. **This is the outcome `@type`** | ✓ |
| `name` | string | `minLength: 1` | ✓ |
| `schemaUrl` | string | the network-specs pack, **pinned to a commit sha** — a branch ref is rejected | ✓ |
| `status` | string | `active` \| `inactive` | ✓ |
| `baseTypes` | array | `TypeCode`, unique — shared field sets this pack composes with `allOf` | |

**Nothing names a provider here.** A capability is network vocabulary; the binding attaches
it to a provider. The sha pin matters because a capability pinned to `main` validates
against a different contract each week, silently.

<details><summary><b>Capability.json</b> — full JSON Schema</summary>

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "Capability",
  "type": "object",
  "required": ["Capability"],
  "properties": { "Capability": { "$ref": "#/definitions/Capability" } },

  "definitions": {

    "Capability": {
      "type": "object",
      "additionalProperties": false,
      "required": ["capabilityCode", "name", "schemaUrl", "status"],
      "properties": {
        "capabilityCode": { "$ref": "#/definitions/CapabilityCode" },
        "name":           { "type": "string", "minLength": 1 },
        "schemaUrl":      { "type": "string",
                            "pattern": "^https://(?!.*/refs/heads/)(?!.*/(main|master|develop)/).+/attributes\\.yaml$" },
        "status":         { "$ref": "#/definitions/Status" },
        "baseTypes":      { "type": "array", "uniqueItems": true,
                            "items": { "$ref": "#/definitions/TypeCode" } }
      }
    },

    "CapabilityCode": { "type": "string",
                        "pattern": "^openagrinet:[A-Z][A-Za-z0-9]*$",
                        "not": { "pattern": "^openagrinet:Agriculture(Capability|Resource)$" } },
    "TypeCode": { "type": "string", "pattern": "^openagrinet:[A-Z][A-Za-z0-9]*$" },
    "Status": { "type": "string", "enum": ["active", "inactive"] }
  },

  "_osConfig": {
    "uniqueIndexFields": ["capabilityCode"],
    "indexFields":       ["status"],
    "systemFields":      ["osCreatedAt", "osUpdatedAt", "osCreatedBy", "osUpdatedBy"]
  }
}
```
</details>

---

### 3.3 `ProviderCapability`

The entity that does the work. A record is **one call** or **a plan of 2–6 calls**, never
half of each — enforced by a `oneOf`, not by convention.

| field | type | constraint | req |
|---|---|---|---|
| `bindingKey` | string | exactly `providerId` + `\|` + `capabilityCode` — **two segments, no action** | ✓ |
| `providerId` | string | must equal segment 1 | ✓ |
| `capabilityCode` | string | must equal segment 2 | ✓ |
| `status` | string | `active` \| `inactive` | ✓ |
| `responseMapping` | string | `^mappings/…\.jsonata$` — upstream response → Beckn v2 resources | ✓ |
| `method` | string | `GET` \| `POST` | single-call |
| `path` | string | `Path` — appended to `Provider.baseUrl`. **No query string**: query parameters are built by the `requestMapping` | single-call |
| `requestMapping` | string | Beckn request → upstream request | single-call |
| `steps` | array | 2–6 × `Step` | multi-step |
| `enricher` | object | → `Enricher` — a Go plugin run **before** the request mapping | |
| `timeoutMs` | integer | 1000–120000, default 15000 — per call, not per plan | |
| `retryMax` | integer | 0–5, default 0 | |
| `sessionGate` | object | `{scope}` — refuse the action unless a live grant exists | |
| `sessionGrant` | object | `{scope, ttlSeconds}` — record that a scope was proven. Binding level **only** on a single-call record | |

**`Step`** — `id`, `method`, `path`, `requestMapping` required; `timeoutMs`, `retryMax`,
`sessionGrant` optional. A step that omits a timeout or a retry count takes the binding's;
a binding that omits one takes the schema default. Precedence is **step > binding >
default**, the same order as the `auth` override described in [§3.1](#31-provider). **Steps model a sequence, not a credential**: they exist because
call 2 needs call 1's *output*. Each step's JSONata sees `{beckn, _local, steps}`, so a
later step reads an earlier response as `steps.<id>` — that is the whole point of them.

**`Enricher`** — `{name, config, secrets}`, always the object form. It exists only for what
the Beckn body cannot express: a private code namespace (Agmarknet's `marketcode`), or a
lookup against something the adapter owns (`nearestStation`'s Postgres). *If a JSONata
expression can do it, it is a mapping, not an enricher.*

**Mappings live in files, not in the row** —
`mappings/<provider>/<action>.<request|response>.jsonata`. The row stores the path; the
file is reviewed and diffed like source. The pattern rejects `..` traversal and uppercase.

| mapping | input | output |
|---|---|---|
| `enricher` (Go) | the Beckn request | `_local` |
| `requestMapping` | `{beckn, _local}` | the upstream request |
| step `requestMapping` | `{beckn, _local, steps}` | that step's upstream request |
| `responseMapping` | `{beckn, _local, steps}` + the response | Beckn v2 resources |

<details><summary><b>ProviderCapability.json</b> — full JSON Schema</summary>

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "ProviderCapability",
  "type": "object",
  "required": ["ProviderCapability"],
  "properties": { "ProviderCapability": { "$ref": "#/definitions/ProviderCapability" } },

  "definitions": {

    "ProviderCapability": {
      "type": "object",
      "additionalProperties": false,
      "required": ["bindingKey", "providerId", "capabilityCode", "status", "responseMapping"],
      "properties": {
        "bindingKey": {
          "type": "string",
          "pattern": "^[a-z0-9][a-z0-9._:-]{2,63}\\|openagrinet:[A-Z][A-Za-z0-9]*$",
          "not": { "pattern": "\\|openagrinet:Agriculture(Capability|Resource)$" }
        },
        "providerId":     { "$ref": "#/definitions/ProviderId" },
        "capabilityCode": { "$ref": "#/definitions/CapabilityCode" },
        "status":         { "$ref": "#/definitions/Status" },

        "method":          { "type": "string", "enum": ["GET", "POST"] },
        "path":            { "$ref": "#/definitions/Path" },
        "requestMapping":  { "$ref": "#/definitions/MappingPath" },
        "steps":           { "type": "array", "minItems": 2, "maxItems": 6,
                             "items": { "$ref": "#/definitions/Step" } },

        "responseMapping": { "$ref": "#/definitions/MappingPath" },
        "enricher":        { "$ref": "#/definitions/Enricher" },
        "timeoutMs":       { "type": "integer", "minimum": 1000, "maximum": 120000, "default": 15000 },
        "retryMax":        { "type": "integer", "minimum": 0, "maximum": 5, "default": 0 },
        "sessionGate":     { "$ref": "#/definitions/SessionGate" },
        "sessionGrant":    { "$ref": "#/definitions/SessionGrant" }
      },

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
    },

    "Step": {
      "type": "object",
      "additionalProperties": false,
      "required": ["id", "method", "path", "requestMapping"],
      "properties": {
        "id":             { "type": "string", "pattern": "^[a-z][a-zA-Z0-9]*$" },
        "method":         { "type": "string", "enum": ["GET", "POST"] },
        "path":           { "$ref": "#/definitions/Path" },
        "requestMapping": { "$ref": "#/definitions/MappingPath" },
        "timeoutMs":      { "type": "integer", "minimum": 1000, "maximum": 120000 },
        "retryMax":       { "type": "integer", "minimum": 0, "maximum": 5 },
        "sessionGrant":   { "$ref": "#/definitions/SessionGrant" }
      }
    },

    "Enricher": {
      "type": "object",
      "additionalProperties": false,
      "required": ["name"],
      "properties": {
        "name":    { "type": "string", "pattern": "^[a-z][a-zA-Z0-9]*$" },
        "config":  { "type": "object" },
        "secrets": { "type": "object", "minProperties": 1,
                     "additionalProperties": { "$ref": "#/definitions/Secret" } }
      }
    },

    "SessionGate": {
      "type": "object", "additionalProperties": false, "required": ["scope"],
      "properties": { "scope": { "type": "string", "pattern": "^[a-z][a-zA-Z0-9]*$" } }
    },

    "SessionGrant": {
      "type": "object", "additionalProperties": false, "required": ["scope", "ttlSeconds"],
      "properties": {
        "scope":      { "type": "string", "pattern": "^[a-z][a-zA-Z0-9]*$" },
        "ttlSeconds": { "type": "integer", "minimum": 60, "maximum": 3600 }
      }
    },

    "MappingPath": { "type": "string",
                     "pattern": "^mappings/(?!.*\\.\\.)[a-z0-9][a-z0-9._/-]*\\.jsonata$" },

    "ProviderId": { "type": "string", "pattern": "^[a-z0-9][a-z0-9._:-]{2,63}$" },
    "CapabilityCode": { "type": "string",
                        "pattern": "^openagrinet:[A-Z][A-Za-z0-9]*$",
                        "not": { "pattern": "^openagrinet:Agriculture(Capability|Resource)$" } },
    "Status": { "type": "string", "enum": ["active", "inactive"] },
    "Path": { "type": "string", "pattern": "^/[A-Za-z0-9._~%/-]*$" },
    "Secret": { "type": "string", "pattern": "^(env://[A-Z][A-Z0-9_]*|inline:.+)$" }
  },

  "_osConfig": {
    "uniqueIndexFields": ["bindingKey"],
    "indexFields":       ["providerId", "capabilityCode", "status"],
    "privateFields":     ["$.enricher.secrets"],
    "systemFields":      ["osCreatedAt", "osUpdatedAt", "osCreatedBy", "osUpdatedBy"]
  }
}
```
</details>

---

### 3.4 Four rules the schema cannot express

JSON Schema cannot compare two fields. These run in the onboarding path and in the
conformance suite:

1. `bindingKey` **must equal** `providerId` + `"|"` + `capabilityCode`.
2. Both must resolve to **live** records — an `active` `Provider` and an `active`
   `Capability`. RC enforces no reference between entities.
3. `signing.keyId` — segment 1 must equal `providerId`, segment 3 must equal `algorithm`.
4. `signing.validUntil` must be after `validFrom`.

Not yet built, and named as open: resolving every `enricher.name` against the adapter's
plugin table at boot, and refusing to start if one is missing.

---

## 4. Examples

One record per entity, using **every property** — including the ones no v1 record needs.
The thirteen records actually seeded are in **[examples.md](examples.md)**.

**`Provider`** — every property

```json
{ "Provider": {
  "providerId": "example-provider",
  "name": "Every property, in one record",
  "baseUrl": "https://api.example.gov.in/v2",
  "status": "active",
  "auth": {
    "scheme": "loginToken",
    "paramName": "Authorization",
    "secrets": { "username": "env://EXAMPLE_USER", "password": "env://EXAMPLE_PASS" },
    "extraHeaders": { "x-tenant-key": "env://EXAMPLE_TENANT_KEY" },
    "login": {
      "path": "/auth/login",
      "method": "POST",
      "tokenPath": "data.accessToken",
      "ttlSeconds": 3600,
      "bodyMapping": "{ \"user\": $.username, \"pass\": $.password }"
    }
  },
  "signing": {
    "keyId": "example-provider|key-1|ed25519",
    "publicKey": "MCowBQYDK2VwAyEAGb9ECWmEzf6FQbrBZ9w7lshQhqowtrbLDFw4rXAxZuE=",
    "algorithm": "ed25519",
    "validFrom": "2026-08-01T00:00:00Z",
    "validUntil": "2027-08-01T00:00:00Z"
  }
} }
```

`encryptedEnvelope` is the one scheme not shown above, because it is exclusive with
`loginToken`. It looks like this:

```json
{ "scheme": "encryptedEnvelope",
  "secrets": { "aesKey": "env://EXAMPLE_AES_KEY" },
  "envelope": { "algorithm": "aes-256-gcm" } }
```

**`Capability`** — every property

```json
{ "Capability": {
  "capabilityCode": "openagrinet:WeatherObservation",
  "name": "Weather Observation and Forecast",
  "schemaUrl": "https://raw.githubusercontent.com/OpenAgriNet/network-specs/3e593b3/schema/WeatherObservation/v0.1/attributes.yaml",
  "status": "active",
  "baseTypes": ["openagrinet:AgricultureResource"]
} }
```

**`ProviderCapability`, single call** — the shape all five v1 bindings use. This one is
illustrative and uses every optional field; the five real ones are in
[examples.md](examples.md).

```json
{ "ProviderCapability": {
  "bindingKey": "example-provider|openagrinet:WeatherObservation",
  "providerId": "example-provider",
  "capabilityCode": "openagrinet:WeatherObservation",
  "status": "active",

  "method": "GET",
  "path": "/get-daily",
  "requestMapping": "mappings/example-provider/select.request.jsonata",

  "responseMapping": "mappings/example-provider/select.response.jsonata",
  "enricher": { "name": "nearestStation",
                "config": { "maxDistanceKm": 50, "maxStationAttempts": 5 },
                "secrets": { "dsn": "env://IMD_DB_DSN" } },
  "timeoutMs": 30000,
  "retryMax": 3,
  "sessionGate": { "scope": "consentGiven" },
  "sessionGrant": { "scope": "weatherFetched", "ttlSeconds": 900 }
} }
```

**`ProviderCapability`, multi-step** — verify an OTP, then fetch what it unlocked

```json
{ "ProviderCapability": {
  "bindingKey": "pm-kisan|openagrinet:SchemeBeneficiaryStatus",
  "providerId": "pm-kisan",
  "capabilityCode": "openagrinet:SchemeBeneficiaryStatus",
  "status": "active",

  "steps": [
    { "id": "verifyOtp",
      "method": "POST",
      "path": "/ChatbotOTPVerified",
      "requestMapping": "mappings/pm-kisan/status.verify-otp.request.jsonata",
      "timeoutMs": 10000,
      "retryMax": 1,
      "sessionGrant": { "scope": "otpVerified", "ttlSeconds": 900 } },

    { "id": "benefit",
      "method": "POST",
      "path": "/ChatbotBeneficiaryStatus",
      "requestMapping": "mappings/pm-kisan/status.benefit.request.jsonata" }
  ],

  "responseMapping": "mappings/pm-kisan/status.response.jsonata",
  "sessionGate": { "scope": "consentGiven" },
  "timeoutMs": 20000,
  "retryMax": 0
} }
```

`steps[]` is **data, not provider-specific code** — one generic executor walks the array
and knows nothing about PM-Kisan. Adding a two-call provider is a registry write and two
JSONata files. **No v1 binding uses this shape.**

---

## 5. APIs

Sunbird RC generates the REST surface from the three schemas. `<Entity>` is `Provider`,
`Capability` or `ProviderCapability`.

| route | who | what |
|---|---|---|
| `POST /api/v1/<Entity>` | Operator | create |
| `POST /api/v1/<Entity>/search` | authenticated (read-only role) | look up by indexed field |
| `GET /api/v1/<Entity>/{osid}` | authenticated | read one |
| `PUT /api/v1/<Entity>/{osid}` | Operator | replace in full |
| `DELETE /api/v1/<Entity>/{osid}` | Operator | remove permanently |

`osid` is RC's row id, returned by the create. It is **not** `providerId` and not
`bindingKey` — so an update has to search first.

### Create

```http
POST /api/v1/Provider
Authorization: Bearer <operator-token>
Content-Type: application/json
```
```json
{ "providerId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "baseUrl": "https://api.agmarknet.gov.in",
  "status": "active",
  "auth": { "scheme": "apiKeyQuery",
            "paramName": "token",
            "secrets": { "token": "env://MANDI_TOKEN" } } }
```
```json
200 OK
{ "id": "sunbird-rc.registry.create",
  "params": { "status": "SUCCESSFUL" },
  "result": { "Provider": { "osid": "1-8f2c4e7a-3b91-4d0e-9c55-2a1f6b8e0d34" } } }
```

Seed in order: **`Capability` → `Provider` → `ProviderCapability`.** The binding's
integrity rules need the other two to exist and be `active`.

### Search

```http
POST /api/v1/ProviderCapability/search
Authorization: Bearer <read-token>
```
```json
{ "filters": { "bindingKey": { "eq": "agmarknet|openagrinet:MandiPrice" },
               "status":     { "eq": "active" } } }
```
```json
200 OK
[ { "osid": "1-4c7d...", "bindingKey": "agmarknet|openagrinet:MandiPrice",
    "providerId": "agmarknet", "capabilityCode": "openagrinet:MandiPrice",
    "method": "GET", "path": "/v1/fetch-agmarknet-vistaar-location",
    "requestMapping": "mappings/agmarknet/select.request.jsonata",
    "responseMapping": "mappings/agmarknet/select.response.jsonata",
    "enricher": { "name": "marketAndCommodityCodes" },
    "timeoutMs": 20000, "retryMax": 2, "status": "active" } ]
```

**The adapter needs exactly two reads**, both single-field exact matches — the binding
above, then `POST /api/v1/Provider/search` with `{"providerId": {"eq": "agmarknet"}}`.
No join, and no `Capability` read: `Capability` is vocabulary, not part of the call path.

**`search` is not public.** A record may hold an `inline:` credential, so a read of
`Provider` is a read of live key material. Give the adapter a **read-only** role — a leaked
adapter token is then a dump, not a rewrite. If a verifier needs `signing.publicKey`
without registry credentials, publish that projection separately; do not reopen `/search`,
which returns `secrets` in the same response.

### Update

Replace in full — RC's `PUT` is not a merge patch. Search for the `osid`, change the field,
send the whole record back.

```http
PUT /api/v1/Provider/1-8f2c4e7a-3b91-4d0e-9c55-2a1f6b8e0d34
Authorization: Bearer <operator-token>
```
```json
{ "providerId": "agmarknet",
  "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
  "baseUrl": "https://api.agmarknet.gov.in",
  "status": "inactive",
  "auth": { "scheme": "apiKeyQuery",
            "paramName": "token",
            "secrets": { "token": "env://MANDI_TOKEN_2026" } } }
```

Rotating an `env://` pointer is a registry write; rotating the value behind it is not.
Rotating an `inline:` credential is always a registry write.

### Delete

```http
DELETE /api/v1/ProviderCapability/1-4c7d5e91-2a08-4f6b-8d13-77e0c9a4b521
Authorization: Bearer <operator-token>
```
```json
200 OK
{ "id": "sunbird-rc.registry.delete", "params": { "status": "SUCCESSFUL" } }
```

**Prefer `status: "inactive"` over `DELETE`.** Every read filters on `status`, so flipping
it takes a provider out of service just as completely and leaves the row where an operator
can see what was turned off. `DELETE` orphans quietly — removing a `Provider` leaves its
bindings pointing at nothing, and RC enforces no reference between them.

### The runtime does not call these per request

```
13 records — 5 Provider, 3 Capability, 5 ProviderCapability.  A few KB.
```

Load all three entities **at boot**, index `ProviderCapability` by `bindingKey` and
`Provider` by `providerId`. Resolution is then two map lookups and the per-request registry
cost is zero reads. Records change on the order of weeks; refresh is a redeploy or a TTL.

`/search` works **without Elasticsearch** on the pinned build (`RELEASE_VERSION=v2.0.0`) —
checked, not assumed. One thing still to confirm on first boot: which read returns every
row of an entity, for the boot load. With no ES, `indexFields` buys nothing at runtime; it
stays declared because it documents what is meant to be queryable.

---

## 6. Do today's providers fit?

Yes — all four v1 categories, all five providers, all five bindings. No field left over
and nothing forced.

**Realtime Information**

| use case | capability | provider | transport | auth | binding | enricher |
|---|---|---|---|---|---|---|
| **Weather** — point forecast | `WeatherObservation` | `mausamgram` | HTTPS REST | `basic` | single, `GET /get-daily` | `pointFromIntent` |
| **Weather** — city / station | `WeatherObservation` | `imd-city-weather` | HTTPS REST | `none` | single, `GET /citywx/city_weather_test.php` | `nearestStation` (+config, +secret) |
| **Mandi prices** | `MandiPrice` | `agmarknet` | HTTPS REST | `apiKeyQuery` | single, `GET /v1/fetch-agmarknet-vistaar-location` | `marketAndCommodityCodes` |

**Advisory (Knowledge)**

| use case | capability | provider | transport | auth | binding | enricher |
|---|---|---|---|---|---|---|
| **Schemes** | `KnowledgeResource` | `hasura-content` | HTTPS GraphQL | `apiKeyHeader` | single, `POST /v1/graphql` | `knowledgeQueryParams` |
| **Crop & pest** | `KnowledgeResource` | `oan-vector` | HTTP REST | `none` | single, `POST /indexes/oan-index/search` | `knowledgeQueryParams` |

Each of the five is traced end to end, with payloads at every hop, in
**[usecases.md](usecases.md)**.

**Two categories share one capability.** Schemes and Crop & pest are both
`openagrinet:KnowledgeResource`; they are separated at `discover` by a category filter, not
by a second capability code. Two bindings, same `capabilityCode`, different `providerId` —
which is exactly what `bindingKey` being two segments buys.

**GraphQL needs no new field.** It is `POST` to one path with the query in the body, built
by the `requestMapping` — using GraphQL *variables*, never string concatenation.

**`oan-vector` is plaintext**, and legal only because `scheme: none`. Moving it behind TLS
is onboarding work, not a schema change, and should happen before real traffic.

**No transport discriminator in v1.** Every provider is HTTP — five of five, and every
provider modelled beyond v1 too. Adding one later is purely additive: `"transport": "http"`
on the binding and on each `Step`, absent meaning `http`, existing records unchanged and
the `oneOf` untouched. Naming a second value now would mean inventing semantics nobody has.

Validated with `jsonschema` draft-07 against the three files in
[`schemas/`](schemas/) — 24 records in these pages pass, and 19 deliberately malformed
records (bare pasted secret, credential over plain HTTP, `..` in a mapping path, a query
string baked into `path`, a record carrying both `method` and `steps`, a `schemaUrl` pinned
to `main`) are all rejected. The one that slips through is the `signing.keyId` cross-field
rule — [§3.4](#34-four-rules-the-schema-cannot-express) says why, and where it runs
instead.

---

## Known gaps for v1

| | |
|---|---|
| No min/max qualifier on `parameter` | The pack's `parameters` item is `{parameter, value, unit}` with an eight-value enum and no aggregation field, so *tomorrow's high is 30.6, low 22.1* is inexpressible — and every Indian weather upstream reports `tmin`/`tmax`. Mappings emit a private `aggregation`, which validates while meaning nothing to anyone else. |
| `informationMode` is not in `docs/design/` | Zero mentions in our plan and zero in `src/`, yet it is `required` on every published resource and decides which half of each pack applies. The one gap here that changes our code. |
| Nothing re-pins `schemaUrl` | The three `Capability` records point at `3e593b3`. When network-specs moves, nothing notices. A check belongs in the seeding path. |
| `oan-vector` on plain HTTP | Legal, but should move behind TLS. |
| Enricher names are unvalidated | A binding naming a plugin that does not exist fails at call time, not at boot. |
| Shared definitions are copied, not referenced | RC loads each entity schema alone, so `Status`, `ProviderId`, `CapabilityCode`, `TypeCode`, `Path` and `Secret` are duplicated verbatim across files ([§3.0](#30-shared-definitions)). Identical names make a drift a diff, but nothing yet fails a build on it. |
| A second credential is untested | `auth.extraHeaders` is in the schema and no v1 provider uses it, so the two-header path has never run. |
