# BV Beckn Adapter — design docs

> **Imported reference. Not binding on this service.**
>
> Copied verbatim on 2026-08-27 from
> `OpenAgriNetLegacy/BharatVistaar/Beckn/docs/design` (not a git repository, so
> there is no commit to cite — the newest file was written 2026-08-27). These
> pages describe a **different system**: the BV adapter, which is Beckn ONIX
> customised against a Sunbird RC registry on Postgres.
>
> They are here because discovery-service and that adapter meet on the same
> network and have to agree about what `discover` and `publish` mean. Read them
> for that: what the registry holds, and what an adapter expects a DS to answer.
> Nothing in this directory constrains discovery-service. Its own binding spec is
> [`docs/design/discover-and-publish.md`](../design/discover-and-publish.md), and
> where the two disagree that one wins — as does `beckn.yaml` over both.
>
> One thing only was edited in the copy: the five **Background** links below
> pointed one level up in the source repository, and now point at
> [`background/`](background/), where those notes were brought along so the set
> has no dead links. Everything else is as it was written — including
> `reference/open-issues.md`, which its own page warns goes stale fastest, so
> check it against the source before acting on it.

The adapter answers one question at runtime: *I have this **provider** and this
**capability** — how do I call them?* The registry holds the answer, the adapter is
**Beckn ONIX** customised, and the registry is **Sunbird RC on Postgres**.

---

## Start here

| | |
|---|---|
| **[1. Overview](01-overview.md)** | How it works, which action is the second call, and the two deployment topologies. |
| **[2. Registry schema](02-registry-schema.md)** | The three entities — `Provider`, `Capability`, `ProviderCapability` — with one worked example. Read once. |
| **[3. Adding a provider](03-adding-a-provider.md)** | The checklist. Open this one when you have actual work to do. |

## Use cases

Eleven bindings across eight providers, but only **four call shapes**. Each page is one
provider: its registry records, and how a farmer's question becomes an upstream call.

| Shape | Provider | What it introduces |
|---|---|---|
| simple | **[mausamgram](usecases/mausamgram.md)** | one action, one call — traced end to end with real payloads |
| enriched | **[imd-city-weather](usecases/imd-city-weather.md)** | `enricher` object form: config and an `env://` DSN in the registry |
| multi-step | **[pm-kisan](usecases/pm-kisan.md)** | `steps[]`, later steps reading `steps.<id>` |
| gated multi-step | **[pmfby](usecases/pmfby.md)** | `sessionGate` / `sessionGrant` across three actions |

**[→ All eleven bindings](usecases/README.md)**, including the four providers that add no
new shape.

## Reference

| | |
|---|---|
| **[Open issues](reference/open-issues.md)** | Asks on network-specs, and questions this design leaves open. Goes stale fastest — check against `git log`. |
| **[Merged schema](reference/merged-schema.md)** | The denormalised alternative: all three entities in one record, one read instead of two. Built and validated; what it costs and why the three still stand. |
| **[Conformance](reference/conformance.md)** | The executed evidence: schema validation, JSONata, Beckn v2.0.0 and network-specs conformance, referential integrity. Plus the file inventory. |

## Background

Pre-existing notes on the upstreams as they behave **today**, before the adapter:
[`PROVIDERS.md`](background/PROVIDERS.md) · [`NETWORKS.md`](background/NETWORKS.md) · [`IMD.md`](background/IMD.md) ·
[`MANDI_PRICE_FLOW.md`](background/MANDI_PRICE_FLOW.md) · [`PMFBY_FLOW.md`](background/PMFBY_FLOW.md)

---

> These pages replace the single `REGISTRY-SCHEMA.md`. Nothing was dropped: the registry
> records that used to sit in its appendix now live beside the flow that uses them, and
> the conformance suite reads them from `docs/design/usecases/`, so the two cannot drift.
