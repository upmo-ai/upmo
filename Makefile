# Every target runs inside Docker through compose.yml.

COMPOSE := docker compose
GO      := $(COMPOSE) run --rm go
WEB     := $(COMPOSE) run --rm web
LINT    := $(COMPOSE) run --rm golangci-lint
TOOL    := $(COMPOSE) run --rm

# A git worktree's .git is a file that points at the common git directory.
# Mount that directory so git works inside the containers. Empty in a plain
# clone, where compose falls back to ./.git.
UPMO_GIT_DIR := $(shell sed -n 's|^gitdir: \(.*\)/worktrees/[^/]*$$|\1|p' .git 2>/dev/null)
export UPMO_GIT_DIR

# Commits to check on this branch: everything not yet on main.
COMMITS_RANGE ?= origin/main..HEAD

# Dev only database for integration tests, served by the compose postgres.
TEST_DATABASE_URL := postgres://upmo:upmo@postgres:5432/upmo?sslmode=disable

.PHONY: help build test test-integration lint fmt up down ci web arch vuln secrets commits docs fuzz

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

arch: ## Check the import rules
	$(GO) go test ./internal/archtest

vuln: ## Scan for known vulnerabilities with govulncheck and osv-scanner
	$(GO) go tool govulncheck ./...
	$(TOOL) osv-scanner

secrets: ## Scan the full git history for secrets with gitleaks
	$(TOOL) gitleaks

commits: ## Check Conventional Commit subjects on this branch
	$(GO) go run ./tools/repolint commits -range $(COMMITS_RANGE)

docs: ## Check markdown style and links
	$(TOOL) markdownlint
	$(TOOL) lychee

fuzz: ## Fuzz the safety libraries for 30 seconds each
	$(GO) go test -fuzz=FuzzWrap -fuzztime=30s ./internal/platform/trustfence
	$(GO) go test -fuzz=FuzzNeutralizeSlot -fuzztime=30s ./internal/platform/trustfence
	$(GO) go test -fuzz=FuzzBlockedAddr -fuzztime=30s ./internal/platform/netguard
	$(GO) go test -fuzz=FuzzVerify -fuzztime=30s ./internal/platform/webhooksig

ci: build lint test arch vuln secrets commits docs ## Run the same steps as CI

web: ## Install, lint, test and build the web app
	$(WEB) sh -c "pnpm install --frozen-lockfile && pnpm lint && pnpm test && pnpm build"
