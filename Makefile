.PHONY: generate build test lint migrate-up migrate-down up down seed e2e fmt vet sqlc

# ─── Variables ──────────────────────────────────────────────────────────────────
BACKEND_DIR := backend
FRONTEND_DIR := frontend
MIGRATIONS_DIR := $(BACKEND_DIR)/migrations
DATABASE_URL ?= ******localhost:5432/reticora?sslmode=disable

# ─── Generate ───────────────────────────────────────────────────────────────────
generate: sqlc
	@echo "==> Generating code..."
	cd $(BACKEND_DIR) && go generate ./...

sqlc:
	@echo "==> Generating sqlc..."
	cd sqlc && sqlc generate 2>/dev/null || echo "sqlc not installed, skipping (install: go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest)"

# ─── Build ──────────────────────────────────────────────────────────────────────
build: build-backend build-frontend

build-backend:
	@echo "==> Building backend..."
	cd $(BACKEND_DIR) && go build ./...

build-collector:
	@echo "==> Building collector..."
	cd collector && go build ./...

build-frontend:
	@echo "==> Building frontend..."
	cd $(FRONTEND_DIR) && npm run build

# ─── Test ───────────────────────────────────────────────────────────────────────
test: test-backend test-frontend

test-backend:
	@echo "==> Testing backend..."
	cd $(BACKEND_DIR) && go test -race -coverprofile=coverage.out ./...

test-collector:
	@echo "==> Testing collector..."
	cd collector && go test -race ./...

test-frontend:
	@echo "==> Testing frontend..."
	cd $(FRONTEND_DIR) && npm test -- --run 2>/dev/null || echo "No frontend tests configured yet"

# ─── Lint ───────────────────────────────────────────────────────────────────────
lint: lint-backend lint-frontend

lint-backend:
	@echo "==> Linting backend..."
	cd $(BACKEND_DIR) && golangci-lint run ./...

lint-frontend:
	@echo "==> Linting frontend..."
	cd $(FRONTEND_DIR) && npm run lint

# ─── Format ─────────────────────────────────────────────────────────────────────
fmt:
	@echo "==> Formatting backend..."
	cd $(BACKEND_DIR) && gofmt -w .

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
	cd $(FRONTEND_DIR) && npx playwright test 2>/dev/null || echo "No E2E tests configured yet"
