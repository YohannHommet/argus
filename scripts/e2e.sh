#!/usr/bin/env bash
# scripts/e2e.sh — the assertion-based browser E2E harness (the v0.1.0 ship
# gate). Sibling of scripts/ui-capture.sh (screenshots); this one runs
# Playwright *assertions* against the real embedded SPA.
#
#   bash scripts/e2e.sh
#
# What it does, in order:
#   1. brings its own compose project up from a clean state on a non-default
#      host port (8080 and 5432 are commonly taken on dev boxes / runners),
#      building argusd from the working tree — server/Dockerfile compiles the
#      Vue SPA and embeds it, so the tests hit the *embedded* app, not a dev
#      server;
#   2. waits for /readyz;
#   3. seeds deterministic demo traffic (`sim --mode=demo --seed=42`) and waits
#      until the read API reports the sessions AND the analytics rollups have
#      converged to the sessions projection's totals (so analytics.spec's
#      API-vs-UI cost/token match is stable, not racing a half-filled rollup);
#   4. runs the main Playwright suite (everything except @live);
#   5. then, strictly after, starts `argusd sim --mode=load` streaming into the
#      same stack and runs the @live suite against events actually flowing —
#      the live sim adds sessions the count/total assertions in step 4 must not
#      see, exactly why it runs last (same ordering rule as ui-capture.sh).
#
# Idempotent: every run starts with `down -v`, so a leftover stack or a
# half-seeded database from an interrupted run cannot leak in. Teardown runs on
# exit, including on failure.
#
# Env:
#   ARGUS_E2E_PORT           host port for argusd (default 18090)
#   ARGUS_E2E_SEED           demo sim seed (default 42 — deterministic)
#   ARGUS_E2E_SESSIONS       demo session count (default 80 — 1/5 land in the
#                            metrics-only project and never reach the sessions
#                            list, so 80 requested -> ~64 visible, above the
#                            50-row page size so the list actually paginates)
#   ARGUS_E2E_KEEP           set to 1 to leave the stack running for debugging
#   ARGUS_E2E_SKIP_LIVE      set to 1 to skip the @live phase (the load-sim pass)
#   ARGUS_E2E_LIVE_RATE      load-sim events/s during the live phase (default 20)
#   ARGUS_E2E_LIVE_DURATION  load-sim duration (default 600s — must outlast the
#                            whole @live phase incl. retries; teardown kills it early)
#   ARGUS_E2E_LIVE_SEED      load-sim seed (default 7)
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

port="${ARGUS_E2E_PORT:-18090}"
seed="${ARGUS_E2E_SEED:-42}"
# 1 in 5 demo sessions belong to a metrics-only project that emits no session
# lifecycle events, so it never appears in the sessions list; 80 requested ->
# ~64 visible, above the 50-row page size so the list paginates (page 1 = 50,
# "Load more" reveals the rest) — which sessions.spec's pagination case needs.
sessions_count="${ARGUS_E2E_SESSIONS:-80}"
live_rate="${ARGUS_E2E_LIVE_RATE:-20}"
live_duration="${ARGUS_E2E_LIVE_DURATION:-600s}"
live_seed="${ARGUS_E2E_LIVE_SEED:-7}"
base_url="http://localhost:${port}"
# Overridable so an E2E run and a concurrent capture run (or a second E2E run)
# can use distinct compose projects without colliding on container names.
project_name="${COMPOSE_PROJECT_NAME:-argus-e2e}"
min_sessions=20

export ARGUS_HTTP_PORT="$port"

dc() {
  docker compose -p "$project_name" \
    -f "$repo_root/deploy/docker-compose.yml" \
    -f "$repo_root/deploy/docker-compose.e2e.yml" "$@"
}

log() { printf '[e2e] %s\n' "$*" >&2; }

cleanup() {
  if [[ "${ARGUS_E2E_KEEP:-0}" == "1" ]]; then
    log "ARGUS_E2E_KEEP=1 — leaving the stack up at ${base_url}"
    log "tear it down with: docker compose -p ${project_name} -f deploy/docker-compose.yml -f deploy/docker-compose.e2e.yml down -v"
    return
  fi
  log "tearing down (docker compose -p ${project_name} down -v)"
  dc down -v --remove-orphans || true
}
trap cleanup EXIT

# The host port is shared even across compose projects, so fail loudly here
# rather than let `up -d` collide with a live stack or a second E2E run.
if (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
  exec 3>&- 3<&- || true
  log "FAIL: host port ${port} is already in use. Set ARGUS_E2E_PORT to a free port, e.g. ARGUS_E2E_PORT=18091 bash scripts/e2e.sh"
  exit 1
fi

# Playwright + its deps must be installed in web/ (CI installs before calling
# this; locally, install on demand so a fresh checkout still runs).
if [[ ! -d "$repo_root/web/node_modules/@playwright/test" ]]; then
  log "installing web dependencies (pnpm install --frozen-lockfile)"
  (cd "$repo_root/web" && pnpm install --frozen-lockfile)
fi

log "starting from a clean state (project=${project_name})"
dc down -v --remove-orphans >/dev/null 2>&1 || true

log "building argus:e2e (compiles + embeds the SPA; first run is slow)"
VERSION="$(git -C "$repo_root" describe --tags --always --dirty 2>/dev/null || echo e2e)" \
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

# Seeded from inside the compose network: the image already carries argusd, so
# seeding needs no Go toolchain on the host. --flush-immediately bypasses the
# 5s/60s ingest batching so the read API reports the sessions promptly.
log "seeding demo traffic (sim --mode=demo --seed=${seed} --sessions=${sessions_count})"
dc run --rm --no-deps argusd sim \
  --mode=demo \
  --seed="${seed}" \
  --sessions="${sessions_count}" \
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

# analytics.spec asserts the rendered Cost/Tokens KPIs against
# /api/v1/analytics/summary to the cent. That endpoint reads the periodic
# `rollup_hourly`, which lags the synchronously-written sessions projection by
# ~45s after a seed. Both the test and the UI read the same rollup, but it can
# advance *between* their two reads and flake the match — so gate on the rollup
# having converged to the sessions projection's ground-truth totals first (the
# exact convergence wait scripts/ui-capture.sh uses, and for the same reason).
# Costs are compared in whole cents to avoid float-repr flapping.
log "waiting for the analytics rollups to reach the sessions projection's totals"
truth=""
for attempt in $(seq 1 90); do
  truth="$(
    curl -fsS "${base_url}/api/v1/sessions?limit=500" 2>/dev/null \
      | python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; print(round(sum(s["cost"]["usd"] for s in d)*100), sum(s["tool_call_count"] for s in d))' 2>/dev/null || echo "pending"
  )"
  rolled="$(
    curl -fsS "${base_url}/api/v1/analytics/summary?from=-30d" 2>/dev/null \
      | python3 -c 'import json,sys; d=json.load(sys.stdin); print(round(d["cost"]["usd"]*100), d["tool_calls"])' 2>/dev/null || echo "pending"
  )"
  if [[ "$truth" != "pending" && "$rolled" == "$truth" ]]; then
    log "rollups match the sessions projection [cents tool_calls = ${rolled}] after ${attempt} attempt(s)"
    break
  fi
  if [[ "$attempt" == 90 ]]; then
    log "FAIL: rollups never reached the projection (rollup=[${rolled}] projection=[${truth}]) — analytics API-vs-UI would flap"
    dc logs --tail=80 argusd >&2 || true
    exit 1
  fi
  sleep 5
done

# Chromium is a Playwright download, not a package dep. Install on demand; keep
# going if it's already present.
log "ensuring the Playwright chromium build is present"
if ! (cd "$repo_root/web" && pnpm exec playwright install chromium >/dev/null 2>&1); then
  log "plain 'playwright install chromium' failed — retrying with --with-deps (may prompt for sudo)"
  (cd "$repo_root/web" && pnpm exec playwright install chromium --with-deps)
fi

# --- main suite (everything except @live) ----------------------------------
log "running the main Playwright suite against ${base_url}"
(
  cd "$repo_root/web"
  ARGUS_E2E_BASE_URL="$base_url" pnpm exec playwright test --grep-invert @live
)

# --- @live suite (needs a load sim streaming while it runs) -----------------
# Runs strictly after the main suite: --mode=load adds sessions with ~now
# timestamps, and running it first would change what every count/total
# assertion above sees. `run -d` detaches inside Docker (a single foreground
# script with nothing to `wait` on); the container is torn down with the
# compose project by cleanup().
if [[ "${ARGUS_E2E_SKIP_LIVE:-0}" == "1" ]]; then
  log "ARGUS_E2E_SKIP_LIVE=1 — skipping the @live phase"
else
  log "starting the load sim (rate=${live_rate}/s, duration=${live_duration}, seed=${live_seed}) for the @live phase"
  dc run --rm --no-deps -d argusd sim \
    --mode=load \
    --rate="${live_rate}" \
    --duration="${live_duration}" \
    --seed="${live_seed}" \
    --target "http://argusd:8080" \
    --flush-immediately >/dev/null

  log "running the @live Playwright suite while events stream"
  (
    cd "$repo_root/web"
    ARGUS_E2E_BASE_URL="$base_url" ARGUS_E2E_LIVE=1 pnpm exec playwright test --grep @live
  )
fi

log "done — all E2E phases passed"
