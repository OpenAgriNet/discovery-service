# OpenAgriNet Discovery Service — build, test and toolchain targets.
#
# Every tool is pinned in tools/go.mod and built into bin/ on demand, so a
# clean checkout needs nothing installed but Go itself.

GO           ?= go
BIN_DIR      := bin
IMAGE        ?= discovery-service:dev
DATABASE_URL ?= postgres://discovery:discovery@localhost:5432/discovery?sslmode=disable

# Test targets pin the embedding provider rather than inheriting it.
# Production defaults to noop (A5), so without the pin the whole semantic path
# — query embedding, HNSW, RRF, the dimension guard, the degradation report —
# would go untested from the day semantic search was deferred.
TEST_ENV := EMBEDDING_PROVIDER=hashing

GOLANGCI_LINT := $(BIN_DIR)/golangci-lint
GOVULNCHECK   := $(BIN_DIR)/govulncheck
SQLC          := $(BIN_DIR)/sqlc
MIGRATE       := $(BIN_DIR)/migrate

.DEFAULT_GOAL := help

## help: list the available targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /' | sort

## build: compile every package and link the service binary
# -o with a trailing slash both compiles every package and puts each main in
# bin/. Plain `go build ./...` links a lone main into the working directory,
# which drops a binary in the repository root.
build:
	$(GO) build -trimpath -o $(BIN_DIR)/ ./...

## test: run the unit and integration suites
test:
	$(TEST_ENV) $(GO) test -race ./...

## test-short: run only the suites that need no container
test-short:
	$(TEST_ENV) $(GO) test -race -short ./...

## cover: run the suites and write a coverage profile
cover:
	$(TEST_ENV) $(GO) test -race -coverprofile=coverage.out -covermode=atomic ./...

## lint: vet, format check and static analysis
lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) fmt --diff ./...

## fmt: apply the formatters lint checks for
fmt: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) fmt ./...

## sqlc: regenerate the typed query layer from migrations/ and queries/
sqlc: $(SQLC)
	$(SQLC) generate

## sqlc-verify: fail if the committed query layer is stale
sqlc-verify: $(SQLC)
	$(SQLC) diff

## migrate: apply every pending migration to DATABASE_URL
migrate: $(MIGRATE)
	$(MIGRATE) -path migrations -database "$(DATABASE_URL)" up

## migrate-down: roll back the most recent migration
migrate-down: $(MIGRATE)
	$(MIGRATE) -path migrations -database "$(DATABASE_URL)" down 1

## security: scan the dependency graph for known vulnerabilities (T4)
security: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...

## docker: build the service image
docker:
	docker build -t $(IMAGE) .

## up: start PostgreSQL with pgvector and wait for it to accept connections
up:
	docker compose up -d --wait

## run: build the image and start PostgreSQL plus the service on :8080
##      migrations are embedded and applied on boot; --wait would need a
##      healthcheck the distroless runtime has no shell to run
run:
	docker compose --profile app up -d --build

## logs: follow the service's output
logs:
	docker compose --profile app logs -f discovery-service

## verify: publish the sample catalog and assert text, spatial and filter
##         retrieval against a stack already running via `make run`
verify:
	./examples/verify.sh

## newman: the same checks through the Postman collection, if newman is around
newman:
	npx --yes newman run examples/OpenAgriNet-discovery-service.postman_collection.json

## down: stop the local stack and discard its volumes
##       -v matters: migrations are edited in place during development, and
##       golang-migrate tracks only version NUMBERS — so a volume migrated by
##       an older revision of the same file keeps its old columns forever and
##       fails at the first write instead of at boot
down:
	docker compose --profile app down -v

## tools: build the pinned toolchain into bin/
tools: $(GOLANGCI_LINT) $(GOVULNCHECK) $(SQLC) $(MIGRATE)

## clean: remove build output and coverage profiles
clean:
	rm -rf $(BIN_DIR) coverage.out

$(GOLANGCI_LINT): tools/go.mod tools/go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) -C tools build -o ../$@ github.com/golangci/golangci-lint/v2/cmd/golangci-lint

$(GOVULNCHECK): tools/go.mod tools/go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) -C tools build -o ../$@ golang.org/x/vuln/cmd/govulncheck

$(SQLC): tools/go.mod tools/go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) -C tools build -o ../$@ github.com/sqlc-dev/sqlc/cmd/sqlc

# The postgres build tag is what registers the driver golang-migrate resolves
# DATABASE_URL against; without it the CLI builds fine and then reports every
# migration URL as an unknown scheme.
$(MIGRATE): tools/go.mod tools/go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) -C tools build -tags postgres -o ../$@ github.com/golang-migrate/migrate/v4/cmd/migrate

.PHONY: help build test test-short cover lint fmt sqlc sqlc-verify migrate run logs \
	migrate-down security docker up down verify newman tools clean
