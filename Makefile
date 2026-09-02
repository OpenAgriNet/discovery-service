# OpenAgriNet Discovery Service — build, test and toolchain targets.
#
# Every tool is pinned in tools/go.mod and built into bin/ on demand, so a
# clean checkout needs nothing installed but Go itself.

GO           ?= go
BIN_DIR      := bin
IMAGE        ?= discovery-service:dev
DATABASE_URL ?= postgres://discovery:discovery@localhost:5432/discovery?sslmode=disable

# CI thresholds/pins live here, not duplicated into workflow env blocks — one
# source of truth for both a local `make` run and the GitHub Actions runner.
MIN_COVERAGE      ?= 80
BASE_REF          ?= origin/main
SEVERITY          ?= HIGH,CRITICAL
GOTESTSUM_VERSION := v1.13.0
TRIVY_VERSION     := v0.74.0

# Test targets pin the embedding provider rather than inheriting it.
# Production defaults to noop (A5), so without the pin the whole semantic path
# — query embedding, HNSW, RRF, the dimension guard, the degradation report —
# would go untested from the day semantic search was deferred.
TEST_ENV := EMBEDDING_PROVIDER=hashing

# Coverage instruments these packages regardless of which test binary is
# running. Without it Go instruments only the package under test, and
# tests/acceptance and tests/dbtest are separate packages holding almost no
# statements of their own — their entire job is to drive src/. So the suite that
# exercises the most code would credit none of it: the total read 68.6% against
# a real 88.5%, and src/beckn read 22.2% against a real 81.9%. A number that
# understates the suite is not a conservative estimate, it is an argument for
# writing tests that already exist.
COVERPKG := ./src/...,./cmd/...

GOLANGCI_LINT := $(BIN_DIR)/golangci-lint
GOVULNCHECK   := $(BIN_DIR)/govulncheck
SQLC          := $(BIN_DIR)/sqlc
MIGRATE       := $(BIN_DIR)/migrate
GOTESTSUM     := $(BIN_DIR)/gotestsum
TRIVY         := $(BIN_DIR)/trivy

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
	$(TEST_ENV) $(GO) test -race -covermode=atomic -coverpkg=$(COVERPKG) \
		-coverprofile=coverage.out ./...

## cover-total: the one number — total statement coverage
cover-total: cover
	@$(GO) tool cover -func=coverage.out | tail -1

## cover-report: per-package coverage, thinnest last
cover-report: cover
	@awk -f tools/cover-report.awk coverage.out

## cover-html: annotated source, green covered and red not, in coverage.html
# -o rather than letting `go tool cover` open a browser: this has to work over
# ssh and in CI, where there is no browser to open and the command would hang.
cover-html: cover
	@$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"

## test-ci: run the suites through gotestsum — one line per package, coverage
##          profile written alongside. What run-tests.yml calls; `make test`
##          stays the plain everyday entrypoint.
test-ci: $(GOTESTSUM)
	$(TEST_ENV) $(GOTESTSUM) --format pkgname --format-hide-empty-pkg -- \
		-race -coverprofile=coverage.out -covermode=atomic ./...

## cover-diff: coverage restricted to files changed vs BASE_REF — a PR review
##             needs the diff's number, not the whole repo's. One line, no
##             per-file table: an entry for a file nobody touched answers a
##             question nobody asked.
cover-diff: coverage.out
	@CHANGED=$$(git diff --name-only --diff-filter=ACMR "$(BASE_REF)...HEAD" -- '*.go' | grep -v '_test\.go$$' || true); \
	if [ -z "$$CHANGED" ]; then \
		echo "📊 Coverage — no changed Go files vs $(BASE_REF), nothing to gate" | tee coverage-report.md; \
		exit 0; \
	fi; \
	MODULE=$$($(GO) list -m); \
	echo "$$CHANGED" | awk -v mod="$$MODULE/" -v min="$(MIN_COVERAGE)" ' \
		NR==FNR { want[mod $$0] = 1; next } \
		{ f = $$1; sub(/:.*/, "", f); if (!(f in want)) next; \
		  tot[f] += $$(NF-1); if ($$NF > 0) cov[f] += $$(NF-1) } \
		END { \
			T = 0; C = 0; for (f in tot) { T += tot[f]; C += cov[f] }; \
			if (T == 0) { print "📊 Coverage — changed files carry no coverable statements"; exit } \
			pct = int(C * 100 / T); icon = (pct < min) ? "❌" : "✅"; status = (pct < min) ? "failed" : "passed"; \
			printf "📊 **Coverage (changed files): %d%%** (min %d%%) — %s %s\n", pct, min, icon, status \
		}' - coverage.out > coverage-report.md; \
	cat coverage-report.md; \
	! grep -q '❌' coverage-report.md

## trivy-deps: dependency graph scan (T4), SARIF report. Catches what the
##             image scan structurally cannot — a vulnerable module only the
##             test suite imports, so it's never linked into the binary and
##             never appears in a layer. skip-dirs excludes tools/ (a
##             separate go.mod for build-time tooling): the linter's
##             dependency graph is not the binary's, so it can't fail a
##             release it doesn't ship in.
trivy-deps: $(TRIVY)
	$(TRIVY) fs . --skip-dirs tools --severity $(SEVERITY) --exit-code 0 \
		--format sarif --output trivy-deps.sarif

TRIVY_IMAGE_SCAN = $(TRIVY) image $(IMAGE) --severity $(SEVERITY)

## trivy-image: shipped image scan (T4), SARIF report — reads base layers and
##              the Go build info embedded in the binary, including stdlib,
##              so a Go toolchain CVE shows up here and nowhere else that the
##              dependency scan above cannot see. IMAGE names the ref to scan.
trivy-image: $(TRIVY)
	$(TRIVY_IMAGE_SCAN) --exit-code 0 --format sarif --output trivy-image.sarif

## trivy-release-gate: the same image scan as trivy-image, but exit 1 on a
##                     finding instead of writing a report — the pre-push
##                     release gate build-and-push.yml runs once per
##                     arch-tagged local image, before anything is pushed.
trivy-release-gate: $(TRIVY)
	$(TRIVY_IMAGE_SCAN) --exit-code 1 --format table

## trivy-gate: fail if either SARIF report already produced by a scan step
##             carries a finding. Reads the reports rather than rescanning —
##             two scans of the same thing can disagree, since Trivy refreshes
##             its DB each run, and a gate that rescans can fail on a finding
##             in no uploaded report, the one state nobody can act on. Plain
##             output only — same as a local run sees; the GitHub Actions
##             ::error:: annotation is the caller's concern, not this
##             target's (security.yml's gate step adds it).
trivy-gate:
	@fail=0; \
	for report in trivy-deps.sarif trivy-image.sarif; do \
		count=$$(jq '[.runs[].results[]?] | length' "$$report"); \
		echo "$${report}: $${count} $(SEVERITY)"; \
		if [ "$$count" -gt 0 ]; then \
			jq -r '.runs[].results[]? | "\(.ruleId) \(.message.text)"' "$$report"; \
			fail=1; \
		fi; \
	done; \
	exit $$fail

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

## migrate-down: roll back one migration — today that is the WHOLE schema
# The schema ships as a single migration at version 1 (A21), so `down 1` is
# `down -all`: it drops every table, function and extension the service owns.
# Once a second migration exists this becomes the one-step operation its name
# implies.
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

## audit: check the answers are RIGHT, not merely unchanged. verify and newman
##        assert id sets that were written by watching this service run, so
##        they freeze whatever it did that day; audit recomputes the expected
##        answer from the published catalog instead and compares.
##        `pip install jsonschema pyyaml` to get the schema checks too — it
##        runs without them and says loudly which checks it skipped.
audit:
	python3 examples/audit.py

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
	rm -rf $(BIN_DIR) coverage.out coverage-report.md trivy-deps.sarif trivy-image.sarif

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

# Installed directly rather than through tools/go.mod like the four builds
# above: gotestsum is CI-only (see run-tests.yml), so it doesn't belong in the
# service's or the linter's dependency graph either one.
$(GOTESTSUM):
	@mkdir -p $(BIN_DIR)
	GOBIN=$(abspath $(BIN_DIR)) $(GO) install gotest.tools/gotestsum@$(GOTESTSUM_VERSION)

# The prebuilt release binary, not `go install`: trivy's rpm-db parser needs
# cgo, and its module graph is comparable in size to golangci-lint's for a
# tool nothing here imports — the official install script is what
# aquasecurity itself recommends over building from source for exactly this.
$(TRIVY):
	@mkdir -p $(BIN_DIR)
	curl -sfL https://raw.githubusercontent.com/aquasecurity/trivy/main/contrib/install.sh | \
		sh -s -- -b $(abspath $(BIN_DIR)) $(TRIVY_VERSION)

.PHONY: help build test test-short test-ci cover cover-diff lint fmt sqlc \
	sqlc-verify migrate run logs migrate-down security trivy-deps trivy-image \
	trivy-release-gate trivy-gate docker up down verify newman audit tools clean
