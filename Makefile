COMPOSE_DEV = docker compose -f deploy/docker-compose.dev.yml
# Same compose, plus the dockerized gateway (hot-reload via air). Compose waits
# for MySQL + Redis to be healthy before starting the gateway, so one `up` is enough.
COMPOSE_GW  = $(COMPOSE_DEV) --profile gateway-dev

.PHONY: infra-up infra-down infra-reset up up-logs down dev web migrate build lint test tidy gen openapi openapi-check

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

dev:         ## backend hot-reload on the HOST (run infra-up first)
	air
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
openapi:     ## generate the OpenAPI spec from the shared Go types (code-first, D11)
	go run ./cmd/genopenapi docs/openapi.gen.yaml
openapi-check: openapi ## CI drift guard: fail if the generated spec is stale
	git diff --exit-code docs/openapi.gen.yaml
gen:         ## regen typed API client + docs pages from openapi.yaml
	cd web && pnpm gen:api && pnpm docs:openapi
