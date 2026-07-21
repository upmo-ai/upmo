# Every target runs inside Docker through compose.yml.

COMPOSE := docker compose
GO      := $(COMPOSE) run --rm go
WEB     := $(COMPOSE) run --rm web
LINT    := $(COMPOSE) run --rm golangci-lint

# Dev only database for integration tests, served by the compose postgres.
TEST_DATABASE_URL := postgres://upmo:upmo@postgres:5432/upmo?sslmode=disable

.PHONY: help build test test-integration lint fmt up down ci web

help: ## List the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-18s %s\n", $$1, $$2}'

build: ## Build the Go module and the web app
	$(GO) go build ./...
	$(WEB) sh -c "pnpm install --frozen-lockfile && pnpm build"

test: ## Run Go unit tests with the race detector and the web tests
	$(GO) go test -race ./...
	$(WEB) sh -c "pnpm install --frozen-lockfile && pnpm test"

test-integration: ## Run Go integration tests against the compose postgres
	$(COMPOSE) up -d --wait postgres
	$(COMPOSE) run --rm -e UPMO_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go go test -tags integration ./...

lint: ## Run golangci-lint (linters and format check) and the web lint
	$(LINT) golangci-lint run ./...
	$(WEB) sh -c "pnpm install --frozen-lockfile && pnpm lint"

fmt: ## Format Go code with gofmt and goimports
	$(LINT) golangci-lint fmt ./...

up: ## Start brain and postgres
	$(COMPOSE) up -d --wait brain postgres

down: ## Stop the dev stack
	$(COMPOSE) down

ci: build lint test ## Run the same steps as CI

web: ## Install, lint, test and build the web app
	$(WEB) sh -c "pnpm install --frozen-lockfile && pnpm lint && pnpm test && pnpm build"
