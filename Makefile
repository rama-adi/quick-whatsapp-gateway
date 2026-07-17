COMPOSE_DEV = docker compose -f deploy/docker-compose.dev.yml
# Same compose, plus the dockerized gateway (hot-reload via air). Compose waits
# for MySQL + Redis to be healthy before starting the gateway, so one `up` is enough.
COMPOSE_GW  = $(COMPOSE_DEV) --profile gateway-dev
BUF_VERSION = v1.47.2
BUF = go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
PROTO_BREAKING_BRANCH ?= origin/main
PROTO_BREAKING_REF ?= refs/remotes/$(PROTO_BREAKING_BRANCH)

.PHONY: infra-up infra-down infra-reset up up-logs down dev router web migrate build lint test tidy sqlc gen openapi openapi-check proto proto-lint proto-breaking proto-check

infra-up:    ## start mysql + redis only (run the gateway on the host with `make dev`)
	$(COMPOSE_DEV) up -d
infra-down:  ## stop infra (keep data)
	$(COMPOSE_DEV) down
infra-reset: ## stop infra + wipe data
	$(COMPOSE_DEV) down -v

up:          ## one command: build + run gateway + mysql + redis in Docker
	$(COMPOSE_GW) up -d --build
up-logs:     ## follow the dockerized gateway logs (e.g. to read the admin pairing code)
	$(COMPOSE_GW) logs -f gateway-dev
down:        ## stop the full dockerized dev stack (keep data; add `-v` target to wipe)
	$(COMPOSE_GW) down

dev:         ## gateway hot-reload on the HOST under air (run infra-up first; air builds ./cmd/server)
	air
router:      ## run the central router (the public front door) on the HOST (run infra-up first)
	go run ./cmd/router
web:         ## frontend dev server (HMR)
	cd web && pnpm dev

migrate:     ## apply DB migrations (the gateway binary embeds golang-migrate; no standalone CLI)
	go run ./cmd/server migrate up

build:       ## production image
	docker build -t whatsmeow-gateway -f deploy/Dockerfile .
lint:
	golangci-lint run
test:
	go test ./...
tidy:
	go mod tidy && cd web && pnpm install
sqlc:        ## generate typed MySQL store queries from migrations + internal/store/queries
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
proto:       ## generate committed public and private gRPC Go code with pinned Buf plugins
	$(BUF) generate
proto-lint:  ## lint protobuf contracts
	$(BUF) lint
proto-breaking: ## check compatibility against PROTO_BREAKING_BRANCH once it has a protobuf baseline
	@if git ls-tree -r --name-only '$(PROTO_BREAKING_BRANCH)' -- proto | grep -q '\.proto$$'; then \
		$(BUF) breaking --against '.git\#ref=$(PROTO_BREAKING_REF)'; \
	else \
		echo "No protobuf baseline on $(PROTO_BREAKING_BRANCH); skipping breaking check until this scaffold lands."; \
	fi
proto-check: proto-lint ## regenerate in a temporary directory and compare committed Go bindings
	@tmp_dir=$$(mktemp -d); \
	trap 'rm -r "$$tmp_dir"' EXIT; \
	$(BUF) generate --output "$$tmp_dir"; \
	diff -ru gen "$$tmp_dir/gen"
openapi:     ## generate the OpenAPI contract from the shared Go types (code-first, D11)
	go run ./cmd/genopenapi docs/openapi.yaml
openapi-check: openapi ## CI drift guard: fail if docs/openapi.yaml is stale vs the Go types
	git diff --exit-code docs/openapi.yaml
gen: openapi ## regen the contract + typed API client + docs pages (run after changing API Go types)
	cd web && pnpm gen:api && pnpm docs:openapi
