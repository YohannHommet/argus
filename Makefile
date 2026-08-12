SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X github.com/YohannHommet/argus/server/internal/telemetry.Version=$(VERSION) \
	-X github.com/YohannHommet/argus/server/internal/telemetry.Commit=$(COMMIT)

.PHONY: help dev build test lint ci gen migrate sim compose-up compose-smoke \
	check-server check-web check-migrations check-compose check-smoke

help: ## Show this list of targets
	@echo "Argus — available make targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# --- Guards -----------------------------------------------------------------
# Each guard fails fast with a message naming the ticket that provides the
# missing input, instead of letting the real command fail with a confusing
# "no such file or directory" further down.

# (not listed in `make help` — internal prerequisites only)
check-server:
	@test -f server/go.mod || { echo "error: server/go.mod not found — not implemented until P1-02 (Go module, subcommand skeleton, config, logging)" >&2; exit 1; }

check-web:
	@test -f web/package.json || { echo "error: web/package.json not found — not implemented until P1-03 (web app scaffold)" >&2; exit 1; }

check-migrations:
	@test -f server/sqlc.yaml || { echo "error: server/sqlc.yaml not found — not implemented until P1-04 (store skeleton, embedded goose migrations)" >&2; exit 1; }

check-compose:
	@test -f deploy/docker-compose.yml || { echo "error: deploy/docker-compose.yml not found — not implemented until P1-07 (Dockerfile, docker-compose, scripts/smoke.sh)" >&2; exit 1; }

check-smoke:
	@test -f scripts/smoke.sh || { echo "error: scripts/smoke.sh not found — not implemented until P1-07 (Dockerfile, docker-compose, scripts/smoke.sh)" >&2; exit 1; }

# --- Everyday targets --------------------------------------------------------

dev: check-server check-web ## Run argusd and the web dev server together
	@( cd server && go run ./cmd/argusd serve ) & \
	( cd web && pnpm dev ) & \
	wait

build: check-server check-web ## Build the argusd binary and the web assets
	cd web && pnpm build
	mkdir -p bin
	cd server && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../bin/argusd ./cmd/argusd

test: check-server check-web ## Run the Go test suite and the web unit tests
	cd server && go test ./...
	cd web && pnpm unit

lint: check-server check-web ## Lint the Go code and the web code
	cd server && golangci-lint run ./...
	cd web && pnpm lint

ci: lint test build ## Local equivalent of the CI pipeline (lint + test + build, both stacks)

gen: check-migrations check-web ## Generate sqlc code and the TS API client
	cd server && sqlc generate
	cd web && pnpm gen:api

migrate: check-server ## Run database migrations via argusd
	cd server && go run ./cmd/argusd migrate

sim: check-server ## Run the traffic simulator via argusd
	cd server && go run ./cmd/argusd sim

compose-up: check-compose ## Start the local stack (postgres + argusd) via docker compose
	docker compose -f deploy/docker-compose.yml up -d

compose-smoke: check-smoke ## Run the compose smoke test
	bash scripts/smoke.sh
