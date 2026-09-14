SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X github.com/YohannHommet/argus/server/internal/telemetry.Version=$(VERSION) \
	-X github.com/YohannHommet/argus/server/internal/telemetry.Commit=$(COMMIT)

# Local run config. Override the port when 8080 is taken: `make up PORT=18080`.
# Falls back to deploy/.env's ARGUS_HTTP_PORT (written by `make setup`, or by
# hand) when PORT isn't given on the command line, so editing that one file
# is enough to change the port for every target below — no need to also
# remember `PORT=` on every invocation.
PORT ?= $(shell [ -f deploy/.env ] && grep -m1 '^ARGUS_HTTP_PORT=' deploy/.env | cut -d= -f2)
ifeq ($(strip $(PORT)),)
PORT := 8080
endif
COMPOSE := docker compose -f deploy/docker-compose.yml
export ARGUS_HTTP_PORT := $(PORT)

.PHONY: help up rebuild down restart clean demo logs status ui \
	setup install-hook uninstall-hook \
	dev build test test-fast lint type-check openapi-check ci gen migrate sim \
	compose-up compose-smoke e2e \
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

# --- Run the app (start here) -----------------------------------------------
# Wraps the docker compose stack. Default port 8080; override with PORT=NNNN.

up: check-compose ## Start Argus (builds on first run), wait for ready, print the URL
	$(COMPOSE) up -d
	@for i in $$(seq 1 60); do curl -fsS localhost:$(PORT)/readyz >/dev/null 2>&1 && break; sleep 1; done
	@echo "Argus is up -> http://localhost:$(PORT)"

rebuild: check-compose ## Rebuild the image from source, then (re)start
	$(COMPOSE) up -d --build

down: check-compose ## Stop Argus (keeps its data)
	$(COMPOSE) down

restart: check-compose ## Restart just the argusd container
	$(COMPOSE) restart argusd

clean: check-compose ## Stop Argus and delete its data volume (wipes everything)
	$(COMPOSE) down -v

demo: check-compose ## Seed deterministic demo data into the running stack
	$(COMPOSE) exec -T argusd /argusd sim --mode=demo --seed=42

logs: check-compose ## Follow argusd logs
	$(COMPOSE) logs -f argusd

status: check-compose ## Show container status and /readyz
	@$(COMPOSE) ps
	@printf 'readyz: '; curl -s localhost:$(PORT)/readyz || echo "(no response on :$(PORT))"; echo

ui: ## Print the UI URL
	@echo "http://localhost:$(PORT)"

setup: check-compose ## Guided onboarding: check docker, create deploy/.env, start the stack, print + install the Claude Code hook
	@bash scripts/setup.sh

install-hook: ## Merge Argus's hook into ~/.claude/settings.json (idempotent; override port with PORT=, path with ARGUS_SETTINGS_FILE=)
	@bash scripts/install-hook.sh install

uninstall-hook: ## Remove Argus's hook from ~/.claude/settings.json (only Argus's own entries; leaves everything else)
	@bash scripts/install-hook.sh uninstall

# --- Everyday targets --------------------------------------------------------

dev: check-server check-web ## Run argusd and the web dev server together
	@( cd server && go run ./cmd/argusd serve ) & \
	( cd web && pnpm dev ) & \
	wait

build: check-server check-web ## Build the argusd binary and the web assets
	cd web && pnpm build
	mkdir -p bin
	cd server && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../bin/argusd ./cmd/argusd

test: check-server check-web ## Run the CI-equivalent test suite: Go -tags=e2e -race + coverage floor, web unit --coverage (see .github/workflows/ci.yml go-test/web)
	cd server && go test -tags=e2e -race -covermode=atomic -coverprofile=cover.out ./...
	cd server && ../scripts/coverage-floor.sh cover.out ../scripts/coverage-floors.txt
	cd web && pnpm unit --coverage

test-fast: check-server check-web ## Fast inner-loop test run: no -race, no -tags=e2e (skips the e2e suites), no coverage floor
	cd server && go test ./...
	cd web && pnpm unit

lint: check-server check-web ## Lint the Go code (golangci-lint + gofmt) and the web code
	cd server && golangci-lint run ./...
	cd server && unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "The following files are not gofmt-formatted:"; echo "$$unformatted"; exit 1; fi
	cd web && pnpm lint

type-check: check-web ## Type-check the web app (vue-tsc)
	cd web && pnpm type-check

openapi-check: check-server check-web ## Validate the OpenAPI spec and fail if the generated TS client is stale (see .github/workflows/ci.yml openapi)
	cd server && go run ./internal/tools/specvalidate
	cd web && pnpm gen:api
	cd web && git diff --exit-code src/api/schema.d.ts

ci: lint type-check test build openapi-check ## Local equivalent of the CI pipeline (go-lint, web type-check, go-test+web-unit w/ coverage, build, openapi)

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

e2e: check-web check-compose ## Run the browser E2E suite (boots the stack, seeds, Playwright, tears down)
	bash scripts/e2e.sh
