SHELL := bash
.SHELLFLAGS := -euo pipefail -c
include scripts/versions.env
GO ?= go

.PHONY: test build build-go web web-stub check-web test-web image

test: web-stub
	$(GO) test -race ./...

build: web build-go

build-go: web-stub
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/hogwatch ./cmd/hogwatch

web:
	cd web && npm ci && HOGWATCH_COMMIT=$$(git rev-parse HEAD 2>/dev/null || true) npm run build

check-web:
	cd web && npm ci && npm run check

test-web:
	cd web && npm ci && npm test

web-stub: web/dist/index.html

web/dist/index.html:
	mkdir -p web/dist
	printf '%s' '<!doctype html><title>Hogwatch</title><div id="app">SPA not built: run make web</div>' > $@

GOTOOL := $(GO) tool -modfile=tools/go.mod

.PHONY: gen gen-check
gen:
	CGO_ENABLED=0 $(GOTOOL) sqlc generate -f internal/db/sqlc.yaml
	$(GOTOOL) oapi-codegen -config internal/web/oapi/config.yaml api/openapi.yaml

# core.fileMode=false: WSL git on /mnt/c reports spurious mode changes.
gen-check: gen
	test -z "$$(git -c core.fileMode=false status --porcelain -- internal/db/dbq internal/web/oapi)"

.PHONY: lint
lint: web-stub gen-check
	$(GOTOOL) golangci-lint run ./...
	bash scripts/lint-barrier.sh
	test "$$(sed -n 's/^go //p' go.mod)" = "$(GO_FLOOR)" || { echo "go.mod go line must be $(GO_FLOOR) (GO_FLOOR in scripts/versions.env)"; exit 1; }
	$(GO) tool -modfile=tools/actionlint.mod actionlint
	! git ls-files --eol | grep -E '(i|w)/(crlf|mixed)'

.PHONY: test-integration
test-integration: web-stub
	bash scripts/test-integration.sh

# check-release proves a release build carries no test hook. The negative
# control builds with the integration tag and must show the probe string, so a
# count of 0 in the release binary means something.
.PHONY: check-release
check-release: web-stub
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/hogwatch ./cmd/hogwatch
	CGO_ENABLED=0 $(GO) build -trimpath -tags integration -o bin/hogwatch-integration ./cmd/hogwatch
	@rel=$$(grep -c HOGWATCH_TEST_PAUSE_PHASE bin/hogwatch || true); \
	ctl=$$(grep -c HOGWATCH_TEST_PAUSE_PHASE bin/hogwatch-integration || true); \
	echo "check-release: release=$$rel integration(control)=$$ctl"; \
	test "$$ctl" -ge 1 || { echo "FAIL: the probe cannot see the string in the integration build"; exit 1; }; \
	test "$$rel" -eq 0 || { echo "FAIL: the release binary contains the test hook"; exit 1; }

# test-e2e seeds a throwaway data dir, builds the binary with the SPA and runs the
# Playwright smoke against `hogwatch serve` on 127.0.0.1:18080. Needs
# `npx playwright install chromium` once.
.PHONY: test-e2e
test-e2e:
	$(MAKE) web
	$(MAKE) build-go
	d=$$(mktemp -d); trap 'rm -rf "$$d"' EXIT; \
	$(GO) test -tags e2e ./internal/e2e -run TestSeed -count=1 -args -out "$$d"; \
	cd web && HW_E2E_DIR="$$d" npm run test:e2e

# image builds the container image for this machine's platform and loads it
# into the local Docker. A multi-platform build cannot be loaded, so for that
# run the docker buildx command with --platform yourself (see deploy/Dockerfile).
IMAGE ?= hogwatch:dev
image:
	docker buildx build -f deploy/Dockerfile --load -t $(IMAGE) \
	  --build-arg NODE_VERSION=$(NODE_VERSION) --build-arg GO_VERSION=$(GO_VERSION) \
	  --build-arg VERSION=$$(git describe --always --dirty) --build-arg COMMIT=$$(git rev-parse HEAD) .

# docs builds the documentation site (dead links fail the build).
.PHONY: docs
docs:
	cd docs && npm ci && npm run build

# docs-shots retakes the documentation screenshots from the e2e seed. It needs
# the seeded server on 127.0.0.1:18080 (see web/playwright.docs.config.ts) and
# a Chromium for Playwright.
.PHONY: docs-shots
docs-shots:
	cd web && npx playwright test -c playwright.docs.config.ts
