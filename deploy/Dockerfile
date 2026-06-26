# syntax=docker/dockerfile:1
# GATEWAY image (Go / whatsmeow). Build context is the repo root:
#   docker build -t whatsmeow-gateway -f deploy/Dockerfile .

# --- Stage 1: build the Go binary (pure-Go, no CGO) ---
# Pinned to golang:1.26-alpine to match go.mod. The SQLite keystore uses the
# pure-Go modernc.org/sqlite driver, so CGO_ENABLED=0 yields a fully static
# binary with no gcc/musl-dev needed. The gateway no longer embeds the SPA —
# the frontend ships as a separate image (deploy/Dockerfile.web).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/gateway ./cmd/server

# --- Stage 2: minimal runtime ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/gateway /usr/local/bin/gateway
VOLUME ["/data/keystore"]            # SQLite whatsmeow keystore persists here
EXPOSE 8080
ENTRYPOINT ["gateway"]
