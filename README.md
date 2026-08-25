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
