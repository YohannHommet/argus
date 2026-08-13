#!/usr/bin/env bash
# scripts/smoke.sh (sole owner: P1-07) — build the argusd image, bring up the
# compose stack from a clean state, poll /readyz, assert /api/v1/meta, then
# tear down (including the named volume) even on failure. Runnable
# repeatedly. Extended by later phases (P2-13) — keep it readable.
#
# Env:
#   ARGUS_HTTP_PORT   host port to publish (default 8080). Some dev machines
#                      and CI runners already have 8080 bound to something
#                      else; this lets smoke runs rebind without touching the
#                      container's own :8080 listener.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose_file="$repo_root/deploy/docker-compose.yml"
image="ghcr.io/yohannhommet/argus:latest"
port="${ARGUS_HTTP_PORT:-8080}"
export ARGUS_HTTP_PORT="$port"
base_url="http://localhost:${port}"

log() { printf '[smoke] %s\n' "$*" >&2; }

cleanup() {
  log "tearing down (docker compose down -v)"
  docker compose -f "$compose_file" down -v --remove-orphans || true
}
trap cleanup EXIT

log "starting from a clean state"
docker compose -f "$compose_file" down -v --remove-orphans

version="$(git -C "$repo_root" describe --tags --always --dirty 2>/dev/null || echo dev)"
commit="$(git -C "$repo_root" rev-parse --short HEAD 2>/dev/null || echo unknown)"

log "building $image (version=$version commit=$commit)"
docker build \
  -f "$repo_root/server/Dockerfile" \
  --build-arg "VERSION=$version" \
  --build-arg "COMMIT=$commit" \
  -t "$image" \
  "$repo_root"

log "docker compose up -d (ARGUS_HTTP_PORT=$port)"
docker compose -f "$compose_file" up -d

log "polling ${base_url}/readyz for up to 60s"
deadline=$((SECONDS + 60))
ready=""
until [ "$SECONDS" -ge "$deadline" ]; do
  if body="$(curl -fsS "${base_url}/readyz" 2>/dev/null)"; then
    if grep -q '"status":"ok"' <<<"$body" && grep -q '"migrations":"current"' <<<"$body"; then
      ready="$body"
      break
    fi
  fi
  sleep 1
done

if [ -z "$ready" ]; then
  log "FAIL: /readyz did not report {\"status\":\"ok\",\"migrations\":\"current\"} within 60s"
  docker compose -f "$compose_file" logs --no-color || true
  exit 1
fi
log "readyz OK: $ready"

log "checking ${base_url}/api/v1/meta"
meta="$(curl -fsS "${base_url}/api/v1/meta")"
log "meta: $meta"

if command -v jq >/dev/null 2>&1; then
  meta_version="$(jq -r '.version // empty' <<<"$meta")"
  retention_days="$(jq -r '.retention_days // empty' <<<"$meta")"

  if [ -z "$meta_version" ]; then
    log "FAIL: /api/v1/meta missing 'version'"
    exit 1
  fi
  if [ "$retention_days" != "90" ]; then
    log "FAIL: /api/v1/meta retention_days expected 90, got '${retention_days:-<missing>}'"
    exit 1
  fi
else
  log "jq not found — degrading to grep-based assertions"
  if ! grep -q '"version"' <<<"$meta"; then
    log "FAIL: /api/v1/meta missing 'version'"
    exit 1
  fi
  if ! grep -q '"retention_days":90' <<<"$meta"; then
    log "FAIL: /api/v1/meta missing retention_days=90"
    exit 1
  fi
fi

# --- P2-13: run the demo sim inside the stack and assert row counts --------
#
# `argusd` (not a separate argus-sim image) has the `sim` subcommand (SPEC
# lead note 7: "two binaries, one implementation"), so `docker compose exec`
# into the already-running argusd container and target its own loopback —
# no extra image, no host networking, and it exercises the exact same wire
# path a real Claude Code process would use. Row counts are read the same
# way, via `docker compose exec postgres psql`, since this script has no
# other route into the compose network's Postgres (no port is published by
# default, deploy/docker-compose.yml's whole point).
sim_sessions=5

log "running the demo sim inside the argusd container (--sessions=${sim_sessions})"
docker compose -f "$compose_file" exec -T argusd /argusd sim \
  --mode=demo \
  --sessions="$sim_sessions" \
  --flush-immediately \
  --tool-use-id-in-hooks=true \
  --target=http://localhost:8080

psql_query() {
  docker compose -f "$compose_file" exec -T postgres psql -U argus -d argus -tAc "$1"
}

log "polling for the sim's rows to land (ingest is async, ARGUS_INGEST_FLUSH)"
deadline=$((SECONDS + 30))
event_count=0
until [ "$SECONDS" -ge "$deadline" ]; do
  event_count="$(psql_query 'SELECT count(*) FROM events' | tr -d '[:space:]')"
  if [ -n "$event_count" ] && [ "$event_count" -gt 0 ] 2>/dev/null; then
    break
  fi
  sleep 1
done

if [ -z "$event_count" ] || [ "$event_count" -le 0 ] 2>/dev/null; then
  log "FAIL: expected events to be > 0 after the demo sim, got '${event_count:-<empty>}'"
  exit 1
fi
log "events: $event_count"

session_count="$(psql_query 'SELECT count(*) FROM sessions' | tr -d '[:space:]')"
if [ -z "$session_count" ] || [ "$session_count" -le 0 ] 2>/dev/null; then
  log "FAIL: expected sessions to be > 0 after the demo sim, got '${session_count:-<empty>}'"
  exit 1
fi
log "sessions: $session_count"

log "PASS"
