# syntax=docker/dockerfile:1

# --- Stage 1: build the React Router SPA to static assets ---
FROM node:22-alpine AS web
WORKDIR /web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build                       # SPA -> /web/build/client

# --- Stage 2: build the Go binary (pure-Go, no CGO) ---
# Pinned to golang:1.26-alpine to match go.mod's "go 1.26.4" (required by Authula).
# Both the SQLite keystore fallback (pure-Go modernc.org/sqlite) and Authula's
# imported packages build with CGO_ENABLED=0, so the binary is fully static with
# no gcc/musl-dev needed.
FROM golang:1.26-alpine AS api
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/build/client ./internal/http/static/dist
RUN CGO_ENABLED=0 go build -o /out/gateway ./cmd/server

# --- Stage 3: minimal runtime ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=api /out/gateway /usr/local/bin/gateway
EXPOSE 8080
ENTRYPOINT ["gateway"]
