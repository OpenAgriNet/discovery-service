# Registry

**The registry stores three things: providers, capabilities, and the mapping between
them.** That mapping is the call plan — given a provider and a capability, how do you
actually reach them. It holds nothing else: no catalogs, no resources, no search index,
no participant identity.

**Who reads it.** The adapter, or the adopter's experience layer. **Not**
discovery-service — that answers `/discover` from its own catalog store and never opens
the registry.

## Start here

| Read | For |
|---|---|
| **[1. registry.md](registry.md)** | **The main document.** Architecture and the two deployment topologies, the schema, and the API spec. Read it once, in order. |
| **[2. examples.md](examples.md)** | The thirteen records to seed, in write form. Open it when you are seeding or reviewing a record. |
| **[3. usecases.md](usecases.md)** | One farmer's question traced end to end with real payloads. Open it when something is not behaving and you need to see the shape at each hop. |

`registry.md` is self-contained for understanding the design; the other two are what you
work from once you do.

## Reference — [`imported/`](imported/README.md)

A verbatim copy of another team's design: the **BV Beckn adapter** and its Sunbird RC
registry. A **different system** on the same network, copied so the two can agree about
what `discover` and `publish` mean. **Binding on nothing here.** It covers eight
providers, eleven bindings and four call shapes; we have five, five and one.

Go there for the reasoning behind a decision this set only states:
[schema rationale](imported/02-registry-schema.md) ·
[a full call with payloads](imported/usecases/mausamgram.md) ·
[what the upstreams do today](imported/background/PROVIDERS.md) ·
[what they left open](imported/reference/open-issues.md).

Where it and these pages disagree, it is describing BV and these are describing us. Where
either disagrees with [`docs/design/discover-and-publish.md`](../design/discover-and-publish.md),
the plan wins — as does `beckn.yaml` over all three.
