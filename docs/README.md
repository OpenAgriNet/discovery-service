# Documentation

Four short documents describe what this service is and how to use it. Two
directories hold the machine-readable artefacts. One directory holds the
binding specification, which is long on purpose and is not the place to start.

## Start here

| | |
|---|---|
| [**quickstart.md**](quickstart.md) | Run the whole stack with Docker Compose, publish a catalog, discover it back. No Go toolchain, no repo build |
| [**publish-and-discover.md**](publish-and-discover.md) | What the two endpoints do, how a request becomes an answer, and the configuration that changes it — with worked requests and real responses |
| [**telemetry.md**](telemetry.md) | The one span per request, the metrics derived from it, the log line beside it, and the attribute registry all three read from. Every payload captured from a running stack |
| [**registry.md**](registry.md) | The three-table capability registry the adapters around this service read: entities, seed records, and six farmer questions end to end |

## Reference

| | |
|---|---|
| [**api/**](api/README.md) | `openapi.yaml` for all three network surfaces, and a getting-started Postman collection |
| [**adr/**](adr/README.md) | Sixteen Architecture Decision Records — what was rejected and the property that disqualified it |
| [**design/**](design/) | The binding specification. See below |

## The specification

[`design/implementation-plan.md`](design/implementation-plan.md) is **binding**:
26 dependency-ordered tasks, 35 acceptance scenarios, the amendments, and the
reasoning behind every schema decision. It is ~5,500 lines and it is written to
be worked through a task at a time, not read start to finish.

The four documents above are a *reading* of the system as it actually runs;
where one of them and the plan disagree, the plan wins and the document is
wrong. They are kept separate deliberately: a newcomer needs eighty lines about
`/discover`, and a task needs the DDL.

Beside it in `design/`:

| | |
|---|---|
| `implementation-prompts.md` | The per-task driver prompt and the task checklist |
| `opentelemetry.md` | The span shape as an interop contract, and its companion `telemetry-seam.md` — where the attribute registry lives in code |
| `telemetry-examples.md`, `telemetry-examples/` | One transaction's OTLP payload across this service and beckn-onix. Binding on nothing; kept because it is the only record here of what the other repo actually emits |
| `metric-code-registry-proposal.md` | Addressed **outward**, to whoever owns the OAN metrics registry. Twelve proposed codes and seven questions. Binding on nothing; Task 24 is blocked until they answer |
| `registry/schemas/` | The three draft-07 files, **which are the registry's contract**. When they and `registry.md` disagree, the JSON wins |

## Elsewhere in the repo

| | |
|---|---|
| [`../README.md`](../README.md) | Build, test and configure |
| [`../CLAUDE.md`](../CLAUDE.md) | The working agreement and the code layout |
| [`../examples/README.md`](../examples/README.md) | The numbered sample requests, what each one pins, and the three checks that run them |
| `../tests/testdata/beckn-v2.0.0.yaml` | The protocol source of truth. Every request and response is validated against it at runtime |
