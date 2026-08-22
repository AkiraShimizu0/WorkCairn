GO_DIR := go
GO_BINARY := bin/workcairn-core
GO_RUN_BINARY := bin/workcairn
GO_DAEMON_BINARY := bin/workcairn-daemon
BUILD_VERSION ?= dev
BUILD_COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || printf unknown)
BUILD_DATE ?= unknown
GO_LDFLAGS := -X github.com/AkiraShimizu0/workcairn/go/internal/buildinfo.Version=$(BUILD_VERSION) -X github.com/AkiraShimizu0/workcairn/go/internal/buildinfo.Commit=$(BUILD_COMMIT) -X github.com/AkiraShimizu0/workcairn/go/internal/buildinfo.BuildDate=$(BUILD_DATE)
GO_BUILD_FLAGS := -trimpath -buildvcs=false -ldflags "$(GO_LDFLAGS)"
RELEASE_VERSION ?=
RELEASE_GOOS ?= $(shell cd $(GO_DIR) && go env GOOS)
RELEASE_GOARCH ?= $(shell cd $(GO_DIR) && go env GOARCH)
DIST_DIR ?= dist
PUBLIC_BETA_VERSION := $(shell sed -n '1p' VERSION)

.PHONY: go-build go-test public-beta-smoke public-beta-browser-setup public-beta-browser-gate public-beta-build-matrix v1-release-gate release-package verify-release-package test check-ui-fast check-ui-changed check-ui-full synthesis-acceptance

go-build:
	mkdir -p bin
	cd $(GO_DIR) && GOTELEMETRY=off go build $(GO_BUILD_FLAGS) -o ../$(GO_BINARY) ./cmd/workcairn-core
	cd $(GO_DIR) && GOTELEMETRY=off go build $(GO_BUILD_FLAGS) -o ../$(GO_RUN_BINARY) ./cmd/workcairn
	cd $(GO_DIR) && GOTELEMETRY=off go build $(GO_BUILD_FLAGS) -o ../$(GO_DAEMON_BINARY) ./cmd/workcairn-daemon

go-test:
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./...

public-beta-smoke:
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/httpapi -run '^TestPublicBetaCommandAllowList'
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/httpapi -run '^TestMobileInteractionHTTPFlowUsesMockProviderAndTemporaryVaultToCompletion$$'
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/httpapi -run '^TestMobileInteractionHTTPFlowRequestChangesRevisionReReviewToCompletion$$'
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/httpapi -run '^TestMobileInteractionHTTPFlowStructuredReviewResponseViolationClassifiesOuterCommand$$'
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/httpapi -run '^TestMobileInteractionHTTPFlowSameRequestTwiceCreatesDistinctProjectsSafely$$'
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/httpapi -run '^TestMobileInteractionHTTPFlowMalformedCEOPlanResponseClassifiesOuterCommand$$'
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/runtime -run '^TestRuntimeCompletesTemporaryVaultExecutionWithDeliverableAndAudit$$'
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/process -run '^TestReviewedWorkflowTemporaryVaultRequestChangesRevisionReReviewAndReplay$$'

# Synthesis quality is measured separately from the release and browser gates.
# Fake baselines execute by default. A real Claude request requires the human
# to opt in explicitly with both PROVIDER=claude and EXECUTE=1; credentials
# are loaded through the existing environment/Keychain path, never argv.
PROVIDER ?= fake-good
EXECUTE ?= 0
synthesis-acceptance:
	cd $(GO_DIR) && GOTELEMETRY=off go run ./cmd/workcairn-synthesis-acceptance --provider '$(PROVIDER)' $(if $(filter fake-good fake-bad,$(PROVIDER)),--execute,$(if $(filter 1 true yes,$(EXECUTE)),--execute,))

# Node and Playwright are test-only dependencies for the actual-daemon browser
# acceptance harness. They are intentionally absent from Go modules, product
# binaries, release archives, and the v1 release gate.
public-beta-browser-setup:
	npm ci --ignore-scripts
	npm exec -- playwright install chromium webkit

public-beta-browser-gate: go-build
	test -x node_modules/.bin/playwright
	npm run browser:gate

# Staged Browser Gate entry points (Test Speed round). Use narrow/targeted
# runs while iterating; run check-ui-full only once, right before a
# checkpoint commit. See AGENTS.md "Browser Gate Validation Staging".
#
# check-ui-fast: chromium-desktop only, @critical-tagged tests -- the
# smallest set that still covers the core product path end to end.
check-ui-fast: go-build
	node --check go/internal/httpapi/web/app.js
	test -x node_modules/.bin/playwright
	npx playwright test --project=chromium-desktop --grep '@critical'

# check-ui-changed AREA=<tag>: chromium-desktop only, tests tagged @<tag>
# (conversation|deliverable|archive|setup|office|failure|detail|mobile).
# Pick AREA to match whatever you just touched.
check-ui-changed: go-build
	test -n "$(AREA)" || (echo "usage: make check-ui-changed AREA=<conversation|deliverable|archive|setup|office|failure|detail|mobile>" && exit 1)
	test -x node_modules/.bin/playwright
	npx playwright test --project=chromium-desktop --grep '@$(AREA)'

# check-ui-full: full regression across both projects -- the exact same
# recipe as public-beta-browser-gate, kept as its own name so "fast /
# changed / full" reads as one staged family. Run this once, at Checkpoint
# completion / immediately before a commit candidate, not while iterating.
check-ui-full: public-beta-browser-gate

public-beta-build-matrix:
	./scripts/check-release-matrix.sh

v1-release-gate: go-build public-beta-build-matrix
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./...
	cd $(GO_DIR) && GOTELEMETRY=off go test -race -count=1 ./...
	cd $(GO_DIR) && GOTELEMETRY=off go vet ./...
	test -z "$$(cd $(GO_DIR) && gofmt -l .)"
	test -x scripts/package-release.sh
	test -x scripts/check-release-matrix.sh
	test -x scripts/verify-release-archive.sh
	sh -n scripts/package-release.sh scripts/check-release-matrix.sh scripts/verify-release-archive.sh
	git diff --check

release-package:
	RELEASE_VERSION='$(if $(RELEASE_VERSION),$(RELEASE_VERSION),$(PUBLIC_BETA_VERSION))' RELEASE_GOOS='$(RELEASE_GOOS)' RELEASE_GOARCH='$(RELEASE_GOARCH)' DIST_DIR='$(DIST_DIR)' BUILD_COMMIT='$(BUILD_COMMIT)' BUILD_DATE='$(BUILD_DATE)' ./scripts/package-release.sh

verify-release-package:
	test -n "$(ARCHIVE)"
	./scripts/verify-release-archive.sh '$(ARCHIVE)'

test: go-test
