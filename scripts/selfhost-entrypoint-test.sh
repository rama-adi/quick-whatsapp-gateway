#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
entrypoint="$repo_root/deploy/selfhost-entrypoint.sh"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT INT TERM

make_stubs() {
  case_dir="$1"
  mkdir -p "$case_dir/bin"

  for command in api node caddy; do
    cat >"$case_dir/bin/$command" <<'EOF'
#!/bin/sh
name=$(basename "$0")
echo "$$" >"$STUB_STATE/$name.pid"
  trap 'echo "$name-term" >>"$STUB_STATE/events"; exit 0' TERM
while :; do sleep 1; done
EOF
    chmod +x "$case_dir/bin/$command"
  done

  cat >"$case_dir/bin/gateway" <<'EOF'
#!/bin/sh
echo "$$" >"$STUB_STATE/gateway.pid"
if [ "${STUB_GATEWAY_EXIT:-}" != "" ]; then
  exit "$STUB_GATEWAY_EXIT"
fi
trap 'echo "gateway-term" >>"$STUB_STATE/events"; exit 0' TERM
while :; do sleep 1; done
EOF
  chmod +x "$case_dir/bin/gateway"

  cat >"$case_dir/bin/wget" <<'EOF'
#!/bin/sh
exit 0
EOF
  chmod +x "$case_dir/bin/wget"
}

wait_for_file() {
  file="$1"
  attempts=0
  until [ -f "$file" ]; do
    attempts=$((attempts + 1))
    [ "$attempts" -lt 100 ] || return 1
    sleep 0.02
  done
}

term_case="$tmp_dir/term"
mkdir -p "$term_case"
make_stubs "$term_case"
STUB_STATE="$term_case" PATH="$term_case/bin:$PATH" sh "$entrypoint" &
supervisor_pid=$!
wait_for_file "$term_case/gateway.pid"
kill -TERM "$supervisor_pid"
set +e
wait "$supervisor_pid"
status=$?
set -e
[ "$status" -eq 143 ] || { echo "TERM status: got $status, want 143" >&2; exit 1; }
for child in api node caddy gateway; do
  grep -q "${child}-term" "$term_case/events" || { echo "$child was not terminated" >&2; exit 1; }
done

failure_case="$tmp_dir/failure"
mkdir -p "$failure_case"
make_stubs "$failure_case"
STUB_STATE="$failure_case" STUB_GATEWAY_EXIT=7 PATH="$failure_case/bin:$PATH" sh "$entrypoint" &
supervisor_pid=$!
set +e
wait "$supervisor_pid"
status=$?
set -e
[ "$status" -eq 7 ] || { echo "child failure status: got $status, want 7" >&2; exit 1; }
for child in api node caddy; do
  grep -q "${child}-term" "$failure_case/events" || { echo "$child sibling was not terminated" >&2; exit 1; }
done

echo "selfhost entrypoint supervisor tests passed"
