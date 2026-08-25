# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src

# Dependency layer first: go.mod and go.sum change far less often than source,
# so the module download stays cached across ordinary code edits. go.su[m] is a
# glob rather than a plain name because a module with no dependencies yet has
# no go.sum, and a missing literal COPY source is a build failure.
COPY go.mod go.su[m] ./
RUN go mod download

COPY cmd/ ./cmd/
COPY src/ ./src/

# -trimpath strips the build machine's filesystem paths from the binary, so two
# machines building one commit produce the same bytes. CGO is off because the
# runtime stage has no libc to link against.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
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
