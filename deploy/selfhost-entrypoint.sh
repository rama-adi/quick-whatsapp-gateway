#!/bin/sh
set -eu

pids=""
gateway_pid=""

start_process() {
  "$@" &
  pids="$pids $!"
}

terminate_and_reap() {
  exit_status="$1"
  trap - INT TERM

  for pid in $pids $gateway_pid; do
    kill -TERM "$pid" 2>/dev/null || true
  done
  for pid in $pids $gateway_pid; do
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

# The gateway is supervised separately. When it exits, Caddy, the API and the
# frontend keep running and the gateway is started again after a delay. A
# gateway that cannot start (for example because its certificate expired and
# it needs a new enrollment token) must not take down the admin API that
# issues that token.
gateway_started_at=0
gateway_retry_at=0
gateway_delay="${GATEWAY_RESTART_DELAY_SECONDS:-5}"
gateway_max_delay="${GATEWAY_RESTART_MAX_DELAY_SECONDS:-60}"
gateway_initial_delay="$gateway_delay"

start_gateway() {
  gateway &
  gateway_pid=$!
  gateway_started_at=$(date +%s)
}

check_gateway() {
  now=$(date +%s)
  if [ -n "$gateway_pid" ]; then
    if kill -0 "$gateway_pid" 2>/dev/null; then
      return
    fi
    status=0
    wait "$gateway_pid" || status=$?
    gateway_pid=""
    # A gateway that ran for a while failed for a new reason; start the delay over.
    if [ $((now - gateway_started_at)) -ge "$gateway_max_delay" ]; then
      gateway_delay="$gateway_initial_delay"
    fi
    echo "selfhost-entrypoint: gateway exited with status $status; restarting in ${gateway_delay}s" >&2
    gateway_retry_at=$((now + gateway_delay))
    gateway_delay=$((gateway_delay * 2))
    if [ "$gateway_delay" -gt "$gateway_max_delay" ]; then
      gateway_delay="$gateway_max_delay"
    fi
    return
  fi
  if [ "$now" -ge "$gateway_retry_at" ]; then
    start_gateway
  fi
}

start_gateway

while :; do
  check_processes
  check_gateway
  sleep 1
done
