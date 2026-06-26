COMPOSE_DEV = docker compose -f deploy/docker-compose.dev.yml

.PHONY: infra-up infra-down infra-reset dev web migrate build lint test tidy gen

infra-up:    ## start mysql + redis
	$(COMPOSE_DEV) up -d
infra-down:  ## stop infra (keep data)
	$(COMPOSE_DEV) down
infra-reset: ## stop infra + wipe data
	$(COMPOSE_DEV) down -v

dev:         ## backend hot-reload (run infra-up first)
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
gen:         ## regen typed API client from openapi.yaml
	cd web && pnpm openapi
