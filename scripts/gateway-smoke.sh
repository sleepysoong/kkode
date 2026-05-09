#!/usr/bin/env bash
set -euo pipefail

addr="${1:-127.0.0.1:41234}"
base="http://$addr"
state_dir="$(mktemp -d "${TMPDIR:-/tmp}/kkode-gateway-smoke.XXXXXX")"
state="$state_dir/state.db"
log="$state_dir/gateway.log"
pid=""

cleanup() {
  if [[ -n "$pid" ]] && kill -0 "$pid" >/dev/null 2>&1; then
    kill "$pid" >/dev/null 2>&1 || true
    wait "$pid" >/dev/null 2>&1 || true
  fi
  rm -rf "$state_dir"
}
trap cleanup EXIT

probe() {
  local path="$1"
  local pattern="$2"
  local body
  if ! body="$(curl -fsS --max-time 3 "$base$path")"; then
    cat "$log" >&2
    exit 1
  fi
  if ! grep -q "$pattern" <<<"$body"; then
    printf 'unexpected response from %s:\n%s\n' "$path" "$body" >&2
    cat "$log" >&2
    exit 1
  fi
}

go run ./cmd/kkode-gateway -addr "$addr" -state "$state" -version smoke >"$log" 2>&1 &
pid="$!"

for _ in $(seq 1 240); do
  if curl -fsS --max-time 1 "$base/healthz" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$pid" >/dev/null 2>&1; then
    cat "$log" >&2
    exit 1
  fi
  sleep 0.25
done

probe "/healthz" '"ok":true'
probe "/readyz" '"ready":true'
probe "/api/v1" '"operations"'
probe "/api/v1/openapi.yaml" 'openapi:'
probe "/api/v1/capabilities" '"features"'

echo "OK"
