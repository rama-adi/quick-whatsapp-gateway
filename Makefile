COMPOSE_DEV = docker compose -f deploy/docker-compose.dev.yml
# Same compose, plus the dockerized gateway (hot-reload via air).
COMPOSE_GW  = $(COMPOSE_DEV) --profile api --profile gateway-dev
BUF_VERSION = v1.47.2
BUF = go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
PROTO_BREAKING_BRANCH ?= origin/main
PROTO_BREAKING_REF ?= refs/remotes/$(PROTO_BREAKING_BRANCH)

.PHONY: infra-up infra-down infra-reset up up-logs down dev api web marketing migrate build build-api lint test api-e2e api-e2e-coverage-check tidy sqlc gen openapi openapi-check proto proto-lint proto-breaking proto-check

infra-up:    ## start mysql + redis only (run the gateway on the host with `make dev`)
	$(COMPOSE_DEV) up -d
infra-down:  ## stop infra (keep data)
	$(COMPOSE_DEV) down
infra-reset: ## stop infra + wipe data
	$(COMPOSE_DEV) down -v

up:          ## build + run API + dev gateway + mysql + redis in Docker
	$(COMPOSE_GW) up -d --build
up-logs:     ## follow the dockerized gateway logs (e.g. to read the admin pairing code)
	$(COMPOSE_GW) logs -f gateway-dev
down:        ## stop the full dockerized dev stack (keep data; add `-v` target to wipe)
	$(COMPOSE_GW) down

dev:         ## gateway hot-reload on the HOST under air (run infra-up first; air builds backend/cmd/gateway)
	air -c backend/.air.toml
api:         ## run the API (the public front door) on the HOST (run infra-up first)
	go -C backend build -o ../.dev/api ./cmd/api
	./.dev/api
web:         ## frontend dev server (HMR)
	cd web && pnpm dev

marketing:   ## marketing + operator/developer docs dev server
	cd site-marketing && pnpm dev

migrate:     ## apply API-owned WA schema migrations
	go -C backend build -o ../.dev/migrate ./cmd/migrate
	./.dev/migrate up

build:       ## production gateway image
	docker build -t whatsmeow-gateway -f deploy/Dockerfile .
build-api:   ## production API/control-plane image
	docker build -t whatsmeow-api -f deploy/Dockerfile.api .
lint:
	cd backend && golangci-lint run
test:
	go -C backend test ./...
	sh scripts/selfhost-entrypoint-test.sh
api-e2e-coverage-check: ## fail when a public API operation or route lacks an E2E coverage entry
	python3 scripts/check-api-e2e-coverage.py
api-e2e: api-e2e-coverage-check ## run the Docker-backed API/gateway process test
	QWG_E2E=1 go -C backend test ./cmd/api -run '^TestOutboundE2E$$' -count=1 -v
tidy:
	go -C backend mod tidy
	cd web && pnpm install
sqlc:        ## generate typed MySQL store queries from backend migrations + store queries
	cd backend && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
proto:       ## generate committed public and private gRPC Go code with pinned Buf plugins
	cd backend && $(BUF) generate
proto-lint:  ## lint protobuf contracts
	cd backend && $(BUF) lint
proto-breaking: ## check compatibility against PROTO_BREAKING_BRANCH once it has a protobuf baseline
	@if git ls-tree -r --name-only '$(PROTO_BREAKING_BRANCH)' -- backend/proto | grep -q '\.proto$$'; then \
		cd backend && $(BUF) breaking --against '../.git#ref=$(PROTO_BREAKING_REF),subdir=backend'; \
	elif git ls-tree -r --name-only '$(PROTO_BREAKING_BRANCH)' -- proto | grep -q '\.proto$$'; then \
		cd backend && $(BUF) breaking --against '../.git#ref=$(PROTO_BREAKING_REF)'; \
	else \
		echo "No protobuf baseline on $(PROTO_BREAKING_BRANCH); skipping breaking check until this scaffold lands."; \
	fi
proto-check: proto-lint ## regenerate in a temporary directory and compare committed Go bindings
	@tmp_dir=$$(mktemp -d); \
	trap 'rm -r "$$tmp_dir"' EXIT; \
	(cd backend && $(BUF) generate --output "$$tmp_dir") && \
	diff -ru backend/gen "$$tmp_dir/gen"
openapi:     ## generate the OpenAPI contract from the shared Go types (code-first, D11)
	go -C backend run ./cmd/genopenapi ../docs/openapi.yaml
openapi-check: openapi ## CI drift guard: fail if docs/openapi.yaml is stale vs the Go types
	git diff --exit-code docs/openapi.yaml
gen: openapi ## regen the contract + typed API client + docs pages (run after changing API Go types)
	cd web && pnpm gen:api && pnpm docs:openapi
	cd site-marketing && pnpm docs:openapi
