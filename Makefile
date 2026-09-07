# OpenAgriNet Discovery Service — build, test and toolchain targets.
#
# Every tool is pinned in tools/go.mod and built into bin/ on demand, so a
# clean checkout needs nothing installed but Go itself.

# Single source of truth for ci.yml and ci-release.yml: every CI step is a
# one-line `make <target>` call, so a red check reproduces locally by running
# the command the log shows. What is left inline in a workflow is GitHub
# context — `${{ }}` expressions, $GITHUB_STEP_SUMMARY writes, `uses:` actions
# — which has no meaning outside a runner and so cannot live here.

GO           ?= go
BIN_DIR      := bin
IMAGE_NAME   ?= discovery-service
IMAGE        ?= $(IMAGE_NAME):dev
DATABASE_URL ?= postgres://discovery:discovery@localhost:5432/discovery?sslmode=disable

# CI thresholds/pins live here, not duplicated into workflow env blocks — one
# source of truth for both a local `make` run and the GitHub Actions runner.
MIN_COVERAGE       ?= 80
BASE_REF           ?= origin/main
SEVERITY           ?= HIGH,CRITICAL
GOTESTSUM_VERSION  := v1.13.0
TRIVY_VERSION      := v0.74.0
ACTIONLINT_VERSION := v1.7.12

# The reports the scans write, the comment renders and the gate reads — named
# once so the three can never disagree about which files are in play.
#
# Derived from one boolean rather than overridden as a list, because a fork PR
# cannot scan the image: the Dockerfile's base images are dhi.io/*, which 401
# on an anonymous pull, and a fork gets an empty string for every secret. So
# ci.yml sets SCAN_IMAGE=false there and the comment and the gate both narrow
# together — spelling the list out in the workflow instead would put the
# default in two places, and the workflow's copy is the one that rots.
SCAN_IMAGE    ?= true
SARIF_REPORTS ?= trivy-deps.sarif $(if $(filter true,$(SCAN_IMAGE)),trivy-image.sarif)

# HTML comment markers. find-comment matches on these to update its comment in
# place rather than posting a new one each run, and the target that writes the
# report is the one that must emit the marker — a workflow step splicing it in
# afterwards is a second place for the string to live, and it drifted once
# already.
COVERAGE_MARKER := <!-- coverage-report -->
SEC_MARKER      := <!-- sec-scan -->

# Only meaningful inside a workflow; a local run gets a placeholder rather
# than a broken link.
RUN_URL ?= $(if $(GITHUB_RUN_ID),$(GITHUB_SERVER_URL)/$(GITHUB_REPOSITORY)/actions/runs/$(GITHUB_RUN_ID),local run)

# The arch of the machine running make, so neither a local run nor the release
# matrix has to pass it: each arch is built on a runner of that arch, natively,
# never under QEMU. Only ever a tag suffix — `docker build` on a native runner
# already produces that arch.
ARCH ?= $(shell uname -m | sed -e 's/^x86_64$$/amd64/' -e 's/^aarch64$$/arm64/')

# The version a tag publishes under. git describe, not GITHUB_REF_NAME: it is
# the same answer on a runner and on a workstation, and it renders an untagged
# commit as v0.0.1-rc1-3-gabc1234 instead of a branch name that would then be
# pushed as an image tag. Needs fetch-depth: 0 in CI to see the tag objects.
VERSION ?= $(shell git describe --tags --always --dirty)

RELEASE_IMAGE = $(IMAGE_NAME):$(VERSION)-$(ARCH)

# Where a tag push publishes. GHCR only, and unconditionally: it is the one
# registry this project actually uses, and a switchboard for three others that
# were never configured is not flexibility, it is four ways for a release to
# quietly push nothing. Adding a registry back is one entry here and one login
# step in ci-release.yml.
#
# GHCR image refs must be lowercase and GITHUB_REPOSITORY_OWNER preserves the
# owner's real case (OpenAgriNet), hence the tr. Deriving the owner rather than
# writing it out means a fork publishes to its own namespace.
OWNER ?= $(shell printf '%s' '$(GITHUB_REPOSITORY_OWNER)' | tr '[:upper:]' '[:lower:]')

IMAGE_REPOS = ghcr.io/$(OWNER)/$(IMAGE_NAME)

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
ACTIONLINT    := $(BIN_DIR)/actionlint

# From GOROOT, not PATH: `go` is always resolvable here (every other target
# needs it) and gofmt sits next to it, so this works where only the toolchain's
# bin dir is on PATH. Expanded at recipe time, hence the `$$`.
GOFMT = $$($(GO) env GOROOT)/bin/gofmt

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
##          profile written alongside. What ci.yml calls; `make test` stays
##          the plain everyday entrypoint.
test-ci: $(GOTESTSUM)
	$(TEST_ENV) $(GOTESTSUM) --format pkgname --format-hide-empty-pkg -- \
		-race -coverprofile=coverage.out -covermode=atomic \
		-coverpkg=$(COVERPKG) ./...

## cover-diff: coverage restricted to files changed vs BASE_REF — a PR review
##             needs the diff's number, not the whole repo's. On failure,
##             names the changed files dragging the number down (worst
##             first) so "what broke" is answered in the same place as
##             "did it break" — on a pass, still just the one line.
#
# Writes coverage-report.md on EVERY exit path, including its own error paths,
# so the workflow can `cat` it unconditionally with no existence guard and no
# fallback text of its own. The marker is written here too, not spliced in by
# a later workflow step: find-comment matches on it, so a second place to
# spell it is a second place for it to drift.
coverage.out:
	$(MAKE) cover

# One line, one place, so a pass and a failure can't disagree about their
# shape. $$1 is the ✅/❌ status, $$2 the sentence after the em dash.
define COVER_REPORT
report() { \
	printf '%s\n📊 **Test Coverage: %s** — %s\n' "$(COVERAGE_MARKER)" "$$1" "$$2" \
		> coverage-report.md; \
	cat coverage-report.md; \
}
endef

cover-diff: coverage.out
	@$(COVER_REPORT); \
	if ! git rev-parse --verify --quiet "$(BASE_REF)" >/dev/null; then \
		report "⚠️ Unavailable" "cannot resolve BASE_REF=$(BASE_REF), so the changed-file set is unknown"; \
		echo "::error::cover-diff: cannot resolve BASE_REF=$(BASE_REF)" >&2; \
		exit 1; \
	fi; \
	if ! CHANGED=$$(git diff --name-only --diff-filter=ACMR "$(BASE_REF)...HEAD" -- '*.go'); then \
		report "⚠️ Unavailable" "git diff against $(BASE_REF) failed, so the changed-file set is unknown"; \
		echo "::error::cover-diff: git diff against $(BASE_REF) failed" >&2; \
		exit 1; \
	fi; \
	CHANGED=$$(printf '%s\n' "$$CHANGED" | grep -v '_test\.go$$' || true); \
	if [ -z "$$CHANGED" ]; then \
		report "✅ Passed" "not applicable, no changed Go files vs $(BASE_REF)"; \
		exit 0; \
	fi; \
	MODULE=$$($(GO) list -m); \
	RESULT=$$(echo "$$CHANGED" | awk -v mod="$$MODULE/" -v min="$(MIN_COVERAGE)" ' \
		NR==FNR { want[mod $$0] = 1; next } \
		{ f = $$1; sub(/:.*/, "", f); if (!(f in want)) next; \
		  tot[f] += $$(NF-1); if ($$NF > 0) cov[f] += $$(NF-1) } \
		END { \
			T = 0; C = 0; \
			for (f in tot) { \
				T += tot[f]; C += cov[f]; \
				p = int(cov[f] * 100 / tot[f]); \
				disp = f; sub("^" mod, "", disp); \
				if (p < min) print "FILE\t" p "\t" disp; \
			} \
			if (T == 0) { print "EMPTY"; exit } \
			print "TOTAL\t" int(C * 100 / T) \
		}' - coverage.out); \
	if echo "$$RESULT" | grep -q '^EMPTY$$'; then \
		report "✅ Passed" "not applicable, changed files carry no coverable statements"; \
		exit 0; \
	fi; \
	PCT=$$(echo "$$RESULT" | awk -F'\t' '$$1=="TOTAL"{print $$2}'); \
	if [ "$$PCT" -ge "$(MIN_COVERAGE)" ]; then \
		report "✅ Passed" "$${PCT}% of changed lines covered, min $(MIN_COVERAGE)%"; \
		exit 0; \
	fi; \
	report "❌ Failed" "$${PCT}% of changed lines covered, min $(MIN_COVERAGE)%"; \
	BELOW=$$(echo "$$RESULT" | awk -F'\t' '$$1=="FILE"{printf "%s\t%s\n",$$2,$$3}' | sort -n); \
	TOTAL_BELOW=$$(echo "$$BELOW" | wc -l); \
	{ \
		echo; \
		echo "| File | Coverage |"; \
		echo "|---|---|"; \
		echo "$$BELOW" | head -15 | awk -F'\t' '{printf "| `%s` | %s%% |\n", $$2, $$1}'; \
		[ "$$TOTAL_BELOW" -gt 15 ] && echo "| … | $$((TOTAL_BELOW - 15)) more file(s) below $(MIN_COVERAGE)% |"; \
		true; \
	} | tee -a coverage-report.md; \
	echo "::error::coverage is below the minimum — see the per-file table above"; \
	exit 1

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
##                     release gate image-build runs once per arch, on the
##                     local image, before anything is pushed anywhere.
trivy-release-gate: $(TRIVY)
	$(TRIVY_IMAGE_SCAN) --exit-code 1 --format table

## trivy-report: render every SARIF report as ONE PR comment, trivy-report.md
# One comment covering both scans, not one comment each: the two scans run in
# the same job now, and two bot comments per PR was the noise this is meant to
# cut. A missing report is written into the comment as missing rather than
# skipped — trivy-gate fails on it, and the comment has to agree with the gate.
#
# Renders from the same SARIF_REPORTS list trivy-gate reads, so the comment and
# the gate can never disagree about what was scanned.
#
# The jq program lives in tools/trivy-comment.jq rather than inline: as a file
# it is lintable (`jq -n --arg severity "" -f tools/trivy-comment.jq`),
# diffable, and free of Makefile `$$`/backslash escaping. It was pasted twice
# into security.yml, and one copy's jq-version bug took the whole job down.
trivy-report:
	@{ \
		echo "$(SEC_MARKER)"; \
		echo "## 🛡️ Trivy security scan ($(SEVERITY))"; \
		echo "[View full run]($(RUN_URL))"; \
		for report in $(SARIF_REPORTS); do \
			case "$$report" in \
				trivy-deps.sarif)  title="Go dependencies";; \
				trivy-image.sarif) title="Container image";; \
				*)                 title="$$report";; \
			esac; \
			echo; echo "### $$title"; echo; \
			if [ -s "$$report" ]; then \
				jq -r --arg severity "$(SEVERITY)" -f tools/trivy-comment.jq "$$report"; \
			else \
				echo "⚠️ No report — the scan did not produce $$report."; \
			fi; \
		done; \
	} > trivy-report.md
	@echo "wrote trivy-report.md"

## trivy-gate: fail if any SARIF report carries a finding, or is missing
# Reads the reports the scans already produced rather than scanning a third and
# fourth time — two scans of the same thing can disagree, since Trivy refreshes
# its DB each run, and a gate that rescans can fail on a finding in no uploaded
# report, the one state nobody can act on.
#
# A missing or unparsable report is a FAILURE, not a pass: a scan that silently
# wrote nothing must never turn the gate into a green no-op — that is the one
# case a security gate must not be green for.
#
# The ::error:: annotation is emitted here rather than by the caller so the
# workflow step stays a bare `make trivy-gate`. Locally it is one extra line of
# output; in CI it is what puts the failure on the PR's Files-changed view.
trivy-gate:
	@fail=0; \
	for report in $(SARIF_REPORTS); do \
		if [ ! -s "$$report" ]; then \
			echo "$$report: MISSING — no scan produced it"; \
			fail=1; continue; \
		fi; \
		count=$$(jq '[.runs[].results[]?] | length' "$$report" 2>/dev/null); \
		if [ -z "$$count" ]; then \
			echo "$$report: UNREADABLE — not valid SARIF"; \
			fail=1; continue; \
		fi; \
		echo "$$report: $$count $(SEVERITY)"; \
		if [ "$$count" -gt 0 ]; then \
			jq -r '.runs[].results[]? | "\(.ruleId) \(.message.text)"' "$$report"; \
			fail=1; \
		fi; \
	done; \
	[ "$$fail" -eq 0 ] || \
		echo "::error::Trivy findings at $(SEVERITY), or a missing report — see the log above"; \
	exit $$fail

## lint: vet, format check and static analysis
lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) fmt --diff ./...

## fmt: apply the formatters lint checks for
fmt: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) fmt ./...

## lint-actions: validate the workflows and composite actions
# Not a CI check on purpose — the pre-commit hook is the gate, so a bad
# `${{ }}` expression or a `needs:` pointing at a nonexistent job is caught
# before the commit exists rather than after a push. Run whole-repo rather than
# per-file: actionlint resolves `needs:` across a workflow's jobs and checks
# `uses: ./.github/actions/...` against the action on disk, so a single file in
# isolation is not enough to judge either.
lint-actions: $(ACTIONLINT)
	$(ACTIONLINT)

## lint-staged: the pre-commit lints, staged files only. What the hook runs.
# Scope is what can pass today, so it does not become something people
# reflexively `--no-verify` past:
#
#   workflows   actionlint, when a workflow or composite action is staged.
#   formatting  gofmt over staged .go files.
#
# Deliberately NOT the full `make lint` and NOT the test suite: golangci-lint
# over the whole module and `go test -race ./...` are both minutes, and a
# pre-commit hook has to stay in seconds. CI is where those belong, and both
# are gates there.
#
# Reads the working tree, not the staged blob. A file staged clean but dirty in
# the working copy is reported here; that is the conservative direction, and it
# avoids checking the index out to a temp dir on every commit.
lint-staged:
	@STAGED=$$(git diff --cached --name-only --diff-filter=ACMR); \
	if [ -z "$$STAGED" ]; then \
		echo "lint-staged: nothing staged"; \
		exit 0; \
	fi; \
	fail=0; \
	if printf '%s\n' "$$STAGED" | grep -qE '^\.github/(workflows/.*\.ya?ml|actions/.*/action\.ya?ml)$$'; then \
		echo "==> lint-actions (staged workflow or action change)"; \
		$(MAKE) --no-print-directory lint-actions || fail=1; \
	fi; \
	GOFILES=$$(printf '%s\n' "$$STAGED" | grep '\.go$$' || true); \
	if [ -n "$$GOFILES" ]; then \
		echo "==> gofmt (staged Go files)"; \
		UNFMT=$$(printf '%s\n' "$$GOFILES" | xargs $(GOFMT) -l); \
		if [ -n "$$UNFMT" ]; then \
			echo "not gofmt-clean:"; \
			printf '  %s\n' $$UNFMT; \
			echo "run \`make fmt\`, then stage the result"; \
			fail=1; \
		fi; \
	fi; \
	if [ "$$fail" -ne 0 ]; then \
		echo; \
		echo "pre-commit checks failed — commit aborted"; \
		exit 1; \
	fi; \
	echo "lint-staged: ok"

## hooks: point git at the repo's versioned hooks (run once per clone)
# core.hooksPath rather than copying into .git/hooks: the hook stays in the
# repo, under review, and a change to it reaches everyone on their next pull
# instead of only the people who remember to re-copy it.
hooks:
	git config core.hooksPath .githooks
	@echo "core.hooksPath -> .githooks, running: $$(ls .githooks | tr '\n' ' ')"

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

## image-build: build this arch's release image locally and gate it on Trivy
# Built and loaded locally, NOT pushed: Trivy then scans the exact bytes that
# are about to ship, before they are tagged for or pushed to any registry. One
# scan covers every registry, because it is one image.
#
# Native, never QEMU — each arch builds on a runner of that arch, so a plain
# `docker build` already produces the right one and ARCH is only the tag suffix.
image-build:
	$(MAKE) docker IMAGE=$(RELEASE_IMAGE)
	$(MAKE) trivy-release-gate IMAGE=$(RELEASE_IMAGE)

## image-push: push the gated local image to every enabled registry (ARCH)
# Re-tags the already-scanned local image per registry and pushes. No rebuild
# and no re-scan, so what is pushed is byte-identical to what image-build
# gated. One arch-suffixed tag each; nothing binds the plain version tag until
# image-publish has every arch.
image-push: require-image-repos
	@for repo in $(IMAGE_REPOS); do \
		dest="$$repo:$(VERSION)-$(ARCH)"; \
		echo "==> $$dest"; \
		docker tag $(RELEASE_IMAGE) "$$dest"; \
		docker push "$$dest"; \
	done

## image-publish: stitch the arch tags into one multi-arch tag per registry
# The only step that creates the tag users actually pull. imagetools create
# makes one manifest list from the arch-specific images image-push already
# pushed, which is what lets `docker pull` resolve the right arch by itself.
#
# :latest moves only for a plain release. Any pre-release renders with a `-`
# (v0.1.1-rc3), and so does git describe on an untagged commit
# (v0.0.1-rc1-3-gabc1234) — neither is what someone who asked for no tag at all
# should get, so the single `*-*` case covers both.
image-publish: require-image-repos
	@for repo in $(IMAGE_REPOS); do \
		tags="-t $$repo:$(VERSION)"; \
		case "$(VERSION)" in \
			*-*) echo "$(VERSION) is not a plain release — not moving :latest";; \
			*)   tags="$$tags -t $$repo:latest";; \
		esac; \
		docker buildx imagetools create $$tags \
			$$(for a in $(RELEASE_ARCHES); do echo "$$repo:$(VERSION)-$$a"; done); \
		docker buildx imagetools inspect "$$repo:$(VERSION)"; \
	done

# The arches image-publish expects image-push to have produced. Named here so
# adding one is a single edit shared with the workflow's build matrix.
RELEASE_ARCHES ?= amd64 arm64

# Split out so both push targets fail the same way, naming the thing to set,
# instead of pushing to a path that is a bare registry and a slash.
#
# OWNER is the only thing that can be empty: it comes from
# GITHUB_REPOSITORY_OWNER, which a runner always sets and a laptop never does.
# So this is the target that tells you `make image-push` needs it, rather than
# letting docker fail on `ghcr.io//discovery-service` and making you work out
# why.
require-image-repos:
	@test -n "$(OWNER)" || \
		{ echo "::error::OWNER is empty — set GITHUB_REPOSITORY_OWNER (CI sets it; locally, pass OWNER=<org>)"; exit 1; }
	@printf 'publishing %s to:\n' "$(VERSION)"; printf '  %s\n' $(IMAGE_REPOS)

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

## clean: remove build output, coverage profiles and scan artifacts
clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html coverage-report.md \
		$(SARIF_REPORTS) trivy-report.md

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
# above: gotestsum is CI-only (see ci.yml), so it doesn't belong in the
# service's or the linter's dependency graph either one.
$(GOTESTSUM):
	@mkdir -p $(BIN_DIR)
	GOBIN=$(abspath $(BIN_DIR)) $(GO) install gotest.tools/gotestsum@$(GOTESTSUM_VERSION)

# CI/hook-only, like gotestsum: actionlint belongs in neither the service's
# dependency graph nor the linter's, so it installs directly rather than
# through tools/go.mod.
$(ACTIONLINT):
	@mkdir -p $(BIN_DIR)
	GOBIN=$(abspath $(BIN_DIR)) $(GO) install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

# The prebuilt release binary, not `go install`: trivy's rpm-db parser needs
# cgo, and its module graph is comparable in size to golangci-lint's for a
# tool nothing here imports — the official install script is what
# aquasecurity itself recommends over building from source for exactly this.
$(TRIVY):
	@mkdir -p $(BIN_DIR)
	curl -sfL https://raw.githubusercontent.com/aquasecurity/trivy/main/contrib/install.sh | \
		sh -s -- -b $(abspath $(BIN_DIR)) $(TRIVY_VERSION)

.PHONY: help build test test-short test-ci cover cover-total cover-report \
	cover-html cover-diff lint fmt lint-actions lint-staged hooks sqlc \
	sqlc-verify migrate run logs migrate-down security trivy-deps \
	trivy-image trivy-release-gate trivy-report trivy-gate docker \
	image-build image-push image-publish require-image-repos up down \
	verify newman audit tools clean
