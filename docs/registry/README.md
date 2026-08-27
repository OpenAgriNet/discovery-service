# Registry

The registry holds **how to reach a provider**. It answers exactly one question at
runtime: *I have this provider and this capability — how do I call them?* Nothing else
lives here. It is not read by discovery-service; the adapter and the experience layer
read it.

## Read this

**[OpenAgriNet registry — v1](oan-v1.md)** — the schema, the thirteen records to seed,
and one end-to-end execution from farmer to provider. Scoped to the four v1 categories:
Weather, Mandi prices, Schemes, Crop & Pest.

That is the whole of what v1 needs. If you read one file in this directory, read that one.

## Everything else is [`imported/`](imported/README.md)

A verbatim copy of another team's design — the **BV Beckn adapter** and its Sunbird RC
registry. A **different system**, on the same network, copied so the two can agree about
what `discover` and `publish` mean.

**It is binding on nothing here.** It describes eight providers, eleven bindings and four
call shapes; v1 has five providers, five bindings and one call shape. Where it and
`oan-v1.md` disagree, it is describing BV and `oan-v1.md` is describing us. Where either
disagrees with [`docs/design/discover-and-publish.md`](../design/discover-and-publish.md),
the plan wins — as does `beckn.yaml` over all three.

Go there for the *why* behind something `oan-v1.md` states:

| Question | Page |
|---|---|
| Why is the schema shaped this way? | [`imported/02-registry-schema.md`](imported/02-registry-schema.md) |
| What does a full call look like, with real payloads? | [`imported/usecases/mausamgram.md`](imported/usecases/mausamgram.md) |
| What do the upstream APIs actually do today? | [`imported/background/PROVIDERS.md`](imported/background/PROVIDERS.md) |
| What did they leave unresolved? | [`imported/reference/open-issues.md`](imported/reference/open-issues.md) |

`imported/README.md` is their own index, and carries the provenance: what was copied,
when, and the four places this copy has diverged from its source.

## Why the split is a directory and not a paragraph

The two sets have different authority and different lifetimes. `oan-v1.md` is ours to
change; `imported/` is someone else's, and its value is that it can still be diffed
against the source it came from — one `diff -r` against their tree, with nothing of ours
mixed in to filter out. Folding v1 into those pages would have bought a shorter directory
listing and cost that reconciliation permanently.
