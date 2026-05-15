.PHONY: help tidy lint vet test test-race build up down logs migrate sqlc \
        demo demo-up demo-migrate demo-seed demo-logs demo-down demo-ui

GO          ?= go
GOLANGCI    ?= golangci-lint
SQLC        ?= sqlc
COMPOSE     ?= docker compose -f deploy/docker-compose.yml
DEMO_COMPOSE ?= docker compose -f deploy/docker-compose.demo.yml

DEMO_DB_URL := postgres://contactcentre:demo@localhost:55432/contactcentre?sslmode=disable

help: ## Show this help
	@awk 'BEGIN{FS=":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

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

# ---------------------------------------------------------------------
# Demo orchestrator. Wraps the Phase-1 stack with a stub OIDC + seed
# data so the agent UI is clickable end-to-end without Zitadel or any
# real channel credentials.
#
# `make demo` is the one-shot:
#   1. Build the Go images (gateway, ws, outbound-stub, seed).
#   2. Bring up Postgres + Redis + NATS + MinIO + ClamAV + the
#      services on the cc-demo network.
#   3. Apply migrations.
#   4. Seed one tenant + two agents + five tickets.
#   5. Print the click-through script (login URLs, demo emails).
#
# Then `make demo-ui` runs `npm run dev` in web/agent so the agent UI
# is reachable at http://localhost:3000.
# ---------------------------------------------------------------------

demo: demo-up demo-migrate demo-seed ## Bring up the demo stack end-to-end
	@echo
	@echo "════════════════════════════════════════════════════════════"
	@echo "  DEMO READY"
	@echo "════════════════════════════════════════════════════════════"
	@echo "  Gateway:        http://localhost:8080"
	@echo "  WebSocket:      http://localhost:8081"
	@echo "  MinIO console:  http://localhost:9001  (minioadmin / minioadmin)"
	@echo "  Agent UI:       run \`make demo-ui\` then open http://localhost:3000"
	@echo
	@echo "  Demo logins (use the email; any password is accepted):"
	@echo "    ada@demo.local    (agent)"
	@echo "    bob@demo.local    (supervisor)"
	@echo "    carol@demo.local  (admin)"
	@echo "    diana@demo.local  (dpo)"
	@echo
	@echo "  Postman:        import docs/demo/postman/{collection,environment}.json"
	@echo "  Tear down:      make demo-down"
	@echo "════════════════════════════════════════════════════════════"

demo-up: ## Build images and bring up the demo stack
	$(DEMO_COMPOSE) up -d --build
	@echo "Waiting for Postgres..."
	@until docker exec contactcentre-demo-postgres-1 pg_isready -U contactcentre -d contactcentre >/dev/null 2>&1; do sleep 1; done
	@echo "Waiting for Gateway /healthz..."
	@until curl -sf http://localhost:8080/healthz >/dev/null 2>&1; do sleep 1; done

demo-migrate: ## Apply migrations to the demo database
	@command -v migrate >/dev/null 2>&1 || { \
	  echo "golang-migrate CLI not found; using psql to apply each migration in order"; \
	  for f in migrations/0*_*.up.sql; do \
	    echo "applying $$f"; \
	    PGPASSWORD=demo psql -h localhost -U contactcentre -d contactcentre \
	      -v ON_ERROR_STOP=1 -f $$f >/dev/null; \
	  done; \
	  exit 0; \
	}
	migrate -path migrations -database "$(DEMO_DB_URL)" up

demo-seed: ## Insert demo tenant + agents + tickets
	DATABASE_URL="$(DEMO_DB_URL)" $(GO) run ./cmd/seed

demo-logs: ## Tail demo stack logs
	$(DEMO_COMPOSE) logs -f --tail=200

demo-down: ## Stop and remove the demo stack
	$(DEMO_COMPOSE) down -v

demo-ui: ## Run the agent UI (Next.js dev server) against the demo stack
	@cd web/agent && \
	  AUTH_DEMO_MODE=1 \
	  AUTH_SECRET=demo-secret-only-for-local-dev-do-not-use-in-prod \
	  AUTH_TRUST_HOST=true \
	  NEXT_PUBLIC_GATEWAY_URL=http://localhost:8080 \
	  NEXT_PUBLIC_WS_URL=ws://localhost:8081 \
	  npm run dev

demo-widget: ## Serve web/widget/ at http://localhost:8000 (the demo "company site")
	@echo "serving web/widget/ at http://localhost:8000/demo.html  (Ctrl-C to stop)"
	@cd web/widget && python3 -m http.server 8000

demo-tunnel: ## Print the public cloudflared URL pointing at the gateway
	@for i in $$(seq 1 60); do \
	  url=$$(docker logs contactcentre-demo-cloudflared-1 2>&1 | grep -oE 'https://[a-z0-9-]+\.trycloudflare\.com' | tail -1); \
	  if [ -n "$$url" ]; then \
	    echo "$$url"; \
	    echo "$$url" > .demo-tunnel-url; \
	    echo "(saved to .demo-tunnel-url)"; \
	    exit 0; \
	  fi; \
	  sleep 2; \
	done; \
	echo "tunnel URL not found in cloudflared logs after 2 minutes"; \
	exit 1
