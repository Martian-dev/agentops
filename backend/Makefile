.PHONY: help build run test clean migrate-up migrate-down migrate-status docker-build docker-run compose-up compose-down compose-logs

# Variables
APP_NAME := agentops-api
GO := go
DOCKER_IMAGE := agentops-backend:latest
DATABASE_URL ?= postgres://agentops:agentops@localhost:5432/agentops?sslmode=disable
COMPOSE_FILE := ../infra/docker-compose.yml
COMPOSE := docker compose -f $(COMPOSE_FILE)

help: ## Display this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

build: ## Build the application
	$(GO) build -o $(APP_NAME) ./cmd/api
	@echo "✓ Built $(APP_NAME)"

run: build ## Build and run the application
	DATABASE_URL="$(DATABASE_URL)" ./$(APP_NAME)

dev: ## Run with live reload (requires air)
	@command -v air >/dev/null 2>&1 || { echo "Installing air..."; go install github.com/cosmtrek/air@latest; }
	air

test: ## Run tests
	$(GO) test -v -cover ./...

test-coverage: ## Run tests with coverage report
	$(GO) test -v -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "✓ Coverage report generated: coverage.html"
	
clean: ## Clean build artifacts
	$(GO) clean
	rm -f $(APP_NAME)
	rm -f coverage.out coverage.html
	@echo "✓ Cleaned"

deps: ## Download and verify dependencies
	$(GO) mod download
	$(GO) mod verify
	@echo "✓ Dependencies verified"

tidy: ## Tidy dependencies
	$(GO) mod tidy
	@echo "✓ Dependencies tidied"

fmt: ## Format code
	$(GO) fmt ./...
	@echo "✓ Code formatted"

lint: ## Run linter (requires golangci-lint)
	@command -v golangci-lint >/dev/null 2>&1 || { echo "Installing golangci-lint..."; go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; }
	golangci-lint run ./...

migrate-up: ## Run database migrations (up)
	@command -v migrate >/dev/null 2>&1 || { echo "Please install migrate CLI: https://github.com/golang-migrate/migrate/tree/master/cmd/migrate"; exit 1; }
	@$(COMPOSE) up -d postgres
	@until $(COMPOSE) exec -T postgres pg_isready -U agentops >/dev/null 2>&1; do echo "Waiting for Postgres on localhost:5432..."; sleep 2; done
	migrate -path ./migrations -database "$(DATABASE_URL)" up
	@echo "✓ Migrations applied"

migrate-down: ## Run database migrations (down)
	@command -v migrate >/dev/null 2>&1 || { echo "Please install migrate CLI: https://github.com/golang-migrate/migrate/tree/master/cmd/migrate"; exit 1; }
	@$(COMPOSE) up -d postgres
	@until $(COMPOSE) exec -T postgres pg_isready -U agentops >/dev/null 2>&1; do echo "Waiting for Postgres on localhost:5432..."; sleep 2; done
	migrate -path ./migrations -database "$(DATABASE_URL)" down
	@echo "✓ Migrations rolled back"

migrate-status: ## Show migration status
	@command -v migrate >/dev/null 2>&1 || { echo "Please install migrate CLI: https://github.com/golang-migrate/migrate/tree/master/cmd/migrate"; exit 1; }
	@$(COMPOSE) up -d postgres
	@until $(COMPOSE) exec -T postgres pg_isready -U agentops >/dev/null 2>&1; do echo "Waiting for Postgres on localhost:5432..."; sleep 2; done
	migrate -path ./migrations -database "$(DATABASE_URL)" version

docker-build: ## Build Docker image
	docker build -t $(DOCKER_IMAGE) .
	@echo "✓ Built Docker image: $(DOCKER_IMAGE)"

docker-run: docker-build ## Build and run Docker container
	docker run -e DATABASE_URL="postgres://agentops:agentops@postgres:5432/agentops?sslmode=disable" -p 8080:8080 $(DOCKER_IMAGE)

compose-up: ## Start full stack with Docker Compose
	$(COMPOSE) up -d
	@echo "✓ Docker Compose stack started"

compose-down: ## Stop Docker Compose stack
	$(COMPOSE) down
	@echo "✓ Docker Compose stack stopped"

compose-logs: ## View Docker Compose logs
	$(COMPOSE) logs -f

all: clean deps fmt test build ## Run all checks and build
	@echo "✓ All checks passed"
