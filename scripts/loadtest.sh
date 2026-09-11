#!/usr/bin/env bash
# scripts/loadtest.sh (P6-03) — measure ingest under `argusd sim --mode=load`
# at a set of rates against a real compose stack, scraping /metrics + Postgres.
#
#   bash scripts/loadtest.sh                 # default rates 100 500 1000, 30s each
#   ARGUS_LOAD_RATES="100 500 1000 2000" ARGUS_LOAD_DURATION=60s bash scripts/loadtest.sh
#
# For each rate it prints a row: rate → achieved throughput, events written,
# dropped, deduped, too_old, deadlock-retries, write p50/p99, ingest-lag
# p50/p99, and the final queue depth. After the runs it reports the events
# table size, the `attrs` column's share of it (P6-07 trigger), and the
# ingest_dedup ledger row count.
#
# It brings up its OWN compose project on a non-default port and tears it down
# on exit (set ARGUS_LOAD_KEEP=1 to keep it up for manual EXPLAIN work).
#
# Env:
#   ARGUS_LOAD_PORT      host port (default 18090)
#   ARGUS_LOAD_RATES     space-separated events/s (default "100 500 1000")
#   ARGUS_LOAD_DURATION  per-rate duration (default 30s)
#   ARGUS_LOAD_KEEP      1 = leave the stack up
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
port="${ARGUS_LOAD_PORT:-18090}"
rates="${ARGUS_LOAD_RATES:-100 500 1000}"
duration="${ARGUS_LOAD_DURATION:-30s}"
base_url="http://localhost:${port}"
project_name="argus-loadtest"
export ARGUS_HTTP_PORT="$port"

dc() {
  docker compose -p "$project_name" \
    -f "$repo_root/deploy/docker-compose.yml" \
    -f "$repo_root/deploy/docker-compose.capture.yml" "$@"
}
log() { printf '[loadtest] %s\n' "$*" >&2; }

cleanup() {
  if [[ "${ARGUS_LOAD_KEEP:-0}" == "1" ]]; then
    log "ARGUS_LOAD_KEEP=1 — leaving the stack up at ${base_url} (tear down: dc down -v)"
    return
  fi
  log "tearing down"
  dc down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

if (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
  exec 3>&- 3<&- || true
  log "FAIL: host port ${port} already in use — set ARGUS_LOAD_PORT to a free port"
  exit 1
fi

log "clean start (project=${project_name}, port=${port})"
dc down -v --remove-orphans >/dev/null 2>&1 || true
log "building + starting the stack"
VERSION="loadtest" COMMIT="$(git -C "$repo_root" rev-parse --short HEAD 2>/dev/null || echo unknown)" \
  dc build argusd >/dev/null 2>&1
dc up -d >/dev/null 2>&1

log "waiting for ${base_url}/readyz"
ready=""
for _ in $(seq 1 60); do curl -fsS "${base_url}/readyz" 2>/dev/null | grep -q '"status":"ok"' && { ready=1; break; }; sleep 1; done
if [[ -z "$ready" ]]; then
  log "FAIL: ${base_url}/readyz never became ready"; dc logs --tail=60 argusd >&2 || true; exit 1
fi

# Parse a Prometheus histogram/counter snapshot into the derived numbers we
# report. The parser is written to a temp FILE (not fed on stdin) so /metrics
# text is what reaches python's stdin — piping data in while also providing the
# program via a heredoc on stdin silently gives python empty input.
parser_py="$(mktemp)"
cat >"$parser_py" <<'PY'
import sys, re
want = sys.argv[1:]
buckets = {}   # metric -> list[(le, cum_count)]
sums = {}      # metric -> sum
counts = {}    # metric -> count
counters = {}  # metric{labels} -> value  (aggregated per metric name)
for line in sys.stdin:
    if line.startswith('#') or not line.strip():
        continue
    m = re.match(r'(\w+)(\{[^}]*\})?\s+([0-9eE.+-]+)$', line.strip())
    if not m:
        continue
    name, labels, val = m.group(1), m.group(2) or '', float(m.group(3))
    if name.endswith('_bucket'):
        base = name[:-7]
        le = re.search(r'le="([^"]+)"', labels)
        if le:
            buckets.setdefault(base, []).append((float('inf') if le.group(1)=='+Inf' else float(le.group(1)), val))
    elif name.endswith('_sum'):
        sums[name[:-4]] = val
    elif name.endswith('_count'):
        counts[name[:-6]] = val
    else:
        counters[name] = counters.get(name, 0.0) + val

def quantile(base, q):
    bs = sorted(buckets.get(base, []))
    if not bs: return None
    total = bs[-1][1]
    if total == 0: return None
    target = q*total
    prev_le, prev_c = 0.0, 0.0
    for le, c in bs:
        if c >= target:
            if le == float('inf'): return prev_le
            # linear interpolation within the bucket
            span = c - prev_c
            frac = (target-prev_c)/span if span>0 else 0
            return prev_le + (le-prev_le)*frac
        prev_le, prev_c = le, c
    return bs[-1][0]

out = []
for w in want:
    if w.startswith('q:'):
        _, base, q = w.split(':')
        v = quantile(base, float(q))
        out.append(f'{v*1000:.2f}ms' if v is not None else 'n/a')
    elif w.startswith('c:'):
        out.append(f'{counters.get(w[2:],0.0):.0f}')
    elif w.startswith('n:'):
        out.append(f'{counts.get(w[2:],0.0):.0f}')
print('\t'.join(out))
PY

# The `|| true` defeats `pipefail`: a transient scrape miss must not abort the
# whole run — python then sees empty input and reports zeros.
metrics_extract() {
  { curl -fsS "${base_url}/metrics" 2>/dev/null || true; } | python3 "$parser_py" "$@"
}

psql_q() { dc exec -T postgres psql -U argus -d argus -tAc "$1" 2>/dev/null | tr -d '[:space:]'; }

printf '\n%-6s %-12s %-9s %-8s %-8s %-8s %-9s %-9s %-9s %-9s\n' \
  rate throughput written dropped deduped too_old retries write_p50 write_p99 lag_p99 >&2

for rate in $rates; do
  before_written="$(metrics_extract c:argus_ingest_events_total)"
  before_drop="$(metrics_extract c:argus_ingest_dropped_total)"
  start=$(date +%s.%N)
  dc run --rm --no-deps argusd sim \
    --mode=load --rate="$rate" --duration="$duration" \
    --target "http://argusd:8080" --flush-immediately >/dev/null 2>&1 || true
  # let the queue drain
  sleep 5
  end=$(date +%s.%N)
  read -r p50 p99 lag99 <<<"$(metrics_extract q:argus_ingest_write_duration_seconds:0.50 q:argus_ingest_write_duration_seconds:0.99 q:argus_ingest_lag_seconds:0.99)"
  after_written="$(metrics_extract c:argus_ingest_events_total)"
  dropped="$(metrics_extract c:argus_ingest_dropped_total)"
  deduped="$(metrics_extract c:argus_ingest_deduped_total)"
  tooold="$(metrics_extract c:argus_ingest_too_old_total)"
  retries="$(metrics_extract c:argus_ingest_retry_total)"
  written=$(python3 -c "print(int(${after_written:-0}-${before_written:-0}))")
  elapsed=$(python3 -c "print(f'{${end}-${start}:.1f}')")
  tput=$(python3 -c "print(f'{(${written})/max(${end}-${start},0.001):.0f}/s')")
  printf '%-6s %-12s %-9s %-8s %-8s %-8s %-9s %-9s %-9s %-9s\n' \
    "$rate" "$tput" "$written" "$dropped" "$deduped" "$tooold" "$retries" "$p50" "$p99" "$lag99" >&2
done

log "post-run storage figures"
events_rows="$(psql_q 'SELECT count(*) FROM events')"
# `events` is RANGE-partitioned: the parent's own size is ~0, real bytes live
# in the monthly partitions, so sum inhrelid sizes rather than the parent's.
part_size_sql="SELECT coalesce(sum(pg_total_relation_size(inhrelid)),0) FROM pg_inherits WHERE inhparent = 'events'::regclass"
events_total="$(psql_q "SELECT pg_size_pretty(($part_size_sql))")"
attrs_bytes="$(psql_q 'SELECT coalesce(sum(pg_column_size(attrs)),0) FROM events')"
table_bytes="$(psql_q "$part_size_sql")"
dedup_rows="$(psql_q 'SELECT count(*) FROM ingest_dedup')"
attrs_share="$(python3 -c "b=${attrs_bytes:-0}; t=${table_bytes:-1}; print(f'{100*b/t:.1f}%')" 2>/dev/null || echo 'n/a')"

{
  echo "events rows:            ${events_rows}"
  echo "events total size:      ${events_total}"
  echo "attrs share of events:  ${attrs_share}  (P6-07 triggers if > 60%)"
  echo "ingest_dedup ledger:    ${dedup_rows} rows"
} >&2

log "done"
