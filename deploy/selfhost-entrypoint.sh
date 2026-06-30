#!/bin/sh
set -eu

pids=""

terminate() {
  for pid in $pids; do
    kill "$pid" 2>/dev/null || true
  done
  wait || true
}

trap terminate INT TERM

router &
pids="$pids $!"

gateway &
pids="$pids $!"

node /web/.output/server/index.mjs &
pids="$pids $!"

while :; do
  for pid in $pids; do
    if ! kill -0 "$pid" 2>/dev/null; then
      terminate
      exit 1
    fi
  done
  sleep 1
done
