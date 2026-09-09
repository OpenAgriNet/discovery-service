# Open issues, asks and decisions

What is **not settled**: asks on network-specs governance, and questions this design
leaves open. Items that have since been answered are struck through rather than deleted,
so the reasoning stays readable. This page goes stale fastest — check it against `git log`
before relying on it.

*[Overview](../01-overview.md) · [Registry schema](../02-registry-schema.md) · [Use cases](../usecases/README.md) · [Conformance](conformance.md) · [docs home](../README.md)*

---

## Adopted: `informationMode`

A domain-schemas design note circulated **2026-08** proposes one flag on every resource:

```yaml
informationMode:
  type: string
  enum: [OnDemand, Direct]
```

`OnDemand` describes information a provider *can* supply and requires invoking the
provider; `Direct` contains or directly references the result. The note is explicit about
the consequence: *"When an OnDemand resource is invoked, the Provider normally returns a
Direct resource of the same `@type`"*, which *"avoids separate capability schemas"*.

**BV has adopted it.** It is BV's two-call model restated in one field, so the structure
was already right; what changed is the vocabulary:

| | before | now |
|---|---|---|
| ① `discover` advertises | `openagrinet:WeatherObservationCapability` | `openagrinet:WeatherObservation` + `OnDemand` |
| ② `select` returns | `openagrinet:WeatherObservation` | `openagrinet:WeatherObservation` + `Direct` |

What that cost, concretely:

- **`capabilityCode` is now the outcome type**, and every `bindingKey` carries it. The
  three-part key is unchanged. `Capability.outcomeType` is **gone** — it would have been a
  permanent duplicate of `capabilityCode`.
- **`pmfby` split into two codes.** `status` yields `InsurancePolicy`, `discover` yields
  `InsuranceClaim`; under one `CropInsuranceCapability` that difference had nowhere to
  live. The keys stay unique.
- **Issue 1 is moot.** With no `AgricultureCapability` base to co-name, there is no array
  `@type` left to argue about.
- **`baseTypes[]` now holds `openagrinet:AgricultureResource`** — the shared field set
  packs compose with `allOf`. The note's composition index is explicit that this is
  composition, *"not a parent-child hierarchy"*.

**Two renames are deliberately not done.** The note works in five flat packs —
`GeneralKnowledge`, `WeatherAdvisory`, `WeatherObservation`, `MandiPrice`,
`MandiAdvisory`. Of those, `GeneralKnowledge` and `MandiAdvisory` do not exist at
`c56ee68`, and `MandiPrice` is `MandiPriceObservation` there. Pointing `schemaUrl` at a
pack that does not exist would trade working validation for a name, so BV keeps
`MandiPriceObservation` and `KnowledgeResource` until the packs land. That is a one-line
change per record when they do.

---

## Asks on network-specs

Found by executing the [Mausamgram trace](../usecases/mausamgram.md) against the real
schemas. These are **asks on network-specs governance**, not BV bugs. Re-audited against
network-specs `c56ee68`; two have since been answered upstream. Numbers are stable ids,
not positions — 8, 9 and 10 belong to the table below.

| # | issue | ask |
|---|---|---|
| 1 | **`@type`: array vs string — resolved, and it was not a spec conflict.** Beckn core declares `@type` as `type: string`. network-specs does not constrain it at all: it appears only under `x-jsonld:` as pack metadata, so array, string, unknown string and absent all validate. At `c56ee68`, 5 of the 29 `@type` occurrences in the repo are two-element arrays (4 files; `weather-provider-catalog.json` carries both forms in adjacent resources) — but an example is not a contract — so the array form has no upstream requirement and one upstream prohibition. | Emit the specific type as a **single string**; tolerate an array on the way in, since a BAP may copy an example. Done — this is what cleared the last failing check in [Conformance](conformance.md). Moot under `informationMode`: the base type is no longer co-named at all. |
| 2 | **`provider.url` is not a legal `Provider` field.** Beckn core's `Provider` is `additionalProperties: false`; all three network-specs catalog examples carry `url`. Those catalogs, as published, do not validate. | Drop `url` from the examples, or move it into `resourceAttributes` where extension is legal. |
| 3 | **`WeatherObservation` cannot express a daily min/max.** The `parameter` enum has no qualifier, but every BV weather upstream reports `tmin`/`tmax` and `rhmin`/`rhmax`. **There is no conformant way to say "tomorrow's high is 30.6 and low is 22.1".** the response step of [the Mausamgram trace](../usecases/mausamgram.md) works around it with a private `aggregation` qualifier that validates *only* because the parameter item is left open — no other consumer on the network will understand it. | Promote `aggregation` to a real optional field with a governed enum, or add the four qualified members. **This is the single genuine misfit in the whole trace.** The `informationMode` note appears to concede it — its `WeatherObservation` Direct example uses `"parameter": "MaximumTemperature"`, which is **not** in the enum at `c56ee68`. Worth confirming that is an intended enum extension and not a slip in the example. |
| 4 | **No request-side schema exists.** Every binding puts query parameters into `resourceAttributes`, but the only published schema for that container describes an **advertisement** — as of `c56ee68` `AgricultureCapability` declares only the shared agriculture field set (subject categories, languages, coverage), and has no slot for the requested location, time or value. Request payloads therefore conform to nothing; they validate only because `Attributes` is open. A BAP can send `{"loc": …}` instead of `{"location": …}` and nothing rejects it until the JSONata silently produces an upstream call with a missing parameter. | Publish a request profile per capability — e.g. `WeatherObservationQuery/v0.1` — so `@type` on the way in names a query shape and `@type` on the way out names an observation. |
| 5 | ~~**`interactionTypes` does not determine a Beckn action.**~~ **Answered in `c56ee68`.** The instance field is gone. Interaction is now `interaction_type` inside each pack's `profile.json`, keyed by governed capability type — i.e. declared as discovery metadata, which is what this ask requested. It still does not map onto `select`/`init`/`status`, and BV still decides the action itself via `ProviderCapability.action` (see [Registry schema](../02-registry-schema.md#schema)). | Closed. |
| 6 | **No `Act` capability exists, and no insurance vocabulary at all.** `Act` is in the enum and in no example, so the transactional half of the network is unexercised. A grep for *insurance*, *policy* or *claim* across `schema/` returns nothing. | An `InsuranceStatusCapability` pack; at least one `Act` example. |
| 7 | ~~**`WeatherAdvisory` has no outcome counterpart.**~~ **Answered in `c56ee68`.** `WeatherAdvisory/v0.1` is now a real outcome contract (topics, location, issue/validity times, recommendations, weather basis, source), and `WeatherAdvisoryCapability/v0.1` is its advertisement pack. An advisory advertised at ① now has a conformant place to land at ②. BV seeds neither yet. | Closed. |
| 11 | **`informationMode` has no conditional requirements, so an `OnDemand` resource fails its own pack.** Executed: the note's own `WeatherObservation` OnDemand example, validated against `WeatherObservation/v0.1/attributes.yaml` at `c56ee68`, fails on four required properties — `observationType`, `source`, `location`, `generatedAt`. Every one of them is a property only a **Direct** resource can have. The note says both modes share one pack, but the pack as written describes Direct only. | Make the required properties conditional on the mode — an `if`/`then` on `informationMode`, with the OnDemand branch requiring the `supported*` / `forecastHorizon` / `geographicGranularity` fields the note's own examples use instead. Until then BV validates the advert against `AgricultureCapability` and the outcome against its own pack, so **the mode also selects the contract** — which is exactly the split the proposal set out to remove. |

**Found in re-audits against `c56ee68`, and closed in the same pass. All three were BV's
own, not network-specs asks — recorded because each was a decision, not a typo:**

| # | issue | ask |
|---|---|---|
| 8 | ~~**`schemaUrl` could only point at the artifact that does not validate.**~~ **Fixed.** The pattern was `.+\.json$`, so it could only address `profile.json` — which network-specs' `INDEX.md` defines as discovery, indexing and privacy *hints*. The contract that validates is `attributes.yaml`. | Resolved in favour of validation: the pattern is now `.+/attributes\.yaml$`, and all three `Capability` records point at their outcome pack's `attributes.yaml`. `profile.json` is not referenced by the registry at all; if indexing hints are ever needed they get their own field. |
| 9 | ~~**The four `responseMapping`s emitted a vocabulary network-specs no longer defines.**~~ **Fixed.** `c56ee68` rewrote the shared `AgricultureResource` field set ([Conformance](../reference/conformance.md)); every resource BV produced went non-conformant — 0 of 10 — while the mappings still compiled and executed, so **nothing failed loudly at runtime**. | All four rewritten and all three capabilities re-pinned to `c56ee68`; [Conformance](../reference/conformance.md) is back to 10/10. The durable fix is the second one: the conformance suite now reads its mappings from `registry/samples/provider-capabilities.json` instead of from copies, so registry and tests cannot diverge again. A commit-pinned `schemaUrl` still has to be re-pinned deliberately — nothing detects an upstream rewrite for us. |
| 10 | ~~**Four `responseMapping`s still emitted an array `@type`.**~~ **Fixed.** `soil-health-card/status`, `pmfby/status`, `pmfby/discover` and `oan-vector/select` each emitted `["openagrinet:AgricultureResource", "<specific type>"]` — the exact form issue 1 resolved against, which fails Beckn core. The suite did not catch it because it only *executes* the 8 mappings behind the four **seeded** bindings; these four sit behind **modelled** ones and were compiled but never validated. | All four now emit the specific type as a single string, and the base entry is dropped — `AgricultureResource` is the shared field set every outcome pack composes, not an outcome contract, so naming it identifies nothing. The durable fix is the control: the suite now statically scans all 26 mappings for an array `@type` ([Conformance](conformance.md)), so the 18 that never execute are no longer unchecked. |

**One Beckn core constraint with consequences for BV:**

**`status` is not a free-standing query — it presumes a contract.** `StatusAction` and
`CancelAction` are the only actions that narrow `Contract`, and both bolt
`required: [id]` onto it. But a `contract.id` is something the network issues, on
`on_confirm` after `select → init → confirm`. *"What is the status of my PMFBY policy?"*
is not a transaction the farmer conducted over this network — the policy pre-exists on
PMFBY's systems, and there is no prior `confirm` to have minted an id.

| option | cost |
|---|---|
| **Use `select`, not `status`** — treat it as a lookup, farmer id in `resourceAttributes` | none; reuses the [Mausamgram](../usecases/mausamgram.md) pattern verbatim |
| Synthesise a contract id at the adapter | dishonest; the id refers to nothing the network can resolve, and breaks correlation |
| Model enrolment as a real `confirm` | large; needs PMFBY to be a network participant, not just an upstream API |

**For BV as it stands, the first is the honest one.**

**One ONIX gap:** response steps are not pluggable — `mapResponse` needs a
`plugins.responseSteps` list added to `initSteps`, roughly twenty lines. Worth
upstreaming — see [Topology B](../01-overview.md#topology-b--adapter-on-both-sides).

**Fixed in this repo, not upstream issues:** all four `responseMapping`s were originally
emitting `message.catalog.providers[]` — the Beckn **1.x `on_search`** shape — and now
emit the correct v2 `OnSelectAction` container. `observationType: "Observed"` was a
genuine bug (the enum is `Observation`/`Forecast`), caught by
[Conformance](conformance.md) and now guarded by a negative control.

---

## Open questions

1. **Sunbird RC version.** `_osConfig` support and the search filter grammar differ
   across releases. Confirm `uniqueIndexFields`, `indexFields` and `roles` against the
   deployed build (see [Search API](../02-registry-schema.md#search-api)).
2. **Where enricher names are registered**, and what happens when a binding names one
   that does not exist. Startup validation is the obvious answer.
3. **Onboarding.** Who writes registry records, and where the two referential checks in
   [Search API](../02-registry-schema.md#search-api) run.
4. **When does sync stop being enough?** IMD gets a 30s timeout and 3 retries, and a
   synchronous `/select` holds a connection for all of it. The callback path to `bapUri`
   does not exist yet. Worth deciding now whether that is a v1 constraint or a v1 bug.
5. ~~**When does a capability become transactional?**~~ **Answered — by modelling the
   full provider set.** This question assumed `bindingKey` could omit the action because
   every capability answers exactly one. PMFBY answers `discover`, `init` and `status`
   against one provider+capability pair, and PM-Kisan answers `init` and `status`, so the
   assumption was already false. The action is now a mandatory third segment of the key
   (see [Schema](../02-registry-schema.md#schema)).

   Worth recording *why* it mattered: **the failure mode was an overwrite, not an error.**
   Tested under the two-part key, two records differing only in `action` both validated
   and carried the identical `bindingKey`; with `uniqueIndexFields: ["bindingKey"]` the
   second write replaced the first, silently. JSON Schema cannot detect a duplicate across
   records — only forbid the shape that allows one.

6. **Which `select` semantics does the network commit to?** This design reads `on_select`
   as *"a zero-cost quote whose payload is the data."* Consistent with the spec's wording,
   but it is an interpretation every BAP must share. Worth pinning in network governance
   rather than leaving to each adapter.

7. **`schemaUrl` is required but never verified.** Tested: a `Capability` naming a pack
   that does not exist upstream — `.../schema/CropInsuranceStatus/v0.1/attributes.yaml` —
   validates cleanly, because the pattern constrains only the *shape* of the URL. The
   field cannot be omitted either. So onboarding a provider ahead of the network
   vocabulary means seeding a well-formed URL that 404s, and nothing notices until
   something tries to fetch it. Either resolve it at write time, or allow null and define
   what an unpinned capability means.

8. **PM-Kisan and PMFBY use actions this design argues against.** Three pages disagree.
   [Overview](../01-overview.md) establishes that `discover` is **CN → DS only**, and the
   table above concludes that `status` presumes a `contract.id` no BV flow ever mints — so
   both point at `select`. The modelled records use neither: `pm-kisan` binds `init` and
   `status`, `soil-health-card` binds `status`, and `pmfby` binds `init`, `status` and a
   CN → PN `discover` for the claim ([pmfby](../usecases/pmfby.md)).

   The records are not wrong by accident. The OTP arrives out of band, so the flow needs a
   human pause in the middle, and a single `select` cannot express *prove something, then
   ask*. But nothing has reconciled that need with the two constraints, so today the design
   says one thing and the records do another. Either the constraints have exceptions worth
   writing down, or six `bindingKey`s and their mapping filenames need renaming — the
   session mechanism itself does not change either way. Settle it before onboarding,
   because the action is the third segment of the key.
