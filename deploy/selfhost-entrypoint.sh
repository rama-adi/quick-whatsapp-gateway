#!/bin/sh
set -eu

pids=""

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

on_int() { terminate_and_reap 130; }
on_term() { terminate_and_reap 143; }
trap on_int INT
trap on_term TERM

node /web/.output/server/index.mjs &
pids="$pids $!"

api &
api_pid=$!
pids="$pids $api_pid"

# The API owns schema migration. Do not start the gateway until migration and
# API readiness have succeeded; this also makes startup failure deterministic.
attempt=0
until wget -qO- http://127.0.0.1:8090/readyz >/dev/null 2>&1; do
  if ! kill -0 "$api_pid" 2>/dev/null; then
    set +e
    wait "$api_pid"
    status=$?
    set -e
    [ "$status" -ne 0 ] || status=1
    terminate_and_reap "$status"
  fi
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    terminate_and_reap 1
  fi
  sleep 1
done

gateway &
pids="$pids $!"

while :; do
  for pid in $pids; do
    if ! kill -0 "$pid" 2>/dev/null; then
      set +e
      wait "$pid"
      status=$?
      set -e
      [ "$status" -ne 0 ] || status=1
      terminate_and_reap "$status"
    fi
  done
  sleep 1
done
