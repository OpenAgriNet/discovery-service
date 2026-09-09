# Quickstart

Get the service running locally, publish a catalog, and find it again. About
five minutes, most of it the first image build.

You need Docker with Compose v2. To *try* the service that is all — the image
carries its own Go toolchain. To *change* it you also want Go 1.25; the linters
and the migration tool are pinned in `tools/` and built into `bin/` on demand.

## 1. Start the stack

```
make run
```

That builds the image and starts PostgreSQL (with pgvector) plus the service on
`:8080`. Migrations are compiled into the binary and applied on boot, so there
is no separate migrate step and no sidecar.

There is one `docker-compose.yml` and three profiles. The `make` targets are
thin wrappers, and the raw commands are worth knowing because they are what you
reach for when you want something in between:

| Target | Is | Starts |
|---|---|---|
| `make up` | `docker compose up -d --wait` | PostgreSQL only |
| `make run` | `docker compose --profile app up -d --build` | + the service on `:8080` |
| `make telemetry` | `OTEL_EXPORTER=otlp docker compose --profile app --profile telemetry up -d --build` | + an OTel collector on `:8889` |
| `make down` | `docker compose --profile app down -v` | — stops and discards volumes |

`OTEL_EXPORTER` is the only environment difference between the second row and
the third; the profile adds the collector container. Until 2026-09-09 the third
row was a separate `docker-compose.telemetry.yml` overlay, which no longer
exists.

There is no Compose healthcheck to wait on — the runtime stage is
`distroless/static` and has no shell to run one — so give it a few seconds and
then ask the service directly:

```
curl -s localhost:8080/healthz     # the process is up
curl -s localhost:8080/readyz      # it can also reach PostgreSQL
```

`make logs` follows the output. One warning about a spec fetch it could not do
is expected and is not a failure — see [The Beckn spec, offline](#the-beckn-spec-offline).

> `make up` starts **PostgreSQL only**. That is the default Compose profile and
> the day-to-day development setup: you run the binary from the host against
> it. `make run` adds the service container via `--profile app`, which is what
> you want when you are trying the service rather than changing it. See
> [Building and testing](#building-and-testing) for that loop.

## 2. Publish a catalog

The service has four routes. Two are probes; these two are the API:

| Route | Does |
|---|---|
| `POST /publish` | A provider publishes or patches a catalog |
| `POST /discover` | A consumer searches across published catalogs |

`examples/01-publish-weather-advisory.json` is one catalog from a fictional
KSNDMC with three weather-advisory resources, built so each differs along every
axis discovery can filter on.

```
curl -s localhost:8080/publish \
  -H 'Content-Type: application/json' \
  -d @examples/01-publish-weather-advisory.json | jq '.message.results'
```

```json
[
  {
    "catalogId": "cat-ksndmc-weather-advisory",
    "status": "ACCEPTED",
    "stats": { "itemCount": 3, "providerCount": 1, "categoryCount": 1 }
  }
]
```

## 3. Find it

```
curl -s localhost:8080/discover \
  -H 'Content-Type: application/json' \
  -d @examples/02-discover-text-search.json \
  | jq -c '[.message.catalogs[].resources[].id]'
```

```json
["res-wx-point-dharwad","res-wx-village-belagavi"]
```

Two of the three, because the third is the statewide alert and this query is
about irrigation and spraying. The response is a full envelope — the catalogs
come back under `message.catalogs`, each with its provider, offers and the
matching resources.

That request is a full Beckn envelope; the part that does the work is small:

```json
{
  "context": {
    "domain": "agriculture",
    "action": "discover",
    "version": "2.0.0",
    "transactionId": "a1000000-0000-4000-8000-000000000002",
    "messageId": "b1000000-0000-4000-8000-000000000002",
    "timestamp": "2026-08-27T06:30:00Z"
  },
  "message": {
    "intent": {
      "textSearch": "irrigation spraying advisory"
    }
  }
}
```

`examples/` carries eighteen of these — text, spatial, attribute filters and
the combinations, plus two that are supposed to be rejected. Swap the `-d @…`
filename to try another:

| Try | Shows |
|---|---|
| `04-discover-spatial-dwithin.json` | find within a radius of a point |
| `06-discover-filter-granularity.json` | filter on a published attribute |
| `10-discover-text-and-geo.json` | text and geometry together |
| `08-discover-invalid-jsonpath.json` | a refusal, with the fault in the NACK |

A refusal comes back as HTTP 400 and a NACK that says which field was wrong and
why, rather than an empty page that looks like "no matches":

```json
{
  "message": {
    "status": "NACK",
    "messageId": "b1000000-0000-4000-8000-000000000008",
    "error": {
      "code": "SCH_INVALID_JSONPATH",
      "message": "the expression has no ? (...) filter, so it selects rather than tests …",
      "details": { "path": "$.message.intent.filters.expression" }
    }
  }
}
```

## 4. Check the answers

```
make verify     # publish, then assert every retrieval path
make audit      # check the answers are RIGHT, not merely unchanged
```

The two ask different questions and you want both. `verify` asserts hard-coded
id sets, which catches regressions but froze whatever the service did the day
they were written. `audit` recomputes what each intent *should* match directly
from the published catalog — with point-in-polygon and haversine code written
from scratch rather than the service's H3 covering — and compares.

`make audit` wants `jsonschema` and `pyyaml` for its schema checks
(`pip install jsonschema pyyaml`). Without them it still runs and names the
checks it skipped.

`make newman` runs the same assertions through the Postman collection.

[`examples/README.md`](../examples/README.md) explains the fixture: what the
three resources are, why the statewide alert carries no geometry, and why each
case is built to *exclude* something another includes.

## 5. Watch it emit telemetry

```
make telemetry          # the same stack plus an OTel collector
make verify             # give it some traffic to describe
make telemetry-metrics  # the derived streams
make telemetry-logs     # the collector's view of the spans
```

`make telemetry-metrics` waits, deliberately — a stream appears only on the
connector's flush interval and only after a request of that shape, so a bare
scrape straight after `make telemetry` reports zeros that are not the answer.

The local stack has no trace backend, so the collector logs the spans rather
than shipping them. The discover call count and latency at `localhost:8889` are
derived from those spans by the spanmetrics connector and are instrumented
nowhere in Go. [`telemetry.md`](telemetry.md) covers what the service emits and
what it does not.

## 6. Stop

```
make down            # or telemetry-down, if you started that profile
```

Both discard volumes, and the `-v` matters: migrations are edited in place
during development and golang-migrate tracks only version *numbers*, so a
volume migrated by an older revision of the same file keeps its old columns
forever and fails at the first write instead of at boot.

## Building and testing

Everything above runs the service from an image. This is the loop for changing
it. `make help` lists every target, one line each.

### Build

```
make build          # compile every package into bin/, including the binary
make docker         # build the service image instead
```

`make build` needs Go 1.25. Nothing else needs installing: `golangci-lint`,
`migrate` and `sqlc` are pinned in a separate `tools/go.mod` and built into
`bin/` the first time a target wants them.

### Test

```
make test           # go test -race ./... — the gate
make test-short     # only the suites that need no container
make lint           # vet, format check and static analysis; must be 0 issues
```

`make test` pins `EMBEDDING_PROVIDER=hashing` rather than inheriting it.
Production defaults to `noop`, so without the pin the entire semantic path —
query embedding, HNSW, RRF, the dimension guard — would go untested.

Integration suites reach PostgreSQL through **testcontainers**, not through
Compose, so they start and discard their own database and do not care whether
`make up` is running. Docker has to be up; nothing else does.

Coverage: `make cover` writes a profile, then `make cover-total` for the one
number, `make cover-report` per package, or `make cover-html` for annotated
source.

### Run it from the host

The development loop is the binary on the host against Compose's PostgreSQL —
no image rebuild between edits.

```
make up             # PostgreSQL only. NOT make run: the app container
                    # would hold :8080 and the host binary cannot bind it
make build

APP_NETWORK_ID=local-network \
DATABASE_AUTO_MIGRATE=true \
DATABASE_URL='postgres://discovery:discovery@localhost:5432/discovery?sslmode=disable' \
  ./bin/discovery-service
```

Those three are all it needs. `APP_NETWORK_ID` is required and the boot refuses
without it; `DATABASE_AUTO_MIGRATE` saves a separate `make migrate`. The Beckn
spec is fetched on first boot and cached under `.cache/beckn/`, so this works
from a clean checkout with no mount and no further configuration — see below.

If you already have the stack up, `docker compose --profile app stop
discovery-service` frees `:8080` and leaves PostgreSQL alone.

## Notes

### The Beckn spec, offline

The service loads the Beckn v2.0.0 specification before it serves and refuses
to start without it. The check is unconditional — turning L1 validation off
does not remove the requirement.

It is fetched from `VALIDATION_SPEC_URL`, which now defaults to

```
https://raw.githubusercontent.com/beckn/protocol-specifications-v2/refs/tags/core-v2.0.0-lts/api/v2.0.0/beckn.yaml
```

and cached under `VALIDATION_SPEC_CACHE_PATH` (`.cache/beckn/beckn.yaml`). That
is why running the binary from the host needs no spec configuration at all: it
fetches once and reads the cache afterwards.

A **tag**, not a branch, which is the whole reason there can be a default. A
branch would let an upstream merge change the validator under a running
deployment without a deploy; `refs/tags/core-v2.0.0-lts` cannot move. A network
that trusts a different document still overrides it.

Compose additionally mounts `tests/testdata/beckn-v2.0.0.yaml` straight into
the cache path. That file is byte-identical to the tag, so the two paths agree
— and the mount is what lets the container stack come up with no network at
all. If the fetch fails, the boot logs one loud warning and falls back to the
cache; that warning is accurate and is not a failure.

### Rate limiting is effectively off locally

The default is 20 rps / 40 burst, which a collection run or any scripted sweep
trips immediately. Compose raises it to a ceiling nothing local will reach.

It is not disabled, because it cannot be: the config layer requires `rps > 0`
and `burst >= rps`, since a bucket smaller than one second's refill can never
fill. Deployments leave both unset and get the real defaults back.

This matters when a harness misreads a refusal: a 429 NACK carries no
`catalogs`, so anything reading `message.catalogs` defensively turns it into an
empty result and reports "no matches" for what was actually a refused request.

### Credentials

`discovery:discovery` appears in `docker-compose.yml` and in the Makefile's
default DSN, and the database it opens holds whatever you published five
minutes ago. A deployment overrides `DATABASE_URL` from the environment — which
is why the environment layer sits on top of both YAML files, and why that
string appears in neither of them.

## Where next

- [`publish-and-discover.md`](publish-and-discover.md) — what the two APIs are and how they work
- [`telemetry.md`](telemetry.md) — traces, metrics and logs
- [`adr/`](adr/) — why the significant technical choices went the way they did
- `make help` — every target, one line each
