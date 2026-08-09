GO_DIR := go
GO_BINARY := bin/workspace-core
GO_RUN_BINARY := bin/workspace-run
GO_DAEMON_BINARY := bin/workspace-daemon
BUILD_VERSION ?= dev
BUILD_COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || printf unknown)
BUILD_DATE ?= unknown
GO_LDFLAGS := -X github.com/AkiraShimizu0/workspace-os/go/internal/buildinfo.Version=$(BUILD_VERSION) -X github.com/AkiraShimizu0/workspace-os/go/internal/buildinfo.Commit=$(BUILD_COMMIT) -X github.com/AkiraShimizu0/workspace-os/go/internal/buildinfo.BuildDate=$(BUILD_DATE)
GO_BUILD_FLAGS := -trimpath -buildvcs=false -ldflags "$(GO_LDFLAGS)"
RELEASE_VERSION ?=
RELEASE_GOOS ?= $(shell cd $(GO_DIR) && go env GOOS)
RELEASE_GOARCH ?= $(shell cd $(GO_DIR) && go env GOARCH)
DIST_DIR ?= dist

.PHONY: go-build go-test go-only-release-gate python-test v1-release-gate release-package test

go-build:
	mkdir -p bin
	cd $(GO_DIR) && GOTELEMETRY=off go build $(GO_BUILD_FLAGS) -o ../$(GO_BINARY) ./cmd/workspace-core
	cd $(GO_DIR) && GOTELEMETRY=off go build $(GO_BUILD_FLAGS) -o ../$(GO_RUN_BINARY) ./cmd/workspace-run
	cd $(GO_DIR) && GOTELEMETRY=off go build $(GO_BUILD_FLAGS) -o ../$(GO_DAEMON_BINARY) ./cmd/workspace-daemon

go-test:
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./...

go-only-release-gate: go-build
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./...
	cd $(GO_DIR) && GOTELEMETRY=off go test -race -count=1 ./...
	cd $(GO_DIR) && GOTELEMETRY=off go vet ./...

python-test: go-build
	PYTHONDONTWRITEBYTECODE=1 PYTHON_DOTENV_DISABLED=1 .venv/bin/python -m unittest discover -s tests

v1-release-gate: go-only-release-gate
	test -x scripts/package-release.sh
	sh -n scripts/package-release.sh
	uv lock --check --offline
	PYTHONDONTWRITEBYTECODE=1 PYTHON_DOTENV_DISABLED=1 .venv/bin/python -m unittest discover -s tests
	PYTHONPYCACHEPREFIX=/tmp/workspace-os-v1-pycache PYTHON_DOTENV_DISABLED=1 .venv/bin/python -m compileall -q src tests
	git diff --check

release-package:
	RELEASE_VERSION='$(RELEASE_VERSION)' RELEASE_GOOS='$(RELEASE_GOOS)' RELEASE_GOARCH='$(RELEASE_GOARCH)' DIST_DIR='$(DIST_DIR)' BUILD_COMMIT='$(BUILD_COMMIT)' BUILD_DATE='$(BUILD_DATE)' ./scripts/package-release.sh

test: go-test python-test
