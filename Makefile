GO_DIR := go
GO_BINARY := bin/workspace-core
GO_RUN_BINARY := bin/workspace-run
GO_DAEMON_BINARY := bin/workspace-daemon

.PHONY: go-build go-test go-only-release-gate python-test v1-release-gate test

go-build:
	mkdir -p bin
	cd $(GO_DIR) && GOTELEMETRY=off go build -o ../$(GO_BINARY) ./cmd/workspace-core
	cd $(GO_DIR) && GOTELEMETRY=off go build -o ../$(GO_RUN_BINARY) ./cmd/workspace-run
	cd $(GO_DIR) && GOTELEMETRY=off go build -o ../$(GO_DAEMON_BINARY) ./cmd/workspace-daemon

go-test:
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./...

go-only-release-gate: go-build
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./...
	cd $(GO_DIR) && GOTELEMETRY=off go test -race -count=1 ./...
	cd $(GO_DIR) && GOTELEMETRY=off go vet ./...

python-test: go-build
	PYTHONDONTWRITEBYTECODE=1 PYTHON_DOTENV_DISABLED=1 .venv/bin/python -m unittest discover -s tests

v1-release-gate: go-only-release-gate
	uv lock --check --offline
	PYTHONDONTWRITEBYTECODE=1 PYTHON_DOTENV_DISABLED=1 .venv/bin/python -m unittest discover -s tests
	PYTHONPYCACHEPREFIX=/tmp/workspace-os-v1-pycache PYTHON_DOTENV_DISABLED=1 .venv/bin/python -m compileall -q src tests
	git diff --check

test: go-test python-test
