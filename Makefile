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

# Commits to check on this branch: everything not yet on main. Uses the local
# main when there is no origin/main yet.
COMMITS_BASE := $(shell git rev-parse --verify --quiet origin/main >/dev/null && echo origin/main || echo main)
COMMITS_RANGE ?= $(COMMITS_BASE)..HEAD
# Changes to check for new migration files: three dots diff against the merge base.
MIGRATIONS_RANGE ?= $(COMMITS_BASE)...HEAD

# Dev only database for integration tests, served by the compose postgres.
TEST_DATABASE_URL := postgres://upmo:upmo@postgres:5432/upmo?sslmode=disable

# The engine socket as the Docker daemon sees it. Bind mounts resolve on the
# daemon side, so this is the same on Docker Desktop, Colima and CI.
UPMO_DOCKER_SOCKET ?= /var/run/docker.sock
export UPMO_DOCKER_SOCKET

# Build metadata stamped into internal/platform/buildinfo.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/upmo-ai/upmo/internal/platform/buildinfo.Version=$(VERSION) \
	-X github.com/upmo-ai/upmo/internal/platform/buildinfo.Commit=$(COMMIT) \
	-X github.com/upmo-ai/upmo/internal/platform/buildinfo.BuildTime=$(BUILD_TIME)

.PHONY: help build build-go build-web test test-go test-web test-integration test-live test-regression lint lint-go lint-shell lint-web lint-terms fmt up down ci web e2e arch vuln secrets commits migrations title hooks docs fuzz canary workflows images

help: ## List the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-18s %s\n", $$1, $$2}'

build: build-go build-web ## Build the Go module and the web app

build-go: ## Build the Go module
	$(GO) go build -ldflags "$(LDFLAGS)" ./...

build-web: ## Build the web app
	$(WEB) sh -c "pnpm install --frozen-lockfile && pnpm build"

# Reports (JUnit and coverage) land in reports/ for the CI run summary and Codecov.
test: test-go test-web ## Run Go unit tests with the race detector and the web tests

test-go: ## Run Go unit tests with the race detector
	mkdir -p reports
	$(GO) go tool gotestsum --junitfile reports/go-unit.xml -- -race -coverprofile=reports/go-unit.cover ./...

test-web: ## Run the web tests
	$(WEB) sh -c "pnpm install --frozen-lockfile && pnpm test"

test-integration: ## Run Go integration tests against the compose postgres and the Docker engine
	mkdir -p reports
	$(COMPOSE) up -d --wait postgres
	$(COMPOSE) run --rm -v "$(UPMO_DOCKER_SOCKET):/var/run/docker.sock" -e UPMO_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" -e UPMO_TEST_DOCKER_HOST=unix:///var/run/docker.sock go go tool gotestsum --junitfile reports/go-integration.xml -- -tags integration -coverprofile=reports/go-integration.cover ./...

test-live: ## Run tests that call a real model provider
	$(GO) go test -tags live ./...

test-regression: ## Run regression tests
	$(GO) go test -race -run Regression ./...

lint: lint-go lint-shell lint-web lint-terms ## Run golangci-lint (linters and format check), shellcheck, the web lint and retired words

lint-go: ## Run golangci-lint (linters and format check)
	$(LINT) golangci-lint run ./...

lint-shell: ## Run shellcheck on the scripts and the git hooks
	$(TOOL) shellcheck

lint-web: ## Run the web lint
	$(WEB) sh -c "pnpm install --frozen-lockfile && pnpm lint"

lint-terms: ## Check every tracked file for retired words
	$(GO) sh -c "git ls-files | go run ./tools/repolint terms"

fmt: ## Format Go code with gofmt and goimports
	$(LINT) golangci-lint fmt ./...

up: ## Start the worker, brain and postgres
	$(COMPOSE) up -d --wait worker brain postgres

down: ## Stop the dev stack
	$(COMPOSE) down

arch: ## Check the import rules
	$(GO) go test ./internal/archtest

vuln: ## Scan for known vulnerabilities with govulncheck and osv-scanner
	$(GO) go tool govulncheck ./...
	$(TOOL) osv-scanner

secrets: ## Scan the full git history for secrets with gitleaks
	$(TOOL) gitleaks

workflows: ## Lint the workflow files with actionlint and zizmor
	$(TOOL) actionlint
	$(TOOL) zizmor

# Builds the two images, runs the markitdown tests in its image, then lints the Dockerfiles and scans the images.
images: ## Test the markitdown image, lint the Dockerfiles with hadolint and scan the built images with trivy
	$(COMPOSE) build --pull markitdown sandbox-image
	$(COMPOSE) run --rm --no-deps -v ./deploy/markitdown/test_main.py:/app/test_main.py:ro --entrypoint python markitdown -m unittest -v test_main
	$(TOOL) hadolint
	$(TOOL) trivy upmo/markitdown:dev
	$(TOOL) trivy upmo/sandbox:base

commits: ## Check Conventional Commit subjects on this branch
	$(GO) go run ./tools/repolint commits -range $(COMMITS_RANGE)

migrations: ## Refuse new migration files on this branch (edit existing ones before beta)
	$(GO) go run ./tools/repolint migrations -range $(MIGRATIONS_RANGE)

# CI only: the title comes from the pull request event through PR_TITLE.
title: ## Check the pull request title in PR_TITLE (the squash commit subject)
	$(COMPOSE) run --rm -e PR_TITLE go go run ./tools/repolint title

hooks: ## Turn on the versioned git hooks in .githooks (optional, CI runs the same checks)
	git config core.hooksPath .githooks

docs: ## Check markdown style and links
	$(TOOL) markdownlint
	$(TOOL) lychee

canary: ## Run the canary library self-test
	$(TOOL) bash

# A Go fuzz run can end in "context deadline exceeded" when the time limit
# lands mid-input, a known engine flake. Each target retries once: a real
# find is saved under testdata/fuzz and fails the retry on replay.
FUZZ = $(GO) sh -c 'go test -fuzz=$$0 -fuzztime=30s $$1 || go test -fuzz=$$0 -fuzztime=30s $$1'

fuzz: ## Fuzz the safety libraries for 30 seconds each
	$(FUZZ) FuzzWrap ./internal/platform/trustfence
	$(FUZZ) FuzzNeutralizeSlot ./internal/platform/trustfence
	$(FUZZ) FuzzBlockedAddr ./internal/platform/netguard
	$(FUZZ) FuzzVerify ./internal/platform/webhooksig
	$(FUZZ) FuzzVerify ./internal/platform/svcsig
	$(FUZZ) FuzzVerify ./internal/platform/runjwt
	$(FUZZ) FuzzWrite ./internal/platform/writeguard
	$(FUZZ) FuzzParseToken ./internal/identity/domain
	$(FUZZ) FuzzParseLinkToken ./internal/identity/domain
	$(FUZZ) FuzzParseChallengeID ./internal/identity/domain
	$(FUZZ) FuzzCheck ./internal/identity/domain
	$(FUZZ) FuzzParsePHC ./internal/identity/domain

ci: build lint test test-regression canary arch vuln secrets commits migrations docs workflows images ## Run the same steps as CI

web: ## Install, lint, test and build the web app
	$(WEB) sh -c "pnpm install --frozen-lockfile && pnpm lint && pnpm test && pnpm build"

# Separate from `web` and `ci`. Runs in an isolated upmo-e2e-<checkout hash>
# compose project, never the default dev stack; it reuses that project's dev
# server when it is already up. e2e/ensure-web.sh prints the project and URL.
e2e: ## Browser-check theme, shell, task flows and identity screens
	@set -eu; \
	out=$$(sh e2e/ensure-web.sh 3>&1 1>&2) || exit 1; \
	project=$$(printf '%s\n' "$$out" | awk 'NR==1'); \
	url=$$(printf '%s\n' "$$out" | awk 'NR==2'); \
	link=$$(sh e2e/ensure-brain.sh "$$project"); \
	$(COMPOSE) -p "$$project" -f compose.yml -f e2e/web.override.yml --profile e2e run --rm -e WEB_URL="$$url" -e UPMO_LINK="$$link" playwright
