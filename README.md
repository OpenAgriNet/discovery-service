# OpenAgriNet Discovery Service

A Go service exposing synchronous `POST /publish` and `POST /discover` that
ingest [Beckn v2.0.0](https://becknprotocol.io) catalogs into PostgreSQL and
serve geo + lexical discovery under 20 ms.

- **Architecture:** `controller → service → repository`. Two ports live in
  `src/domain/`: `CatalogRepository` (write, driven by publish) and
  `SearchRepository` (read, driven by discover). Neither capability package
  imports a driver, so the backend is swappable.
- **Stack:** Go 1.25 · chi v5 · pgx/v5 + sqlc · PostgreSQL 16 + pgvector 0.8 ·
  uber/h3-go v4 · kin-openapi · zap · testify · testcontainers-go.

The design is specified in
[`docs/design/discover-and-publish.md`](docs/design/discover-and-publish.md);
the decisions behind it are recorded in [`docs/adr/`](docs/adr/README.md). Read
the design document before changing anything here — the constraints it states
are binding, not advisory.

## Requirements

Go 1.25 or later, and Docker for the local database and the integration
suites. Nothing else needs installing: every build tool is pinned in
`tools/go.mod` and built into `bin/` on demand.

## Quickstart

```sh
make up        # PostgreSQL 16 + pgvector 0.8, waited on until healthy
make migrate   # apply migrations to DATABASE_URL
make build     # compile every package and link bin/discovery-service
make test      # unit and integration suites
```

`make` on its own lists every target.

To try the service rather than change it, run the whole stack in Docker:

```sh
make run       # PostgreSQL + the service on :8080, migrations applied on boot
make logs      # follow the service's output
make down      # stop everything and discard the volumes
curl localhost:8080/readyz
```

`make up` deliberately stays dependencies-only. The integration suites reach
PostgreSQL through testcontainers and never through Compose, and development
runs the binary from the host — so the service container lives behind the `app`
Compose profile, which `make run` selects.

That stack applies its own migrations (they are compiled into the binary) and
reads the Beckn specification from `tests/testdata/beckn-v2.0.0.yaml`, mounted
at the cache path. The boot logs one warning about the registry fetch it did
not do, which is why it works with no network; set `VALIDATION_SPEC_URL` to
exercise the fetch path instead.

With the stack up, there is a worked catalog and the requests that find it:

```sh
make verify    # publish the sample catalog, assert text, spatial and filter retrieval
make newman    # the same checks through the Postman collection
```

Both assert the exact set of resource ids each request returns, and the cases
are built to disagree with each other — see [`examples/`](examples/README.md)
for what each one pins and why.

## Layout

```
cmd/discovery-service/   entrypoint
src/publish/             SYSTEM 1 — catalog ingest
src/discover/            SYSTEM 2 — search
src/domain/              the contract between them; stdlib + uuid only
src/beckn/               Beckn v2.0.0 wire types
src/indexing/            H3 geospatial covers, embeddings
src/storage/             postgres, memory, and the conformance suite both pass
src/platform/            config, logging, errors, validation, middleware
src/app/                 composition root
config/  migrations/  schemas/  tests/  docs/
tools/                   separate module pinning the build toolchain
```

## Configuration

Four layers, lowest precedence first: `envDefault` struct tags →
`config/common.yaml` → `config/instance.yaml` → process environment.
Environment sits on top because secrets arrive from a secret store and must
beat a file.

Copy `config/instance.yaml.example` to `config/instance.yaml` for deployment
overrides; that file is gitignored. **Secrets — `DATABASE_URL` above all —
belong in neither YAML file.**

The Beckn specification is fetched at boot from `VALIDATION_SPEC_URL` and
cached under `.cache/beckn/`. It is deliberately not committed and not baked
into the image; air-gapped deploys mount a cache file at
`VALIDATION_SPEC_CACHE_PATH`.

## Contributing

Contribution policy and a security disclosure process have not been decided
yet, so this repository deliberately carries no `CONTRIBUTING.md` or
`SECURITY.md` — a placeholder would publish a promise nobody has made. Until
they exist, raise an issue.

## Licence

MIT — see [LICENSE](LICENSE).
