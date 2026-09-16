SHELL := /bin/bash

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
GO ?= go
PKG := ./...
BIN := bin/cmt-top

.PHONY: help web build build-no-web run test test-race lint tidy clean e2e fmt

help:
	@echo "Targets:"
	@echo "  build          build everything (web bundle + Go binary)"
	@echo "  build-no-web   build the Go binary only (web UI returns 503)"
	@echo "  web            build the Svelte SPA into internal/web/dist"
	@echo "  run            build and run against default Injective RPC"
	@echo "  test           run tests"
	@echo "  test-race      run tests with -race"
	@echo "  lint           run golangci-lint (if installed)"
	@echo "  tidy           go mod tidy"
	@echo "  clean          remove build artifacts"
	@echo "  e2e            run local RPC/WS integration tests"

web:
	@if [ -d web ] && [ -f web/package.json ]; then \
		echo "==> building web bundle"; \
		cd web && pnpm install --frozen-lockfile && pnpm build; \
	else \
		echo "==> web/ not present, skipping"; \
	fi

build: web
	@mkdir -p bin
	$(GO) build -tags webui -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/cmt-top
	@echo "==> built $(BIN)"

build-no-web:
	@mkdir -p bin
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/cmt-top
	@echo "==> built $(BIN) (web stub)"

run: build-no-web
	./$(BIN)

test:
	$(GO) test $(PKG)

test-race:
	$(GO) test -race $(PKG)

e2e:
	$(GO) test -race -count=1 ./internal/chain/... ./internal/core/... ./internal/web/...

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed"; exit 1; }
	golangci-lint run $(PKG)

fmt:
	$(GO) fmt $(PKG)

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin internal/web/dist/* web/node_modules web/dist
