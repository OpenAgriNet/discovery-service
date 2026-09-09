# The merged schema

Two denormalised alternatives to the three-schema design, both built and validated,
neither adopted: **`Binding.json`** folds all three entities into one record, and
**`ProviderBinding.json`** folds only the two that a request actually reads. Both resolve
a call in **one read**.

*[Registry schema](../02-registry-schema.md) · [Conformance](conformance.md) · [Open issues](open-issues.md) · [docs home](../README.md)*

---

## Why

Sunbird RC has no joins. The three-schema design therefore resolves a call in two reads:

```
ProviderCapability  by bindingKey   →  the call plan
Provider            by providerId   →  baseUrl + auth
```

`Capability` is not read at request time at all — `schemaUrl` is a validation and
onboarding concern — so the second read is the only one the merge removes.

`registry/schemas/Binding.json` collapses all three into one record per binding:
**22 properties, 11 required**, keyed on `bindingKey` exactly as before.

## The field map

| from | fields | in the merged record |
|---|---|---|
| `Provider` | `providerId` `baseUrl` `auth` `authProfiles` | unchanged |
| | `name` | **renamed `providerName`** |
| `Capability` | `capabilityCode` `schemaUrl` `baseTypes` | unchanged |
| | `name` | **renamed `capabilityName`** |
| `ProviderCapability` | everything | unchanged |
| all three | `status` | **collapsed to one**, binding-level |

Two renames because `name` meant two different things. Every pattern, enum, bound,
default, `if`/`then` and `oneOf` carried over verbatim — including the `$ref` that points
`steps[].sessionGrant` at the binding-level definition, which had to be re-pointed at
`#/definitions/Binding/properties/sessionGrant`.

Confirmed by execution, not inspection — a merged multi-step PMFBY record with
`authProfiles`, a `sessionGate` and a step-level `sessionGrant` validates, and all four
illegal shapes are still rejected:

```
multi-step merged record (steps + gate + grant)      PASS
multi-step + method (oneOf must reject)              REJECTED
grant at binding level on multi-step (must reject)   REJECTED
step grant with no ttlSeconds (must reject)          REJECTED
gate on a step (must reject)                         REJECTED
```

## What it costs

**Duplication.** Every binding carries its own copy of its provider's `baseUrl` and `auth`,
and of its capability's `schemaUrl`. Across the 11 modelled bindings that is 3 extra
provider copies and 4 extra capability copies. The distribution is what matters:

```
pmfby              3 bindings   ← 568-byte provider block: authProfiles + login + secrets
pm-kisan           2 bindings
the other six      1 each       ← no duplication at all
```

The provider with the most bindings is the one with the most complex auth, because its
endpoints do not agree with each other. So the copies land exactly where they hurt most.

**Three operations become N writes, none of them atomic:**

| operation | three schemas | merged |
|---|---|---|
| rotate a provider credential pointer | 1 write | 3 writes for PMFBY |
| re-pin a capability to a new commit | 1 write | 2 writes for `WeatherObservation` |
| deactivate a provider | 1 write | 1 write per binding |

Miss one and the failure is silent and partial — for PMFBY, `discover` authenticating
against a stale `env://` while `init` and `status` do not, on the flow that is gated by
OTP. The re-pin case is not hypothetical: it already went wrong once at `c56ee68`, where
all 10 resources went non-conformant while every mapping still compiled and executed
(issue 9 of [Open issues](open-issues.md)).

**A rule JSON Schema cannot express.** Draft-07 cannot compare one record to another, so
nothing in the schema can require that two copies of a provider's auth block agree. That
check runs in [the suite](conformance.md) instead, section H — grouped by `providerId` and
by `capabilityCode`, every duplicated block compared field by field. It has its own
negative control: corrupt one copy of the `WeatherObservation` `schemaUrl` and the check
fires.

## The two-entity variant — `ProviderBinding.json`

The three-way merge is the wrong question, because `Capability` is not read at request
time. The merge that actually removes a read is **`Provider` + `ProviderCapability`**,
leaving `Capability` as its own table. `registry/schemas/ProviderBinding.json` is that:
**19 properties, 9 required**, `Capability` untouched.

It is strictly better than the three-way merge — same one read, one fewer duplicated
field, and re-pinning a capability stays a single write. Measured across the 11 modelled
bindings, this is the whole cost:

```
provider           bindings   provider block   duplicated
pmfby                     3            568 B       1136 B   <- 3 writes to rotate
pm-kisan                  2            162 B        162 B   <- 2 writes to rotate
the other six             1 each                      0 B

total duplication  1298 B          providers needing >1 write: 2 of 8
```

So **six of eight providers duplicate nothing at all**, and the two that do are the reason
to think twice: `pmfby`'s 568-byte block is the only one carrying `authProfiles` + `login`
+ `secrets`, because its endpoints disagree with each other. Rotating its credential
pointer becomes three non-atomic writes; miss one and `discover` authenticates against a
stale `env://` while `init` and `status` do not — silently, on the OTP-gated flow.

## Recommendation

**Keep the three, and preload `Provider` at boot.** The whole table is 8 rows and 2426
bytes, so loading it once and refreshing on a TTL gives the same single read at request
time, with no duplicated credentials and no multi-write rotation:

```
merge        1 read  +  1298 B duplicated  +  multi-write rotation for 2 of 8 providers
preload      1 read  +     0 B duplicated  +  1 write to rotate, always
```

```
boot         all Provider rows            8 rows, 2426 B, once
per request  ProviderCapability by key    one exact-match read
```

Six of eight providers duplicate nothing, so the merge takes on the anomaly class to fix a
problem two records have.

**The fair counter-argument.** The preload is *code* — a map, a TTL, a refresh, a
cold-start path. The merge is *data*. If you would rather not own that code, merging is
defensible; it is roughly twenty lines against a rule draft-07 can never enforce. And with
the merge, *"which providers serve this capability?"* returns `baseUrl` and auth in the
same search, with no second lookup — though under the preload that is a search plus an
in-memory map hit, so it is close to a wash.

The no-join constraint is real. It forces one of two things: denormalise, or hold the
other side in memory. Denormalising is right when the other table is too large to hold or
changes too fast to cache. `Provider` is 8 rows and 2426 bytes, changing on the order of
weeks — smaller than one HTTP header block. At 100 providers it is ~30 KB; at 1000, ~300
KB. There is no join to avoid here, only a map lookup.

**If you want the merge anyway**, the schema and records are ready and the cutover is
mechanical: rewrite the 19 records in [Use cases](../usecases/README.md), fold sections A,
B and G of the suite onto the one schema, rewrite the field tables in
[Registry schema](../02-registry-schema.md), and add the cross-record agreement check to
the onboarding path so it runs on writes and not only in tests.

## Files

```
registry/schemas/Binding.json             572 lines, 22 properties, 11 required
registry/samples/bindings.json            the 4 seeded bindings, all three merged
registry/schemas/ProviderBinding.json     551 lines, 19 properties,  9 required
registry/samples/provider-bindings.json   the same 4, Provider + ProviderCapability only
```

Neither is adopted. Both carry a **drift guard** in the suite, because both were derived
from the live schemas: each asserts its property set stays the union of its sources and
compares every shared property structurally, so a field added to `ProviderCapability.json`
and forgotten here fails the run rather than rotting quietly.

Generated from the three existing schemas and sample files rather than retyped, so the
patterns and records cannot have drifted in transcription.
