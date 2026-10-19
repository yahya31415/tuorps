ROOT := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))

GO       ?= go
TOOL     := $(GO) tool -modfile=$(ROOT)/tools/go.mod
COVER    ?= coverage.out
FUZZTIME ?= 10s
COMPOSE  := docker compose -f compose.yaml -f compose.dev.yaml

.DEFAULT_GOAL := help
.PHONY: help check fmt fmt-check vet staticcheck gosec vuln test cover-gate fuzz-smoke bench \
	build images up down logs dev-secrets storage-init migrate-up migrate-down migrate-status red green

help: ## List targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

## Quality gate (CLAUDE.md §5) ---------------------------------------------

check: fmt-check vet staticcheck gosec vuln test cover-gate ## Everything CI runs; must pass before a task is done

fmt: ## Format all Go code
	gofmt -w .

fmt-check: ## Fail if any Go file is unformatted
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi

vet: ## go vet
	$(GO) vet ./...

staticcheck: ## staticcheck (pinned in tools/go.mod)
	$(TOOL) staticcheck ./...

gosec: ## gosec security scan (pinned)
	$(TOOL) gosec -quiet -exclude-generated -exclude-dir=tools ./...

vuln: ## govulncheck (pinned)
	$(TOOL) govulncheck ./...

test: ## All tests with the race detector and cross-package coverage
	$(GO) test -race -count=1 -coverpkg=./... -coverprofile=$(COVER) ./...

cover-gate: ## Enforce core/ >= 90% and overall >= 80% on $(COVER)
	@$(ROOT)/scripts/coverage-gate.sh $(COVER) 90 80

fuzz-smoke: ## Run every fuzz target for $(FUZZTIME)
	@set -e; for pkg in $$($(GO) list ./...); do \
		for fz in $$($(GO) test -list '^Fuzz' $$pkg | grep '^Fuzz' || true); do \
			echo "fuzz $$pkg $$fz"; $(GO) test -run '^$$' -fuzz "^$$fz\$$" -fuzztime $(FUZZTIME) $$pkg; \
		done; \
	done

bench: ## Run benchmarks
	$(GO) test -run '^$$' -bench . -benchmem ./...

## Build and run ----------------------------------------------------------

build: ## Build static binaries into bin/
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o bin/ ./cmd/...

images: ## Build the api and worker images
	docker build --build-arg CMD=api -t tuorps-api:dev .
	docker build --build-arg CMD=worker -t tuorps-worker:dev .

dev-secrets: ## Generate local Docker secrets in secrets/
	@$(ROOT)/scripts/dev-secrets.sh

up: ## Start the full stack and wait until healthy
	$(COMPOSE) up -d --build --wait

down: ## Stop the stack (keeps volumes)
	$(COMPOSE) down

logs: ## Follow stack logs
	$(COMPOSE) logs -f

storage-init: ## Bootstrap the Garage layout, key and media bucket
	@$(ROOT)/scripts/storage-init.sh

migrate-up: ## Apply migrations in the stack
	$(COMPOSE) run --rm migrate migrate up

migrate-down: ## Roll back one migration in the stack
	$(COMPOSE) run --rm migrate migrate down

migrate-status: ## Show migration status in the stack
	$(COMPOSE) run --rm migrate migrate status

## TDD guard (CLAUDE.md §1) -----------------------------------------------

red: ## Record a failing test: make red PKG=./internal/x/core RUN='^TestName$$'
	@$(TDD_SCRIPT_SHELL) $(ROOT)/scripts/tdd.sh red '$(value PKG)' '$(value RUN)'

green: ## Rerun the recorded test + its package with -race; clear .tdd/red when green
	@$(TDD_SCRIPT_SHELL) $(ROOT)/scripts/tdd.sh green
