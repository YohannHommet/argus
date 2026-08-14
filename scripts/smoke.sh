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
#                      container's own :8080 listener. It also doubles as the
#                      escape hatch for the M10 port collision below: a `make
#                      compose-up` stack and this smoke stack are different
#                      compose projects now, so they no longer share a volume,
#                      but they'd still fight over the same host port unless
#                      one of them sets this.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose_file="$repo_root/deploy/docker-compose.yml"
image="ghcr.io/yohannhommet/argus:latest"
port="${ARGUS_HTTP_PORT:-8080}"
export ARGUS_HTTP_PORT="$port"
base_url="http://localhost:${port}"

# M10: run under our own compose project, distinct from `make compose-up`'s
# (which takes the default project name derived from the `deploy` dir). Same
# -f file, different -p, so `down -v` below only ever touches this project's
# own containers/volumes — never a developer's live stack sharing that file.
project_name="argus-smoke"
dc() { docker compose -p "$project_name" -f "$compose_file" "$@"; }

log() { printf '[smoke] %s\n' "$*" >&2; }

cleanup() {
  log "tearing down (docker compose -p $project_name down -v)"
  dc down -v --remove-orphans || true
}
trap cleanup EXIT

# The port is still shared host-wide even across projects: fail loudly and
# early rather than let `up -d` collide with a live stack (or another smoke
# run) already bound to it.
if (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
  exec 3>&- 3<&- || true
  log "FAIL: host port ${port} is already in use — likely a live 'make compose-up' stack or another smoke run. Set ARGUS_HTTP_PORT to a free port, e.g.: ARGUS_HTTP_PORT=18080 bash scripts/smoke.sh"
  exit 1
fi

log "starting from a clean state (project=$project_name)"
dc down -v --remove-orphans

version="$(git -C "$repo_root" describe --tags --always --dirty 2>/dev/null || echo dev)"
commit="$(git -C "$repo_root" rev-parse --short HEAD 2>/dev/null || echo unknown)"

log "building $image (version=$version commit=$commit)"
docker build \
  -f "$repo_root/server/Dockerfile" \
  --build-arg "VERSION=$version" \
  --build-arg "COMMIT=$commit" \
  -t "$image" \
  "$repo_root"

log "docker compose up -d (ARGUS_HTTP_PORT=$port, project=$project_name)"
dc up -d

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
  dc logs --no-color || true
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
dc exec -T argusd /argusd sim \
  --mode=demo \
  --sessions="$sim_sessions" \
  --flush-immediately \
  --tool-use-id-in-hooks=true \
  --target=http://localhost:8080

psql_query() {
  dc exec -T postgres psql -U argus -d argus -tAc "$1"
}

# Poll to QUIESCENCE, not to first sight. This loop used to break the moment
# `events > 0`, which meant it read its counts in the middle of ingest and
# printed whatever had landed so far — a 5-session run reporting "sessions: 4"
# and a partial event count, which looks like a lost session and is really
# just an early read. Worse, `> 0` is too weak to fail: a regression that
# persisted one event out of hundreds would still have passed this gate, even
# though SPEC §8.3 describes this job as asserting row counts.
#
# Waiting for two consecutive equal reads is what makes the numbers below
# mean something, and lets the session count be asserted exactly.
log "polling for the sim's rows to settle (ingest is async, ARGUS_INGEST_FLUSH)"
deadline=$((SECONDS + 60))
event_count=0
previous=-1
stable=0
until [ "$SECONDS" -ge "$deadline" ]; do
  event_count="$(psql_query 'SELECT count(*) FROM events' | tr -d '[:space:]')"
  [ -n "$event_count" ] || event_count=0
  if [ "$event_count" -gt 0 ] 2>/dev/null && [ "$event_count" -eq "$previous" ] 2>/dev/null; then
    stable=1
    break
  fi
  previous="$event_count"
  sleep 2
done

if [ "$stable" -ne 1 ]; then
  log "FAIL: event count never settled within 60s (last two reads: ${previous:-<empty>}, ${event_count:-<empty>})"
  dc logs --no-color argusd || true
  exit 1
fi
log "events: $event_count (settled)"

# Every session referenced by an event must have a sessions row, and there
# must be no rows beyond those: stub-on-reference (SPEC §1.7) guarantees the
# parent row exists for any event that arrives, so a mismatch means either a
# lost session projection or an orphaned row. This is the assertion that can
# actually catch something, where "> 0" would notice none of it.
#
# Deliberately NOT asserted as "== --sessions": that would be asserting a
# property of the generator, not of the server. `argusd sim --sessions=5`
# currently emits zero log and zero hook events for its last session (only a
# metric sample — verified with --out=, where session-0004 holds one
# metrics-*.pb and nothing else, and session-0003 gets 8 log events against
# 121-203 for the earlier ordinals), so a metrics-only session correctly
# produces no sessions row at all. That sim defect is recorded in
# docs/review/phase-3-deviations.md; it belongs to the generator, and the
# invariant below holds either way.
session_count="$(psql_query 'SELECT count(*) FROM sessions' | tr -d '[:space:]')"
event_sessions="$(psql_query 'SELECT count(DISTINCT session_id) FROM events' | tr -d '[:space:]')"
if [ "${session_count:-0}" -ne "${event_sessions:-1}" ] 2>/dev/null; then
  log "FAIL: every event's session must have a sessions row (SPEC §1.7 stub-on-reference): sessions=${session_count:-<empty>} vs distinct event session_ids=${event_sessions:-<empty>}"
  exit 1
fi
if [ "${session_count:-0}" -le 0 ] 2>/dev/null; then
  log "FAIL: expected at least one session after the demo sim, got '${session_count:-<empty>}'"
  exit 1
fi
log "sessions: $session_count (= distinct event session_ids)"

# A clean run (no --chaos-orphans) must leave no stub sessions: every session
# saw its SessionStart, so started_at is populated everywhere (SPEC §1.7,
# Phase-2 exit criterion 4).
stub_count="$(psql_query 'SELECT count(*) FROM sessions WHERE started_at IS NULL' | tr -d '[:space:]')"
if [ "${stub_count:-1}" -ne 0 ] 2>/dev/null; then
  log "FAIL: expected 0 stub sessions (started_at IS NULL) after a clean run, got '${stub_count:-<empty>}'"
  exit 1
fi
log "stub sessions: $stub_count"

log "PASS"
