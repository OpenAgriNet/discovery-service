# Adding a new provider

A checklist. Nothing here needs adapter code unless step 5 says so — the point of the
design is that a new upstream is a set of registry rows and a folder of JSONata.

*[Overview](01-overview.md) · [Registry schema](02-registry-schema.md) · [Use cases](usecases/README.md) · [docs home](README.md)*

---

## 1. Pick the capability — do not invent one

Look for an existing `Capability` record first. A capability is **network vocabulary**: it
is what the whole network agrees a request means, so a BV-only name defeats the point. If
none fits, the new one has to come from a network-specs pack with an `attributes.yaml`,
and adding it is a conversation with the network, not a row you write alone.

- `capabilityCode` must be the **specific** resource `@type` — the same one the outcome
  carries — never the shared base field set `openagrinet:AgricultureResource`.
  The schema rejects the base type outright — it identifies nothing.
- `schemaUrl` points at the pack's `attributes.yaml`, **not** `profile.json`, and is pinned
  to a commit SHA. Branch refs are rejected by pattern.
- Every resource you emit must carry `informationMode` — `Direct` from a `select`,
  `OnDemand` from a catalog advertisement. The conformance suite rejects a
  `resourceAttributes` without one.

## 2. Decide the action

Not every read is a `select`. The rule of thumb, argued in
[Overview](01-overview.md): `select` when the caller names a provider and asks for data,
`init`/`status` when there is a transaction with state on the upstream side. If the answer
is not obvious, it belongs in [Open issues](reference/open-issues.md) before it belongs in
a row.

## 3. Write the `Provider` record

- `baseUrl` is the **only** host in play. A plugin that reaches for `process.env` for an
  address puts it somewhere nobody audits.
- Every credential value is an `env://VARNAME` pointer — in `auth.secrets`, in
  `auth.extraHeaders`, and in every `authProfiles` entry. Literals are rejected by pattern
  and by negative control. Fixed headers are pointers too: an upstream that wants a
  constant `x-api-client` today wants a rotated one tomorrow.
- If the upstream's own endpoints disagree about authentication, that is `authProfiles`,
  not a second provider. See [pmfby](usecases/pmfby.md).

## 4. Write the `ProviderCapability` record

- `bindingKey` is `providerId|capabilityCode|action`. All three segments, always.
- **One upstream call?** `method` + `path` + `requestMapping`.
  **Several?** `steps[]` — and then `method`/`path`/`requestMapping` must be absent. A
  `oneOf` enforces this; a record cannot be half of each.
- `timeoutMs` and `retryMax`: set them deliberately. A retry on a non-idempotent POST is a
  duplicate submission, not resilience.

## 5. Decide whether it needs an enricher — and be reluctant

An `enricher` is only for what the Beckn body **cannot** express: private code namespaces
(Agmarknet's `marketcode`), or a lookup against something the adapter owns
(`nearestStation`'s Postgres). It is the one place this design admits Go, so the bar is
high — if a JSONata expression can do it, it is a mapping, not an enricher.

If it does need one, prefer the **object form** so its config and its `env://` pointers
live in the registry with everything else.

## 6. Write the mappings as files

```
registry/mappings/<provider>/<action>[.<step>].<request|response>.jsonata
```

**Lowercase only** — a path differing from the file on disk only by case resolves on a
macOS checkout and 404s on a Linux pod. Step ids are camelCase, so kebab-case the filename:
step `verifyMobile` → `status.verify-mobile.request.jsonata`.

Request mappings are evaluated over `{beckn, _local, steps}`; the single response mapping
sees every step's response under `steps.<id>`.

## 7. Does anything have to survive between requests?

Usually no. If the upstream sends an OTP by SMS — or anything else that forces a human
pause between Beckn actions — then yes, and it is `sessionGrant` on the step that earns the
proof plus `sessionGate` on the binding that requires it. Both keyed on `transaction_id`,
both stored by the generic executor in ONIX's `cache` plugin.

`ttlSeconds` is required and capped at an hour. That is not a formality: the NestJS adapter
this replaces held the same state in module-level `Map`s that nothing ever deleted.

## 8. Validate before you commit

```
python3 suite.py          # positives, negatives, JSONata compile, referential integrity
```

The referential pass checks that every mapping path in every record resolves to a real
file, and re-parses the JSON blocks out of `docs/design/usecases/` — so a record
documented but not shipped, or shipped but not documented, fails the run.

## 9. Add it to the docs

If it fits one of the [four shapes](usecases/README.md), add a row to the table and its
records under *Remaining providers*. Give it its own page only when it introduces a shape
that does not exist yet.
