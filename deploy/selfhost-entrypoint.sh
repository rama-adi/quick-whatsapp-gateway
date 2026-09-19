#!/bin/sh
set -eu

pids=""

start_process() {
  "$@" &
  pids="$pids $!"
}

terminate_and_reap() {
  exit_status="$1"
  trap - INT TERM

  for pid in $pids; do
    kill -TERM "$pid" 2>/dev/null || true
  done
  for pid in $pids; do
    wait "$pid" 2>/dev/null || true
  done
  exit "$exit_status"
}

check_processes() {
  for pid in $pids; do
    if ! kill -0 "$pid" 2>/dev/null; then
      # Even a successful exit is unexpected for these long-running services.
      status=0
      wait "$pid" || status=$?
      [ "$status" -ne 0 ] || status=1
      terminate_and_reap "$status"
    fi
  done
}

on_int() { terminate_and_reap 130; }
on_term() { terminate_and_reap 143; }
trap on_int INT
trap on_term TERM

start_process caddy run --config /etc/caddy/Caddyfile --adapter caddyfile

# Keep the frontend on its private listener. Zeabur may inject PORT for the
# public edge; Caddy owns that public port and proxies to this fixed port.
start_process env HOST=127.0.0.1 PORT=3000 node /web/.output/server/index.mjs

start_process api

# The API owns schema migration. Do not start the gateway until migration and
# API readiness have succeeded; this also makes startup failure deterministic.
attempt=0
until wget -qO- http://127.0.0.1:8090/readyz >/dev/null 2>&1; do
  check_processes
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    terminate_and_reap 1
  fi
  sleep 1
done

start_process gateway

while :; do
  check_processes
  sleep 1
done
