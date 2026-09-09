# syntax=docker/dockerfile:1

# DHI (Docker Hardened Images) requires `docker login dhi.io` first — a Docker
# subscription credential this build cannot supply. Both stages default to it
# because it's what CI and every shipped image use, but a local build without
# that subscription can override both build-args with public equivalents:
#
#   docker build \
#     --build-arg BUILD_IMAGE=golang:1.27-alpine \
#     --build-arg RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot \
#     --build-arg INSTALL_BUILD_TOOLS=true \
#     -t discovery-service:dev .
#
# INSTALL_BUILD_TOOLS=true is required with the public build image: DHI's
# -dev variant already bundles gcc/g++/musl-dev/make (see below), the public
# golang:alpine image does not, and this build needs a C toolchain regardless
# of which base runs it.
ARG BUILD_IMAGE=dhi.io/golang:1.27-alpine-dev
ARG RUNTIME_IMAGE=dhi.io/static:20260611-musl-alpine
ARG INSTALL_BUILD_TOOLS=false

# Tag verified against
# github.com/docker-hardened-images/catalog image/golang/alpine-3.24/1.27-dev.yaml;
# that -dev variant already bundles gcc/g++/musl-dev/make, which is why the
# apk add below is conditional rather than unconditional — github.com/uber/h3-go/v4
# is a cgo binding around Uber's C library (there is no pure-Go H3, and H3 is
# how every geometry in the index is covered), so this build needs a C
# toolchain from somewhere.
FROM ${BUILD_IMAGE} AS build
WORKDIR /src

ARG INSTALL_BUILD_TOOLS
RUN if [ "$INSTALL_BUILD_TOOLS" = "true" ]; then apk add --no-cache gcc g++ musl-dev make; fi

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

# The build stamp, declared here rather than at the top of the stage so a new
# release does not invalidate the go mod download layer above it.
#
# There is no .git in the build context — nothing COPYs it — so neither
# `git describe` nor `git rev-parse` can run in here, AND the Go toolchain writes
# no vcs.revision / vcs.time / vcs.modified into the build info either. That
# second half is the part that was missed: until 2026-09-10 only VERSION crossed,
# so every image built here reported build.commit=unknown, build.tree_state=unknown
# and build.date=1970-01-01T00:00:00Z — the three attributes that say WHICH build
# is running, absent from exactly the binaries you cannot identify by looking at
# your own checkout.
#
# All four now arrive as arguments. `make docker` passes them; a bare
# `docker build .` gets the defaults below and the binary says dev/unknown, which
# is exactly what it is. Empty is not a default here — an empty -X value is what
# src/platform/buildinfo's linkerStamp reads as "nothing supplied".
ARG VERSION=dev
ARG COMMIT=
ARG BUILD_DATE=
ARG TREE_STATE=

# -trimpath strips the build machine's filesystem paths from the binary, so two
# machines building one commit produce the same bytes.
#
# cgo is ON and the link is STATIC, which is not a contradiction: musl links
# fully static cleanly, so the result still needs no libc at runtime and the
# dhi/static base below stays correct. Dropping -static here does not fail the
# build — it fails the first container start, with a missing loader and no Go
# stack to say why.
#
# The four -X targets are the flag strings this file and the Makefile must agree
# on (OP5; docs/design/opentelemetry.md, "Build identity", says why none of them
# can be avoided). Go silently ignores an -X naming a symbol that does not exist,
# so renaming that package would leave a green build shipping `dev` and an
# unknown commit — which is why tests/architecture asserts the two files stamp
# the SAME SET and that every symbol in it is declared, rather than trusting them
# to.
RUN CGO_ENABLED=1 go build -trimpath \
        -ldflags="-s -w -extldflags '-static' \
            -X github.com/OpenAgriNet/discovery-service/src/platform/buildinfo.version=${VERSION} \
            -X github.com/OpenAgriNet/discovery-service/src/platform/buildinfo.commit=${COMMIT} \
            -X github.com/OpenAgriNet/discovery-service/src/platform/buildinfo.buildDate=${BUILD_DATE} \
            -X github.com/OpenAgriNet/discovery-service/src/platform/buildinfo.treeState=${TREE_STATE}" \
        -o /out/discovery-service ./cmd/discovery-service

# dhi/static musl-alpine variant, verified against
# image/static/alpine-3.24/static-musl.yaml — runs as uid 65532 / user
# "nonroot" by default already, same as the distroless image it replaces, and
# matches the musl static link above. distroless/static-nonroot (the public
# override) uses the same uid 65532 convention, so USER below needs no
# override of its own.
FROM ${RUNTIME_IMAGE} AS runtime
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
