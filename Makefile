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

.PHONY: go-build go-test public-beta-smoke public-beta-build-matrix v1-release-gate release-package verify-release-package test

go-build:
	mkdir -p bin
	cd $(GO_DIR) && GOTELEMETRY=off go build $(GO_BUILD_FLAGS) -o ../$(GO_BINARY) ./cmd/workcairn-core
	cd $(GO_DIR) && GOTELEMETRY=off go build $(GO_BUILD_FLAGS) -o ../$(GO_RUN_BINARY) ./cmd/workcairn
	cd $(GO_DIR) && GOTELEMETRY=off go build $(GO_BUILD_FLAGS) -o ../$(GO_DAEMON_BINARY) ./cmd/workcairn-daemon

go-test:
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./...

public-beta-smoke:
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/httpapi -run '^TestMobileInteractionHTTPFlowUsesMockProviderAndTemporaryVaultToCompletion$$'
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/httpapi -run '^TestMobileInteractionHTTPFlowRequestChangesRevisionReReviewToCompletion$$'
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/httpapi -run '^TestMobileInteractionHTTPFlowMalformedReviewResponseClassifiesOuterCommand$$'
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/httpapi -run '^TestMobileInteractionHTTPFlowSameRequestTwiceCreatesDistinctProjectsSafely$$'
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/httpapi -run '^TestMobileInteractionHTTPFlowMalformedCEOPlanResponseClassifiesOuterCommand$$'
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/runtime -run '^TestRuntimeCompletesTemporaryVaultExecutionWithDeliverableAndAudit$$'
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./internal/process -run '^TestReviewedWorkflowTemporaryVaultRequestChangesRevisionReReviewAndReplay$$'

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
