SHELL := /bin/bash

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
GO ?= go
PKG := ./...
BIN := bin/cmt-top
PROFILE ?= mainnet45
USERS ?= 150
HOLD ?= 30m
CAPACITY_IMAGE ?= cmt-top-capacity:local

.PHONY: help web build build-no-web run test test-race lint tidy clean e2e fmt capacity-tools capacity-build capacity-smoke capacity-test capacity-image capacity-isolated

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
	@echo "  capacity-build build bundled app and synthetic replay/load tools"
	@echo "  capacity-smoke run a 150-session, 10-second local protocol check"
	@echo "  capacity-test  run PROFILE=mainnet45|testnet5|stalled128|mixed USERS=150 HOLD=30m"
	@echo "  capacity-isolated run protocol smoke in Docker with outbound networking disabled"

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

capacity-tools:
	@mkdir -p bin
	$(GO) build -mod=readonly -o bin/cmt-top-replay ./cmd/cmt-top-replay
	$(GO) build -mod=readonly -o bin/cmt-top-load ./cmd/cmt-top-load

capacity-build: build capacity-tools

capacity-smoke: capacity-build
	CAPACITY_USERS=150 CAPACITY_DURATION=10s bash deploy/capacity/smoke.sh

capacity-test: capacity-build
	@case "$(PROFILE)" in \
	  mainnet45|testnet5) CAPACITY_PROFILE="$(PROFILE)" CAPACITY_USERS="$(USERS)" CAPACITY_DURATION="$(HOLD)" bash deploy/capacity/smoke.sh ;; \
	  stalled128) CAPACITY_PROFILE=mainnet45 CAPACITY_ROUNDS=128 CAPACITY_STALLED=1 CAPACITY_INVESTIGATE_PERCENT=100 CAPACITY_USERS="$(USERS)" CAPACITY_DURATION="$(HOLD)" bash deploy/capacity/smoke.sh ;; \
	  mixed) CAPACITY_PROFILE=mainnet45 CAPACITY_INVESTIGATE_PERCENT=50 CAPACITY_USERS="$(USERS)" CAPACITY_DURATION="$(HOLD)" bash deploy/capacity/smoke.sh ;; \
	  *) echo "Unknown capacity PROFILE: $(PROFILE)" >&2; exit 1 ;; \
	esac

capacity-image:
	docker build -f deploy/capacity/Dockerfile -t $(CAPACITY_IMAGE) .

capacity-isolated: capacity-image
	@mkdir -p capacity-results/smoke
	@chmod 777 capacity-results/smoke
	docker run --rm --network none --cpus=2 --memory=1g -v "$(CURDIR)/capacity-results/smoke:/results" -e CAPACITY_OUTPUT_DIR=/results -e CAPACITY_USERS=$(USERS) -e CAPACITY_DURATION=10s $(CAPACITY_IMAGE)

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed"; exit 1; }
	golangci-lint run $(PKG)

fmt:
	$(GO) fmt $(PKG)

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin internal/web/dist/* web/node_modules web/dist
