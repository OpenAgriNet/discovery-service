# OpenAgriNet registry

The registry stores **participants, the capabilities they can answer, and the binding between
them**. The binding is the call plan: given a provider and a capability, how do you reach them.

Nothing else lives here — no catalogs, no resources, no search index.

**Who reads it:** the adapter. **Who does not:** discovery-service, which answers `/discover`
from its own catalog store.

**Everyone on the network is a participant.** One that has declared capabilities is a provider;
one that only consumes them is a consumer. That is a `roles` value, not a different entity.

## The four documents

| | |
|---|---|
| **[usecases.md](usecases.md)** | **Start here.** Six farmer questions traced end to end — the records you need, every call on the wire, and the real payloads. |
| **[schemas.md](schemas.md)** | The three entities field by field, the five rules JSON Schema cannot express, what a `select` must supply, and the [known gaps](schemas.md#known-gaps). |
| **[api.md](api.md)** | The registry's own REST API — create, search, update, and why there is no delete. |
| **[examples.md](examples.md)** | The thirteen records to seed, as write bodies. Copy-paste ready. |

[`schemas/`](schemas) holds the machine-readable draft-07 files, which are the contract.
[`verify/`](verify) holds the checkers that keep these documents true.

## Also here

`archive/` is the BV Beckn adapter's own design set — a **different system**, copied verbatim
for interop context and kept diffable against its source. Nothing in it is binding on anything
in this folder.

`docs/design/discover-and-publish.md` is the binding plan for the discovery service and wins over
anything written here.
