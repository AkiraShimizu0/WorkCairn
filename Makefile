GO_DIR := go
GO_BINARY := bin/workspace-core

.PHONY: go-build go-test python-test test

go-build:
	mkdir -p bin
	cd $(GO_DIR) && GOTELEMETRY=off go build -o ../$(GO_BINARY) ./cmd/workspace-core

go-test:
	cd $(GO_DIR) && GOTELEMETRY=off go test -count=1 ./...

python-test: go-build
	PYTHONDONTWRITEBYTECODE=1 .venv/bin/python -m unittest discover -s tests

test: go-test python-test
