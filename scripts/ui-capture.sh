#!/usr/bin/env bash
# scripts/ui-capture.sh — the Phase-4 screenshot harness the visual gauntlet
# calls. One argument: the output directory.
#
#   bash scripts/ui-capture.sh /tmp/argus-shots
#
# What it does, in order:
#   1. brings its own compose project up from a clean state on a non-default
#      host port (8080 and 5432 are commonly taken on dev boxes / runners),
#      building argusd from the working tree — server/Dockerfile compiles the
#      Vue SPA and embeds it, so the screenshots are of the *embedded*
#      assets, not a dev server;
#   2. waits for /readyz;
#   3. seeds deterministic demo traffic (`sim --mode=demo --seed=42`) and
#      waits until the read API actually reports the sessions;
#   4. drives headless chromium over the six routes at 1440x900, dark theme,
#      writing PNGs plus a capture.json manifest into $1.
#
# Idempotent: every run starts with `down -v`, so a leftover stack or a
# half-seeded database from an interrupted run cannot leak into the next
# capture. Teardown runs on exit, including on failure.
#
# Env:
#   ARGUS_CAPTURE_PORT   host port for argusd (default 18080)
#   ARGUS_CAPTURE_KEEP   set to 1 to leave the stack running for debugging
#   ARGUS_CAPTURE_SEED   sim seed (default 42 — deterministic, >= 20 sessions)
set -euo pipefail

if [[ $# -lt 1 || -z "${1:-}" ]]; then
  echo "usage: bash scripts/ui-capture.sh <output-dir>" >&2
  exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out_dir="$(mkdir -p "$1" && cd "$1" && pwd)"

port="${ARGUS_CAPTURE_PORT:-18080}"
seed="${ARGUS_CAPTURE_SEED:-42}"
base_url="http://localhost:${port}"
project_name="argus-capture"
min_sessions=20

export ARGUS_HTTP_PORT="$port"

dc() {
  docker compose -p "$project_name" \
    -f "$repo_root/deploy/docker-compose.yml" \
    -f "$repo_root/deploy/docker-compose.capture.yml" "$@"
}

log() { printf '[ui-capture] %s\n' "$*" >&2; }

cleanup() {
  if [[ "${ARGUS_CAPTURE_KEEP:-0}" == "1" ]]; then
    log "ARGUS_CAPTURE_KEEP=1 — leaving the stack up at ${base_url}"
    log "tear it down with: docker compose -p ${project_name} -f deploy/docker-compose.yml -f deploy/docker-compose.capture.yml down -v"
    return
  fi
  log "tearing down (docker compose -p ${project_name} down -v)"
  dc down -v --remove-orphans || true
}
trap cleanup EXIT

# The host port is shared even across compose projects, so fail loudly here
# rather than let `up -d` collide with a live stack or a second capture run.
if (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
  exec 3>&- 3<&- || true
  log "FAIL: host port ${port} is already in use. Set ARGUS_CAPTURE_PORT to a free port, e.g. ARGUS_CAPTURE_PORT=18081 bash scripts/ui-capture.sh $1"
  exit 1
fi

log "output dir: ${out_dir}"

log "starting from a clean state (project=${project_name})"
dc down -v --remove-orphans >/dev/null 2>&1 || true

log "building argus:ui-capture (compiles + embeds the SPA; first run is slow)"
VERSION="$(git -C "$repo_root" describe --tags --always --dirty 2>/dev/null || echo ui-capture)" \
COMMIT="$(git -C "$repo_root" rev-parse --short HEAD 2>/dev/null || echo unknown)" \
  dc build argusd

log "bringing the stack up on ${base_url}"
dc up -d

log "waiting for ${base_url}/readyz"
for attempt in $(seq 1 90); do
  if curl -fsS "${base_url}/readyz" >/dev/null 2>&1; then
    log "ready after ${attempt} attempt(s)"
    break
  fi
  if [[ "$attempt" == 90 ]]; then
    log "FAIL: ${base_url}/readyz never became ready"
    dc logs --tail=80 argusd >&2 || true
    exit 1
  fi
  sleep 2
done

# Seeded from inside the compose network: the image already carries argusd,
# so a capture needs no Go toolchain on the host. --flush-immediately
# bypasses the 5s/60s batching that mirrors Claude Code's exporter defaults,
# which would otherwise make the wait below much longer.
log "seeding demo traffic (sim --mode=demo --seed=${seed})"
dc run --rm --no-deps argusd sim \
  --mode=demo \
  --seed="${seed}" \
  --target "http://argusd:8080" \
  --flush-immediately

log "waiting for >= ${min_sessions} sessions on the read API"
sessions=0
for attempt in $(seq 1 60); do
  sessions="$(
    curl -fsS "${base_url}/api/v1/sessions?limit=500" \
      | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["data"]))' 2>/dev/null || echo 0
  )"
  if [[ "$sessions" -ge "$min_sessions" ]]; then
    log "read API reports ${sessions} sessions after ${attempt} attempt(s)"
    break
  fi
  if [[ "$attempt" == 60 ]]; then
    log "FAIL: only ${sessions} sessions after 60 attempts (need >= ${min_sessions})"
    dc logs --tail=80 argusd >&2 || true
    exit 1
  fi
  sleep 2
done

# Chromium is not a repo dependency of the web package, only a Playwright
# download. Install it on demand; keep the run going if the browser is
# already there.
log "ensuring the Playwright chromium build is present"
if ! (cd "$repo_root/web" && pnpm exec playwright install chromium >/dev/null 2>&1); then
  log "plain 'playwright install chromium' failed — retrying with --with-deps (may prompt for sudo)"
  (cd "$repo_root/web" && pnpm exec playwright install chromium --with-deps)
fi

log "capturing screenshots (1440x900, dark)"
(
  cd "$repo_root/web"
  ARGUS_BASE_URL="$base_url" ARGUS_OUT_DIR="$out_dir" node scripts/ui-capture.mjs
)

log "done — screenshots in ${out_dir}:"
ls -la "$out_dir" >&2
