# syntax=docker/dockerfile:1

# DHI (Docker Hardened Images) requires `docker login dhi.io` first — a Docker
# subscription credential this build cannot supply. Tag verified against
# github.com/docker-hardened-images/catalog image/golang/alpine-3.24/1.27-dev.yaml;
# that -dev variant already bundles gcc/g++/musl-dev/make, which is why there's
# no `apk add` here — github.com/uber/h3-go/v4 is a cgo binding around Uber's C
# library (there is no pure-Go H3, and H3 is how every geometry in the index is
# covered), so it needs the toolchain the -dev variant already carries.
FROM dhi.io/golang:1.27-alpine-dev AS build
WORKDIR /src

# Dependency layer first: go.mod and go.sum change far less often than source,
# so the module download stays cached across ordinary code edits. go.su[m] is a
# glob rather than a plain name because a module with no dependencies yet has
# no go.sum, and a missing literal COPY source is a build failure.
COPY go.mod go.su[m] ./
RUN go mod download

COPY cmd/ ./cmd/
COPY src/ ./src/

# migrations/ is a Go PACKAGE, not data: migrations/embed.go carries the .sql
# pairs into the binary through //go:embed, and src/storage/postgres/migrate.go
# imports it. Leaving it out does not produce an image that cannot migrate — it
# produces one that does not compile.
COPY migrations/ ./migrations/

# -trimpath strips the build machine's filesystem paths from the binary, so two
# machines building one commit produce the same bytes.
#
# cgo is ON and the link is STATIC, which is not a contradiction: musl links
# fully static cleanly, so the result still needs no libc at runtime and the
# dhi/static base below stays correct. Dropping -static here does not fail the
# build — it fails the first container start, with a missing loader and no Go
# stack to say why.
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -extldflags '-static'" \
        -o /out/discovery-service ./cmd/discovery-service

# dhi/static musl-alpine variant, verified against
# image/static/alpine-3.24/static-musl.yaml — runs as uid 65532 / user
# "nonroot" by default already, same as the distroless image it replaces, and
# matches the musl static link above.
FROM dhi.io/static:20260611-musl-alpine AS runtime
WORKDIR /app

COPY --from=build /out/discovery-service /app/discovery-service
COPY schemas/    /app/schemas/
COPY migrations/ /app/migrations/
COPY config/common.yaml /app/config/common.yaml

# beckn.yaml is deliberately not baked in. It is fetched at boot from
# VALIDATION_SPEC_URL; a copy inside the image is a second source of truth that
# ages with the image. Air-gapped deploys mount a cache file at
# VALIDATION_SPEC_CACHE_PATH instead.

# Static hardened images carry no /etc/passwd entries, so the user is numeric,
# not a name — 65532 is the same nonroot uid distroless used.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/discovery-service"]
