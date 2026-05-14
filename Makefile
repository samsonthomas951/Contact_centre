.PHONY: help tidy lint vet test test-race build up down logs migrate sqlc

GO          ?= go
GOLANGCI    ?= golangci-lint
SQLC        ?= sqlc
COMPOSE     ?= docker compose -f deploy/docker-compose.yml

help: ## Show this help
	@awk 'BEGIN{FS=":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

tidy: ## go mod tidy
	$(GO) mod tidy

vet: ## go vet
	$(GO) vet ./...

lint: ## golangci-lint run
	$(GOLANGCI) run ./...

test: ## go test
	$(GO) test ./...

test-race: ## go test -race -cover
	$(GO) test -race -cover ./...

build: ## build all binaries under cmd/
	$(GO) build -trimpath -o bin/ ./cmd/...

sqlc: ## regenerate sqlc code
	$(SQLC) generate

migrate: ## run pending migrations (requires DATABASE_URL)
	migrate -path migrations -database "$$DATABASE_URL" up

up: ## start the local stack
	$(COMPOSE) up -d

down: ## stop the local stack
	$(COMPOSE) down

logs: ## tail compose logs
	$(COMPOSE) logs -f --tail=200
