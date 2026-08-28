# OpenAgriNet registry

The registry stores **participants, the schemas that describe a capability, and the binding
between them** — the call plan for reaching a participant, plus how to authenticate to it.

Everyone on the network is a participant; one that has declared capabilities is a provider.
That is a `roles` value, not a different entity.

Nothing else. No catalogs, no resources, no search index.

**Who reads it:** the adapter, or the adopter's experience layer.
**Who does not:** discovery-service — it answers `/discover` from its own catalog store.

## Read these three, in order

| | Read | For |
|---|---|---|
| 1 | **[registry.md](registry.md)** | How it fits, the three schemas field by field, examples, and the create / search / update APIs — there is no delete |
| 2 | **[examples.md](examples.md)** | The thirteen records to seed |
| 3 | **[usecases.md](usecases.md)** | The four v1 categories end to end, with payloads |

Machine-readable schemas, which are the contract: [`schemas/`](schemas/) —
`Participant.json`, `SchemaRegistry.json`, `ProviderSchema.json` (draft-07, with RC
`_osConfig`). `registry.md` describes them; it deliberately keeps no second copy.
[`verify/`](verify/README.md) re-runs the claims those pages make.

Then **[dpg-fit.md](dpg-fit.md)** — whether the responses those bindings produce satisfy the
OAN domain packs. Three of five do not, and that is the largest open item in this folder.

Those four pages are self-contained. A reviewer needs no other file.

## Not part of the review

[`archive/`](archive/README.md) is another team's design documents — the **BV Beckn
adapter** — kept verbatim for provenance. It describes a **different system** and is binding
on nothing here.

Where any of this disagrees with
[`docs/design/discover-and-publish.md`](../design/discover-and-publish.md), that plan wins —
as does `beckn.yaml` over all of it.
