# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src

# A C toolchain, because one dependency needs one: github.com/uber/h3-go/v4 is
# a cgo binding around Uber's C library, and under CGO_ENABLED=0 its build
# constraints exclude every file in the package. There is no pure-Go H3, and H3
# is how every geometry in the index is covered.
RUN apk add --no-cache gcc musl-dev

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
# distroless/static base below stays correct. Dropping -static here does not
# fail the build — it fails the first container start, with a missing loader
# and no Go stack to say why.
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -extldflags '-static'" \
        -o /out/discovery-service ./cmd/discovery-service

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

COPY --from=build /out/discovery-service /app/discovery-service
COPY schemas/    /app/schemas/
COPY migrations/ /app/migrations/
COPY config/common.yaml /app/config/common.yaml

# beckn.yaml is deliberately not baked in. It is fetched at boot from
# VALIDATION_SPEC_URL; a copy inside the image is a second source of truth that
# ages with the image. Air-gapped deploys mount a cache file at
# VALIDATION_SPEC_CACHE_PATH instead.

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/discovery-service"]
