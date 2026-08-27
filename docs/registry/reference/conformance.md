# Conformance evidence

What has actually been executed against the real schemas, and what it proved.

*[Overview](../01-overview.md) · [Registry schema](../02-registry-schema.md) · [Use cases](../usecases/README.md) · [Open issues](open-issues.md) · [docs home](../README.md)*

---

Not inspection — **executed**. `jsonschema` 4.26 for the schemas, and ONIX's own
`jsonata-go` invoked through `jsonata.OpenLatest()` — the same call `reqmapper.go:153`
makes. Beckn-side validation is against
`protocol-specifications-v2/api/v2.0.0/beckn.yaml` (**4995 lines, authoritative**), never
the 3379-line copy vendored inside ONIX. Network-specs is pinned at **`c56ee68`**.

The tally covers **all four seeded bindings**, not only the weather advisory traced on the
[Mausamgram page](../usecases/mausamgram.md).

> **Aligned to `informationMode`.** Every resource now carries the mode that tells an
> advertisement from an answer: `discover` emits the outcome `@type` with `OnDemand`,
> `select` emits the same `@type` with `Direct`. `capabilityCode` became the outcome type,
> `Capability.outcomeType` was dropped as a permanent duplicate, and `pmfby` split into
> `InsurancePolicy` and `InsuranceClaim`. One thing does **not** work yet: an `OnDemand`
> resource fails its own outcome pack, because the pack's required properties are all
> Direct-only. Until that is conditional upstream, the advert validates against
> `AgricultureCapability` — so the mode also selects the contract (issue 11 of
> [Open issues](open-issues.md)).

> **Realigned to `c56ee68`.** That commit rewrote the shared `AgricultureResource` field
> set every outcome pack composes: `subjectAreas` became **required** `subjectCategories`
> (enum `Crop Livestock Weather Market Scheme Knowledge`), the
> `subjectScope`/`agricultureSubjects` conditional was dropped, flat
> `coverageAreaCodes: ["IN"]` became `coverageAreas: [{codeScheme, areaCode, areaLevel}]`,
> and `KnowledgeResource.resourceType` became required `knowledgeType`. All four
> `responseMapping`s and all three `Capability` records have been rewritten; the numbers
> below are from that rewrite, re-executed.

```
registry schemas + records     3 schemas valid draft-07 | 13 records validated
negative controls (registry)   33/33 rejected (6 on session gate/grant placement)
JSONata                        26/26 mapping files compile and execute
static scan (all 26 mappings)  0 array @type · 0 resources without informationMode
Beckn v2.0.0 + network-specs   4 bindings executed | 10/10 resources conform
negative controls (output)     8/8 rejected
session gate/grant placement   3/3 legal placements accepted
hop ① published catalog        discover PASS · on_discover PASS · advert is OnDemand
referential integrity          71/71  (incl. all 19 records in docs/design/usecases/)
merged Binding schema          4 records validated | 7 negatives | copies agree | no drift
merged ProviderBinding         5 records validated | 8 negatives | copies agree | no drift

==== positive 63/63  |  negative 58/58 correctly rejected ====
```

Four controls on the harness itself:

1. **The harness is checked against known-good input.** All 20 examples shipped inside
   network-specs validate against their own packs under it.
2. **Mappings are executed from the files, not from copies** — and from the *file
   contents*, not the stored path. A path like `mappings/agmarknet/select.request.jsonata`
   is itself syntactically valid JSONata, so compiling the field *value* would have passed
   vacuously once mappings moved out of the row.
3. **All 26 mappings are scanned statically, not just the 8 that execute.** Section D only
   runs the mappings behind the four *seeded* bindings; the other 18 sit behind modelled
   ones and are never executed, so a defect in them would never be validated. Two scans
   close that gap — no array `@type`, and no `resourceAttributes` without an
   `informationMode`. Both are checked against deliberately broken copies: reintroduce one
   array and the run drops to 47/48.
4. **The docs are re-parsed.** The referential pass reads all 19 JSON blocks out of
   `docs/design/usecases/*.md` and validates them against the live schemas, so the pages
   cannot drift from what they document. Break a single mapping path in
   [Use cases](../usecases/README.md) and the run drops to 69/71.

**Registry negative controls, rejected as intended** — 33, in six groups:

| group | rejected |
|---|---|
| credentials | a literal secret where a pointer belongs; `env://lowercase`; `vault://` (not this stack); `apiKeyQuery` with no `paramName` |
| keys and types | a stale two-part key with no action; a bogus action in the key; the base type as a `capabilityCode`; the base type inside a `bindingKey`; the shared field set `AgricultureResource` in either position; a key with no capability half; an uppercase `providerId`; a malformed key; `action: "search"` (v1 leaking into v2); an unknown field |
| mapping paths | an inline expression where a path belongs; an empty `requestMapping`; `..` traversal; an uppercase path; a non-`.jsonata` extension; a path outside `mappings/` |
| session placement | a `sessionGrant` at binding level on a multi-step record; a grant with no `ttlSeconds`; a day-long TTL; a non-camelCase scope; a `ttlSeconds` on a *gate*; a `sessionGate` on a step |
| capability pinning | a `schemaUrl` on `/main/`; one via `/refs/heads/`; one pointing at `profile.json` instead of `attributes.yaml`; a re-added `schemaSha` field |
| found by review | a literal DSN smuggled through `enricher.config` — the one free-form object in the three schemas, and the only way a credential could still reach the database; a `baseTypes` entry with an empty local name |

The three legal placements — grant on a step, grant on a single-call binding, gate on a
binding — are checked as positives in the same run.

**The merged schema is held to the same standard.**
[`Binding.json`](merged-schema.md) — the denormalised alternative, all three entities in
one record — validates as draft-07, its 4 merged records pass, and 7 negatives are
rejected: a literal secret, the base type as a `capabilityCode`, a stale two-part key, a
branch-ref `schemaUrl`, a `..` traversal, an unknown field, and an unmerged `name` left
behind by the rename.

The merge also creates a rule **no JSON Schema can express**: two bindings of the same
provider carry two copies of its auth block, and nothing in draft-07 can compare one
record to another. So the suite groups by `providerId` and `capabilityCode` and compares
every duplicated block field by field — today that is one duplicated capability block,
`WeatherObservation` across `mausamgram` and `imd-city-weather`. The check has its own
control: corrupt one copy of that `schemaUrl` and it fires.

**And a drift guard, because `Binding.json` was generated from the other three.** Nothing
stops the three moving on without it, and validating it against itself would never notice.
So the suite asserts its 22 properties stay the union of theirs — modulo the two renames
and the collapsed `status` — and compares all 19 shared properties structurally, patterns,
enums, bounds and defaults, plus the `Auth` and `Login` definitions. Add a field to
`ProviderCapability.json` and forget `Binding.json`, and the run fails.

**`ProviderBinding.json` — the two-entity merge — is held to the same standard.** Provider
folded onto the call plan, `Capability` left as its own table: **19 properties, 9
required**, one read on `bindingKey`. Its 4 seeded records validate, plus the merged PMFBY
`discover` record — multi-step, two auth profiles, a `sessionGate` and a step-level
`sessionGrant` in one row. Eight negatives are rejected: a literal secret in the merged
auth block, a stale two-part key, the base type as a `capabilityCode`, a trailing-slash
`baseUrl`, a `..` traversal, an unmerged `name`, a `schemaUrl` folded in from the entity
that stayed separate, and a re-added `providerStatus`. Same drift guard as above.

One honest limit: the four **seeded** bindings are four different providers, so the
agreement check has nothing duplicated to compare there — its negative control is what
proves it works. Add a second `mausamgram` binding carrying a stale `baseUrl` and it
fires. The duplication only appears at the 11 **modelled** bindings, where `pmfby` carries
three copies of a 568-byte auth block and `pm-kisan` two.

**Output conformance** — every `resourceAttributes` produced by a `responseMapping`,
validated against its outcome contract in network-specs with the `allOf` chain resolved:

| binding | outcome | resources | at `c56ee68` |
|---|---|---|---|
| `mausamgram\|openagrinet:WeatherObservation\|select` | `WeatherObservation` v0.1 | 5 | PASS — **traced on the [Mausamgram page](../usecases/mausamgram.md)** |
| `agmarknet\|openagrinet:MandiPriceObservation\|select` | `MandiPriceObservation` v0.1 | 2 | PASS |
| `imd-city-weather\|openagrinet:WeatherObservation\|select` | `WeatherObservation` v0.1 | 1 | PASS |
| `hasura-content\|openagrinet:KnowledgeResource\|select` | `KnowledgeResource` v0.1 | 2 | PASS |

Plus, for each: `select.message` → `SelectAction`, `on_select.message` →
`OnSelectAction`, both contexts → `Context`, every `offer.resourceIds` entry resolving to
a real resource, and `context.action` flipped to `on_select`.

**Output negative controls, rejected as intended:** a contract with no `commitments`; a
commitment with no `offer`; `observationType: "Observed"`; a `select` carrying an
`intent`; an observation missing `observationType`; an empty `subjectCategories`
(`minItems: 1`); a `coverageAreas` entry with `areaCode` but no `codeScheme`; and the
published advert relabelled `Direct`, which fails the outcome pack on the four Direct-only
properties it does not have.

**`on_discover` passes too — after a fixture bug was found here.** This check failed
until the published-catalog fixture was corrected. The failure read:

```
catalogs/0/resources/0/resourceAttributes/@type ::
  ['openagrinet:AgricultureCapability', 'openagrinet:WeatherObservationCapability']
  is not of type 'string'
```

It was first recorded as a spec-level conflict (issue 1 of
[Open issues](open-issues.md)). It was not one — the array was **ours**. The fixture had
been written against a pre-`c56ee68` draft and never realigned: alongside the array
`@type` it still carried four field names the pack no longer defines (`subjectAreas`,
`subjectScope`, `coverageAreaCodes`, `interactionTypes`). None of it was caught because
this fixture was only ever validated against Beckn core, which leaves
`resourceAttributes` open.

Emitting the specific type alone is valid against both schemas, so the fixture now does
that, and the suite is green.

---

## Files

```
registry/schemas/Provider.json               entity + auth + login
registry/schemas/Capability.json             network vocabulary + schema pack pin
registry/schemas/ProviderCapability.json     the call plan
registry/schemas/Binding.json                all three merged into one — see
                                             [Merged schema](merged-schema.md)
registry/schemas/ProviderBinding.json        Provider + ProviderCapability merged,
                                             Capability left separate — same page
registry/samples/providers.json              6 seeded BV upstreams (8 are modelled)
registry/samples/capabilities.json           3 capabilities, keyed by outcome type
registry/samples/provider-capabilities.json  4 working `select` bindings (11 are modelled)
registry/samples/bindings.json               the same 4, merged
registry/samples/provider-bindings.json      the same 4, two-entity merge
registry/mappings/<provider>/*.jsonata       26 mapping files: 8 behind the working
                                             bindings, 18 behind the modelled ones

docs/design/README.md                        index
docs/design/01-overview.md                   architecture, actions, both topologies
docs/design/02-registry-schema.md            the three entities
docs/design/03-adding-a-provider.md          checklist
docs/design/usecases/                        11 bindings, 4 shapes, 19 records
docs/design/reference/open-issues.md         asks and open questions
docs/design/reference/merged-schema.md       the denormalised alternative
docs/design/reference/conformance.md         this page
docs/design/diagrams/request-flow.excalidraw the Mausamgram sequence, editable

docs/*.md                                    pre-existing background notes on the
                                             upstreams as they behave today
```

The gap between *seeded* and *modelled* is deliberate: `registry/samples/` holds what the
suite executes end to end, `docs/design/usecases/` holds every binding the design covers.
Both are validated against the same schemas on every run.

Authoritative references this document is validated against:

```
protocol-specifications-v2/api/v2.0.0/beckn.yaml   Beckn core (4995 lines) — NOT the
                                                   3379-line copy vendored in ONIX
network-specs/schema/**/v0.1/attributes.yaml       OpenAgriNet outcome contracts
network-specs/schema/examples/README.md            "catalogs advertise, they do not
                                                   contain the requested value"
beckn-onix/core/module/handler/stdHandler.go       sync proxy path + the NACK trap
beckn-onix/pkg/model/model.go                      StepContext, Route, WithContext
beckn-onix/pkg/plugin/implementation/reqmapper/    the JSONata engine we reuse
```
