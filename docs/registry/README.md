# OpenAgriNet registry

The registry stores **providers, capabilities, and the binding between them** — the call
plan for reaching a provider. Plus the key material a call needs: the provider's signing
public key, and (only where the adapter's environment cannot hold it) the upstream API key.

Nothing else. No catalogs, no resources, no search index.

**Who reads it:** the adapter, or the adopter's experience layer.
**Who does not:** discovery-service — it answers `/discover` from its own catalog store.

## Read these three, in order

| | Read | For |
|---|---|---|
| 1 | **[registry.md](registry.md)** | How it fits, **the three JSON Schemas**, examples, and the create / search / update / delete APIs |
| 2 | **[examples.md](examples.md)** | The thirteen records to seed |
| 3 | **[usecases.md](usecases.md)** | All five v1 use cases end to end, with payloads |

Machine-readable schemas: [`schemas/`](schemas/) — `Provider.json`, `Capability.json`,
`ProviderCapability.json` (draft-07, with RC `_osConfig`).

Those three pages are self-contained. A reviewer needs no other file.

## Not part of the review set

[`archive/`](archive/README.md) is another team's design set — the **BV Beckn adapter** —
kept verbatim for provenance. It describes a **different system** and is binding on nothing
here.

Where any of this disagrees with
[`docs/design/discover-and-publish.md`](../design/discover-and-publish.md), that plan wins —
as does `beckn.yaml` over all of it.
