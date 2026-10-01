.PHONY: generate build test test-backend test-collector test-edgecore test-db test-frontend lint lint-backend lint-frontend lint-collector lint-edgecore check-golangci-lint migrate-up migrate-down migrate-roundtrip up down seed e2e fmt vet oapi-codegen generate-api-client check-api-client

# ─── Variables ──────────────────────────────────────────────────────────────────
BACKEND_DIR := backend
FRONTEND_DIR := frontend
COLLECTOR_DIR := collector
EDGECORE_DIR := edgecore
MIGRATIONS_DIR := $(BACKEND_DIR)/migrations
DATABASE_URL ?= ******localhost:5432/reticora?sslmode=disable

# ─── Generate ───────────────────────────────────────────────────────────────────
generate: oapi-codegen generate-api-client
	@echo "==> Generating code..."
	cd $(BACKEND_DIR) && go generate ./...

generate-api-client:
	@echo "==> Generating TypeScript API client from api/openapi.yaml..."
	cd $(FRONTEND_DIR) && npm run generate:api

check-api-client:
	@echo "==> Checking the generated TypeScript client is up to date..."
	cd $(FRONTEND_DIR) && npm run generate:api:check

oapi-codegen:
	@echo "==> Generating OpenAPI server..."
	oapi-codegen --config oapi-codegen.yaml api/openapi.yaml 2>/dev/null || echo "oapi-codegen not installed, skipping (install: go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest)"

# ─── Build ──────────────────────────────────────────────────────────────────────
build: build-backend build-frontend

build-backend:
	@echo "==> Building backend..."
	cd $(BACKEND_DIR) && go build ./...

build-collector:
	@echo "==> Building collector..."
	cd $(COLLECTOR_DIR) && go build ./...

build-frontend:
	@echo "==> Building frontend..."
	cd $(FRONTEND_DIR) && npm run build

# ─── Test ───────────────────────────────────────────────────────────────────────
test: test-backend test-collector test-edgecore test-frontend

test-backend:
	@echo "==> Testing backend..."
	cd $(BACKEND_DIR) && go test -race -coverprofile=coverage.out ./...

test-collector:
	@echo "==> Testing collector..."
	cd $(COLLECTOR_DIR) && go test -race ./...

test-edgecore:
	@echo "==> Testing edgecore..."
	cd $(EDGECORE_DIR) && go test -race ./...

# Runs every backend test against a migrated PostgreSQL, like the CI
# migrations job: all DB tests activate themselves through TEST_DATABASE_URL.
# Packages run sequentially because they share the database.
TEST_DATABASE_URL ?= $(DATABASE_URL)
test-db:
	@echo "==> Testing backend against PostgreSQL..."
	cd $(BACKEND_DIR) && TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -count=1 -p 1 -v ./...

test-frontend:
	@echo "==> Testing frontend..."
	cd $(FRONTEND_DIR) && npx vitest run

# ─── Lint ───────────────────────────────────────────────────────────────────────
# Must match GOLANGCI_LINT_VERSION in .github/workflows/ci.yml (CI checks this).
GOLANGCI_LINT_VERSION := v2.14.0
GOLANGCI_LINT ?= golangci-lint
# All go.work modules share the backend configuration.
GOLANGCI_LINT_RUN := $(GOLANGCI_LINT) run --config $(CURDIR)/$(BACKEND_DIR)/.golangci.yml ./...

lint: lint-backend lint-frontend lint-collector lint-edgecore

check-golangci-lint:
	@$(GOLANGCI_LINT) version --short 2>/dev/null | grep -qx '$(patsubst v%,%,$(GOLANGCI_LINT_VERSION))' || { \
		echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required. Install it with:"; \
		echo "  curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b \$$(go env GOPATH)/bin $(GOLANGCI_LINT_VERSION)"; \
		exit 1; }

lint-backend: check-golangci-lint
	@echo "==> Linting backend..."
	cd $(BACKEND_DIR) && $(GOLANGCI_LINT_RUN)

lint-frontend:
	@echo "==> Linting frontend..."
	cd $(FRONTEND_DIR) && npm run lint
	cd $(FRONTEND_DIR) && npx prettier --check .

lint-collector: check-golangci-lint
	@echo "==> Linting collector..."
	cd $(COLLECTOR_DIR) && $(GOLANGCI_LINT_RUN)

lint-edgecore: check-golangci-lint
	@echo "==> Linting edgecore..."
	cd $(EDGECORE_DIR) && $(GOLANGCI_LINT_RUN)

# ─── Format ─────────────────────────────────────────────────────────────────────
fmt:
	@echo "==> Formatting backend..."
	cd $(BACKEND_DIR) && gofmt -w .
	@echo "==> Formatting frontend..."
	cd $(FRONTEND_DIR) && npx prettier --write .

# ─── Vet ────────────────────────────────────────────────────────────────────────
vet:
	@echo "==> Vetting backend..."
	cd $(BACKEND_DIR) && go vet ./...

# ─── Migrations ─────────────────────────────────────────────────────────────────
migrate-up:
	@echo "==> Running migrations up..."
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

migrate-down:
	@echo "==> Running migrations down..."
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

migrate-roundtrip:
	@echo "==> Verifying migrations apply and revert cleanly..."
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down -all
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

migrate-create:
	@echo "==> Creating migration: $(name)"
	migrate create -ext sql -dir $(MIGRATIONS_DIR) -seq $(name)

# ─── Docker Compose ─────────────────────────────────────────────────────────────
up:
	docker compose up -d

down:
	docker compose down

# ─── Seed ───────────────────────────────────────────────────────────────────────
seed:
	@echo "==> Seeding database..."
	psql "$(DATABASE_URL)" -f $(BACKEND_DIR)/migrations/seed.sql 2>/dev/null || echo "No seed file found"

# ─── E2E ────────────────────────────────────────────────────────────────────────
e2e:
	@echo "==> Running E2E tests..."
	cd $(FRONTEND_DIR) && npx playwright test
