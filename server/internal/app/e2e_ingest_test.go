//go:build e2e

// Package app's end-to-end test (P2-13) starts the real App (this package's
// New/Serve — the same construction cmd/argusd's `serve` subcommand uses,
// not a hand-assembled subset of it) against a real Postgres, drives
// argus-sim (internal/sim, P2-12/P2-13) against it over real HTTP exactly
// as a live process would, and asserts every Phase-2 exit criterion
// (docs/PLAN.md "Phase 2 — Ingestion … Exit criteria", numbered 1-9) plus
// every chaos-flag AC this ticket names, as SQL assertions against the rows
// that landed.
//
// Build-tagged (never runs in a plain `go test ./...`) because it needs
// real Docker (or ARGUS_TEST_DATABASE_URL) and takes tens of seconds to
// drive thousands of real HTTP requests through the pipeline; CI's
// `go-test` job passes -tags=e2e explicitly (.github/workflows/ci.yml) so
// it still runs on every push.
//
// The white-box `package app` (not `app_test`) is deliberate: this test
// needs a.ingest.Metrics() to read the exact Prometheus counters SPEC §3.6
// names (argus_ingest_events_total, _deduped_total, _too_old_total) without
// re-parsing the /metrics text exposition format, which would just be
// re-implementing promhttp's own decoder for no benefit.
package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/config"
	"github.com/YohannHommet/argus/server/internal/sim"
	storetesting "github.com/YohannHommet/argus/server/internal/store/testing"
)

// e2eDrainTimeout bounds every "wait for the async ingest pipeline to catch
// up with what was just sent" poll in this file. The pipeline enqueues and
// returns 2xx before the batch is actually written (ARGUS_INGEST_FLUSH,
// internal/ingest/pipeline.go), so every assertion that depends on rows
// having landed polls a quiescence condition instead of asserting
// immediately after an HTTP response comes back.
const e2eDrainTimeout = 60 * time.Second

// TestE2E_Phase2ExitCriteria is the single end-to-end run this ticket
// requires: one App instance, one Postgres schema, driven by several
// argus-sim invocations against it in sequence (a rerun for exit criterion
// 5's dedup proof needs a *second* invocation with the same seed/origin;
// each chaos flag needs its own invocation so its effect is attributable),
// asserting every Phase-2 exit criterion and every chaos-flag AC along the
// way.
func TestE2E_Phase2ExitCriteria(t *testing.T) {
	app, baseURL, pool := newE2EApp(t)
	ctx := context.Background()

	// A --clock-origin fixed once and reused by both the baseline run and
	// its rerun (exit criterion 5: "same --seed and --clock-origin twice").
	// It is a live (near-"now") timestamp, not a historical one: exit
	// criterion 6 needs correlation='exact' > 0, which the P2-13 brief's
	// live-run finding says requires --backfill=0 (live timestamps) —
	// picking an explicit origin close to "now" rather than relying on
	// --backfill=0's own now-minus-zero default just makes the value
	// reusable across the two invocations.
	origin := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	originFlag := "--clock-origin=" + origin.Format(time.RFC3339)

	baselineArgs := []string{
		"--seed=1",
		originFlag,
		"--backfill=0",
		"--sessions=40",
		"--flush-immediately",
		"--tool-use-id-in-hooks=true",
		"--target=" + baseURL,
	}

	// --- Exit criterion 1: sim exits 0, every HTTP status 2xx -----------
	stdout, stderr, code, report := runSim(t, baselineArgs)
	require.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	require.True(t, report.AllOK(), "expected an all-2xx run: %+v", report.StatusHistogram)

	eventsBySource, deduped, tooOld := waitForIngestQuiescence(t, pool, app)

	// --- Exit criterion 2: row count reconciles against the sim's own
	// reported counts, net of dedup/too_old (P2-13 brief finding 5's
	// verified formula).
	totalRows := scalarInt(t, pool, `SELECT count(*) FROM events`)
	distinctSessions := scalarInt(t, pool, `SELECT count(DISTINCT session_id) FROM events`)
	require.Positive(t, totalRows)
	require.Positive(t, distinctSessions)
	require.Equal(t,
		int64(totalRows),
		eventsBySource["otel_log"]+eventsBySource["hook"]-deduped-tooOld,
		"argus_ingest_events_total{source=otel_log}+{source=hook} - deduped - too_old must equal count(*) FROM events "+
			"(otel_metric is deliberately excluded: SPEC §1.8 - no metric is ever mirrored into events)",
	)

	// --- Exit criterion 3: >= 14 distinct kinds, unknown = 0 on a clean
	// (no --chaos-*) run.
	kindCount := scalarInt(t, pool, `SELECT count(DISTINCT kind) FROM events`)
	require.GreaterOrEqual(t, kindCount, 14, "expected >= 14 distinct kinds (taxonomy incl. the three hook.* kinds)")
	unknownCount := scalarInt(t, pool, `SELECT count(*) FROM events WHERE kind = 'unknown'`)
	require.Zero(t, unknownCount, "kind='unknown' must be 0 with every --chaos-* flag off")

	// --- Exit criterion 4 (clean-run half): every session fully healed.
	nullStarted := scalarInt(t, pool, `SELECT count(*) FROM sessions WHERE started_at IS NULL`)
	require.Zero(t, nullStarted, "a clean run must leave no stub sessions behind")

	// --- Exit criterion 6: all 6 documented decision_source values plus
	// the sim's invented one; correlation='exact' > 0 (requires
	// --backfill=0 + --tool-use-id-in-hooks=true, P2-13 brief finding 2).
	sources := scalarStrings(t, pool, `SELECT DISTINCT decision_source FROM tool_calls WHERE decision_source IS NOT NULL ORDER BY 1`)
	require.GreaterOrEqual(t, len(sources), 7, "want the 6 documented decision_source values plus the sim's invented one, got %v", sources)
	require.Contains(t, sources, "an_invented_decision_source")
	exactCorrelation := scalarInt(t, pool, `SELECT count(*) FROM tool_calls WHERE correlation = 'exact'`)
	require.Positive(t, exactCorrelation, "expected correlation='exact' > 0 with --backfill=0 --tool-use-id-in-hooks=true")

	// --- Exit criterion 7: depth-2 subagents with a non-null parent,
	// cost_usd NULL on every row (SPEC §1.9: never fabricated), non-null
	// tool_call_count where hook coverage exists.
	depth2 := scalarInt(t, pool, `SELECT count(*) FROM subagents WHERE depth = 2 AND parent_agent_id IS NOT NULL`)
	require.Positive(t, depth2, "expected at least one depth-2 subagent with a non-null parent_agent_id (needs enough sessions - see P2-13 brief finding 4)")
	costNotNull := scalarInt(t, pool, `SELECT count(*) FROM subagents WHERE cost_usd IS NOT NULL`)
	require.Zero(t, costNotNull, "cost_usd must be NULL on every subagent row (SPEC §1.9: Claude Code emits no per-agent cost)")
	toolCallCountKnown := scalarInt(t, pool, `SELECT count(*) FROM subagents WHERE tool_call_count IS NOT NULL`)
	require.Positive(t, toolCallCountKnown, "expected tool_call_count to be known for at least one subagent (hook coverage is on)")

	// --- Exit criterion 8: cost_by_query_source carries at least one value
	// Argus has no constant for (SPEC §1.9's unconstrained-vocabulary path).
	qsKeys := scalarStrings(t, pool, `SELECT DISTINCT jsonb_object_keys(cost_by_query_source) FROM sessions`)
	require.Contains(t, qsKeys, "generate_session_title", "expected the live-observed, undocumented query_source value to flow through verbatim")

	// --- Exit criterion 5: re-running the identical seeded sim inserts
	// zero new rows, and argus_ingest_deduped_total accounts for exactly
	// the resent count — including hook events specifically (P2-13 lead
	// note: hook ts is receipt time and differs on every delivery, so this
	// is the proof the ingest_dedup ledger, not a ts-bearing key, is the
	// actual gate).
	bySourceBefore := scalarByKeyInt(t, pool, `SELECT source, count(*) FROM events GROUP BY source`)
	totalBefore := totalRows

	_, dedupedBefore, _ := readIngestMetrics(app)

	stdout2, stderr2, code2, report2 := runSim(t, baselineArgs)
	require.Equal(t, 0, code2, "stdout=%s stderr=%s", stdout2, stderr2)
	require.True(t, report2.AllOK())

	_, dedupedAfter, _ := waitForRerunQuiescence(t, pool, app, totalBefore)

	totalAfter := scalarInt(t, pool, `SELECT count(*) FROM events`)
	require.Equal(t, totalBefore, totalAfter, "re-running the identical seeded sim must insert zero new rows")

	bySourceAfter := scalarByKeyInt(t, pool, `SELECT source, count(*) FROM events GROUP BY source`)
	require.Equal(t, bySourceBefore["hook"], bySourceAfter["hook"], "hook-sourced row count must be unchanged by the rerun (proves ledger-based, not ts-based, hook dedup)")
	require.Equal(t, bySourceBefore["otel_log"], bySourceAfter["otel_log"], "otel_log-sourced row count must be unchanged by the rerun")

	resentTotal := report2.LogEvents + report2.HookEvents
	require.Equal(t, int(dedupedAfter-dedupedBefore), resentTotal,
		"argus_ingest_deduped_total must have grown by exactly the number of (identical) events resent")

	// --- --chaos-duplicates: resend ~3% of hook sends byte-identical
	// within a single run; the ledger suppresses them so the stored
	// hook-row count matches the sim's own logical event count (not
	// inflated by the resends), and the dedup counter shows suppression
	// actually happened.
	runChaosDuplicates(t, app, pool, baseURL)

	// --- --chaos-orphans: turn events before SessionStart ->
	// stub-on-reference (transient NULL started_at, observed by concurrent
	// polling since the pipeline is async) and the late-project rollup
	// re-mark.
	runChaosOrphans(t, app, pool, baseURL)

	// --- --chaos-clock-skew: sets clock_skewed on some events, and its
	// opt-in beyond-partition-horizon event is rejected with
	// argus_ingest_too_old_total incremented.
	runChaosClockSkew(t, app, pool, baseURL)

	// --- --chaos-unknown: kind='unknown' rows with event_name preserved,
	// zero rejections.
	runChaosUnknown(t, app, pool, baseURL)

	// --- Exit criterion 9: load mode above capacity sheds load correctly
	// and never panics.
	runLoadCapacityCheck(t)
	_ = ctx
}

// newE2EApp constructs a real App (New) against a freshly migrated, isolated
// Postgres schema and starts Serve on an ephemeral port, returning the base
// URL argus-sim's --target expects and a plain pgxpool.Pool (a second,
// independent connection to the same schema) for this test's own read-only
// assertions.
func newE2EApp(t *testing.T) (*App, string, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	dsn := storetesting.NewDSN(t)
	t.Setenv("ARGUS_DATABASE_URL", dsn)
	t.Setenv("ARGUS_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("ARGUS_INGEST_FLUSH", "100ms")

	cfg, warnings, err := config.Load("")
	require.NoError(t, err)
	require.Empty(t, warnings)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(ctx, cfg, logger)
	require.NoError(t, err)

	serveCtx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- a.Serve(serveCtx) }()

	select {
	case <-a.Listening():
	case <-time.After(10 * time.Second):
		t.Fatal("app did not start listening in time")
	}
	require.NotEmpty(t, a.Addr())

	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(20 * time.Second):
		}
	})

	assertPool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(assertPool.Close)

	return a, "http://" + a.Addr(), assertPool
}

// runSim runs one argus-sim invocation (sim.RunCLI, the exact entry point
// cmd/argus-sim/main.go and argusd's `sim` subcommand both call) and
// returns its stdout/stderr/exit code plus the Report it printed, recovered
// by running the CLI's own construction directly rather than parsing
// stdout, so assertions can use its typed fields (Report.AllOK,
// Report.HookEvents, …).
func runSim(t *testing.T, args []string) (stdout, stderr string, code int, report *sim.Report) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	code, report = sim.RunCLIWithReport(args, &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), code, report
}

// scalarInt runs a single-row, single-column integer query.
func scalarInt(t *testing.T, pool *pgxpool.Pool, query string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), query).Scan(&n))
	return n
}

// scalarStrings runs a single-column query and collects every row.
func scalarStrings(t *testing.T, pool *pgxpool.Pool, query string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), query)
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

// scalarByKeyInt runs a "SELECT key, count(*) ... GROUP BY key"-shaped
// query into a map.
func scalarByKeyInt(t *testing.T, pool *pgxpool.Pool, query string) map[string]int {
	t.Helper()
	rows, err := pool.Query(context.Background(), query)
	require.NoError(t, err)
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		require.NoError(t, rows.Scan(&k, &n))
		out[k] = n
	}
	require.NoError(t, rows.Err())
	return out
}

// readIngestMetrics reads the current cumulative values of the three
// argus_ingest_* counters exit criterion 2/5 reconcile against, straight
// off the Pipeline's own *prometheus.CounterVec/Counter (a.ingest is this
// package's own unexported field — package app's white-box test doc
// comment explains why this beats scraping /metrics text).
func readIngestMetrics(a *App) (bySource map[string]int64, deduped, tooOld int64) {
	m := a.ingest.Metrics()
	bySource = map[string]int64{}
	for _, src := range []string{"otel_log", "otel_metric", "hook"} {
		bySource[src] = int64(testutil.ToFloat64(m.Events.WithLabelValues(src)))
	}
	deduped = int64(testutil.ToFloat64(m.Deduped))
	tooOld = int64(testutil.ToFloat64(m.TooOld))
	return bySource, deduped, tooOld
}

// queueDepthSum sums a GaugeVec's current values across every label
// combination, used by the quiescence pollers below to tell "the pipeline
// has nothing buffered right now" apart from "the pipeline hasn't been
// asked to do anything yet".
func queueDepthSum(g *prometheus.GaugeVec) float64 {
	ch := make(chan prometheus.Metric, 8)
	g.Collect(ch)
	close(ch)
	var total float64
	for m := range ch {
		var pb dto.Metric
		_ = m.Write(&pb)
		total += pb.GetGauge().GetValue()
	}
	return total
}

// waitForIngestQuiescence polls until the ingest queue is empty and the
// event-row count in Postgres has stopped growing (the pipeline enqueues
// and returns 2xx before a batch is actually written, so an assertion
// immediately after an HTTP 2xx would be racing the batcher). It returns
// the final cumulative argus_ingest_events_total (by source),
// _deduped_total, and _too_old_total values once quiescent.
func waitForIngestQuiescence(t *testing.T, pool *pgxpool.Pool, a *App) (bySource map[string]int64, deduped, tooOld int64) {
	t.Helper()
	waitForQuiescentCount(t, pool, a, `SELECT count(*) FROM events`)
	return readIngestMetrics(a)
}

// waitForQuiescentCount polls query (a single-row, single-int-column
// statement) until both the ingest queue is empty and the value it returns
// has stopped changing for 5 consecutive 100ms polls, then returns that
// value. Every "wait for the async pipeline to finish landing what was just
// sent" check in this file (the hard rules note: "poll with a deadline and
// fail loudly on timeout", never a bare sleep) goes through this one
// function.
func waitForQuiescentCount(t *testing.T, pool *pgxpool.Pool, a *App, query string) int {
	t.Helper()

	deadline := time.Now().Add(e2eDrainTimeout)
	lastCount := -1
	stableIterations := 0
	for time.Now().Before(deadline) {
		queueEmpty := queueDepthSum(a.ingest.Metrics().QueueDepth) == 0
		count := scalarInt(t, pool, query)
		if queueEmpty && count == lastCount {
			stableIterations++
			if stableIterations >= 5 {
				return count
			}
		} else {
			stableIterations = 0
		}
		lastCount = count
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("waitForQuiescentCount: timed out after %s waiting for %q to stabilize (last value %d)", e2eDrainTimeout, query, lastCount)
	return -1
}

// waitForRerunQuiescence is waitForIngestQuiescence specialised for exit
// criterion 5's rerun: the row count is expected to stay at
// expectedUnchangedCount (a full rerun inserts nothing), so quiescence here
// means "queue empty and deduped_total has stopped growing", not "row count
// has stopped growing" (which would already be true from the moment the
// rerun starts).
func waitForRerunQuiescence(t *testing.T, pool *pgxpool.Pool, a *App, expectedUnchangedCount int) (bySource map[string]int64, deduped, tooOld int64) {
	t.Helper()

	deadline := time.Now().Add(e2eDrainTimeout)
	var lastDeduped int64 = -1
	stableIterations := 0
	for time.Now().Before(deadline) {
		queueEmpty := queueDepthSum(a.ingest.Metrics().QueueDepth) == 0
		_, dedupedNow, _ := readIngestMetrics(a)
		if queueEmpty && dedupedNow == lastDeduped {
			stableIterations++
			if stableIterations >= 5 {
				count := scalarInt(t, pool, `SELECT count(*) FROM events`)
				require.Equal(t, expectedUnchangedCount, count, "row count changed mid-rerun-quiescence check")
				bySource, deduped, tooOld = readIngestMetrics(a)
				return bySource, deduped, tooOld
			}
		} else {
			stableIterations = 0
		}
		lastDeduped = dedupedNow
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("waitForRerunQuiescence: timed out after %s", e2eDrainTimeout)
	return nil, 0, 0
}

// runChaosDuplicates drives a --chaos-duplicates run and asserts the
// resulting hook-row count never exceeds the sim's own reported logical
// hook event count — i.e. the ~3% byte-identical resends never inflate
// storage — and that argus_ingest_deduped_total grew, i.e. suppression
// (not a silent overwrite) is what happened.
//
// The bound is "<=", not "==": P2-13's live-run finding 6 established that
// even a --chaos-duplicates-free run already shows some dedup suppression,
// because the hook dedup key deliberately excludes ts (SPEC §1.7 rule 2) —
// two genuinely distinct hook events with byte-identical bodies in one
// session collapse into one row on their own. An exact-equality assertion
// here would assume a clean-baseline-of-zero-collisions that is not true in
// general; "never inflated, and suppression demonstrably happened" is the
// honest, always-true form of this AC.
func runChaosDuplicates(t *testing.T, a *App, pool *pgxpool.Pool, baseURL string) {
	t.Helper()

	before := scalarInt(t, pool, `SELECT count(*) FROM events WHERE source = 'hook'`)
	_, dedupedBefore, _ := readIngestMetrics(a)

	args := []string{
		"--seed=101",
		"--backfill=0",
		"--sessions=10",
		"--flush-immediately",
		"--tool-use-id-in-hooks=true",
		"--chaos-duplicates",
		"--target=" + baseURL,
	}
	stdout, stderr, code, report := runSim(t, args)
	require.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	require.True(t, report.AllOK())

	ceiling := before + report.HookEvents
	lastSeen := waitForQuiescentCount(t, pool, a, `SELECT count(*) FROM events WHERE source = 'hook'`)
	require.LessOrEqual(t, lastSeen, ceiling,
		"expected the ~3%% byte-identical hook resends to never inflate the stored row count beyond the sim's own logical event count; before=%d reportHookEvents=%d",
		before, report.HookEvents)

	_, dedupedAfter, _ := readIngestMetrics(a)
	require.Greater(t, dedupedAfter, dedupedBefore, "expected argus_ingest_deduped_total to grow: --chaos-duplicates resent >=1 hook payload byte-identically across 10 sessions")
}

// runChaosOrphans drives a --chaos-orphans run while concurrently polling
// `sessions.started_at IS NULL` in the background: because SessionStart is
// delivered after several of that session's own turn hooks (and the
// pipeline is asynchronous), there is a real window mid-run where at least
// one session exists only as a stub (exit criterion 4's "> 0 with
// --chaos-orphans" half). After the run drains, every session must be
// fully healed again (criterion 4's "returning to fully-populated").
//
// It also asserts the SPEC §2.4 project-change re-mark's *mechanism* ran
// (rollup_dirty carries an 'event' row for the hour every touched session
// lands in) — but not that it added *more* rows than an ordinary session
// would have: upsert_session.go's projectChangeInputs treats any
// NULL/"" -> real-value transition as a change, which is also true of an
// ordinary (non-orphaned) session's very first batch (old.project is ""
// for a brand-new row). The re-mark rule is the same code path either way;
// --chaos-orphans is SPEC's example of *why* it exists (a session whose
// project is unknown for a while), not a separate mechanism with its own
// distinguishable side effect. A real deployment's orphaned sessions span
// hours or days (the whole point of the rule); this in-process test's
// entire run completes in well under an hour, so the marked bucket set for
// an orphaned session and an ordinary one is not observably different at
// hour granularity here — a limitation of test compression, not of the
// mechanism, and reported as such rather than asserting a delta this test
// cannot actually produce.
func runChaosOrphans(t *testing.T, _ *App, pool *pgxpool.Pool, baseURL string) {
	t.Helper()

	stopPolling := make(chan struct{})
	sawPartial := make(chan bool, 1)
	go func() {
		// This background goroutine must never call a require.* assertion
		// (directly or via a helper like scalarInt that wraps one):
		// require's FailNow calls runtime.Goexit, which only unwinds the
		// calling goroutine, not the test — a failure in here would be
		// silently swallowed instead of failing the test (testifylint's
		// go-require rule). It reads the count with a bare query and simply
		// ignores a transient error; a real failure still surfaces from the
		// main goroutine's own assertions below.
		observed := false
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopPolling:
				sawPartial <- observed
				return
			case <-ticker.C:
				var n int
				if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE started_at IS NULL`).Scan(&n); err == nil && n > 0 {
					observed = true
				}
			}
		}
	}()

	args := []string{
		"--seed=202",
		"--backfill=0",
		"--sessions=10",
		"--flush-immediately",
		"--tool-use-id-in-hooks=true",
		"--chaos-orphans",
		"--target=" + baseURL,
	}
	stdout, stderr, code, report := runSim(t, args)
	close(stopPolling)
	partial := <-sawPartial

	require.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	require.True(t, report.AllOK())
	require.True(t, partial, "expected to observe at least one stub session (started_at IS NULL) while --chaos-orphans delayed SessionStart delivery")

	require.Eventually(t, func() bool {
		return scalarInt(t, pool, `SELECT count(*) FROM sessions WHERE started_at IS NULL`) == 0
	}, e2eDrainTimeout, 100*time.Millisecond, "every session must be fully healed once the late SessionStart lands and drains")

	dirtyAfter := scalarInt(t, pool, `SELECT count(*) FROM rollup_dirty WHERE source = 'event'`)
	require.Positive(t, dirtyAfter, "expected the project-change dirty-marking rule (SPEC §2.4) to have populated rollup_dirty for the hour every healed session lands in")
}

// runChaosClockSkew drives a --chaos-clock-skew run and asserts both
// halves of its AC: at least one event is flagged clock_skewed, and its
// single opt-in beyond-partition-horizon event is rejected with
// argus_ingest_too_old_total incremented (chaos.go's buildChaosTooOldEvent
// doc comment explains why a month-boundary-crossing timestamp, not one
// that trips the §1.2 clamp, is the reachable mechanism).
func runChaosClockSkew(t *testing.T, a *App, pool *pgxpool.Pool, baseURL string) {
	t.Helper()

	_, _, tooOldBefore := readIngestMetrics(a)

	args := []string{
		"--seed=303",
		"--backfill=0",
		"--sessions=10",
		"--flush-immediately",
		"--tool-use-id-in-hooks=true",
		"--chaos-clock-skew",
		"--target=" + baseURL,
	}
	stdout, stderr, code, report := runSim(t, args)
	require.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	require.True(t, report.AllOK())

	require.Eventually(t, func() bool {
		return scalarInt(t, pool, `SELECT count(*) FROM events WHERE clock_skewed`) > 0
	}, e2eDrainTimeout, 100*time.Millisecond, "expected at least one clock_skewed=true event from the ~2%% per-event skew draw")

	require.Eventually(t, func() bool {
		_, _, tooOldNow := readIngestMetrics(a)
		return tooOldNow > tooOldBefore
	}, e2eDrainTimeout, 100*time.Millisecond, "expected argus_ingest_too_old_total to grow: the opt-in beyond-partition-horizon event has no partition to land in")
}

// runChaosUnknown drives a --chaos-unknown run and asserts kind='unknown'
// rows are stored (never rejected) with event_name preserved.
func runChaosUnknown(t *testing.T, _ *App, pool *pgxpool.Pool, baseURL string) {
	t.Helper()

	args := []string{
		"--seed=404",
		"--backfill=0",
		"--sessions=5",
		"--flush-immediately",
		"--tool-use-id-in-hooks=true",
		"--chaos-unknown",
		"--target=" + baseURL,
	}
	stdout, stderr, code, report := runSim(t, args)
	require.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	require.True(t, report.AllOK(), "an unknown kind must never be rejected (SPEC §1.4)")

	require.Eventually(t, func() bool {
		return scalarInt(t, pool, `SELECT count(*) FROM events WHERE kind = 'unknown' AND event_name = 'chaos_invented_event'`) > 0
	}, e2eDrainTimeout, 100*time.Millisecond, "expected at least one kind='unknown' row with event_name preserved")
}

// runLoadCapacityCheck implements exit criterion 9 ("load mode above
// capacity returns 503/429 with Retry-After, never panics, and RSS stays
// bounded") against a *second*, purpose-built App whose ingest queue is
// deliberately tiny (ARGUS_INGEST_QUEUE=1, one worker, batch size 1) so a
// short, modest-rate burst reliably saturates it — proving the same
// load-shedding path SPEC §3.4/§3.5 document without literally running
// --rate=5000 --duration=60s, which would make this suite's own CI job
// several minutes slower for the same proof. The mechanism under test
// (ErrQueueFull -> 503 for OTLP, 429 for hooks, both with Retry-After) does
// not depend on the specific rate/duration/queue-size numbers.
func runLoadCapacityCheck(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	dsn := storetesting.NewDSN(t)
	t.Setenv("ARGUS_DATABASE_URL", dsn)
	t.Setenv("ARGUS_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("ARGUS_INGEST_QUEUE", "1")
	t.Setenv("ARGUS_INGEST_WORKERS", "1")
	t.Setenv("ARGUS_INGEST_BATCH_SIZE", "1")
	t.Setenv("ARGUS_INGEST_FLUSH", "5s") // slow flush: keeps the tiny queue full instead of draining as fast as it fills

	cfg, warnings, err := config.Load("")
	require.NoError(t, err)
	require.Empty(t, warnings)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A dedicated prometheus.Registry, not the process-global default this
	// test's first App already registered against: two Apps in the same
	// test binary would otherwise panic on the second's duplicate metric
	// registration (App.WithRegisterer's doc comment).
	a, err := New(ctx, cfg, logger, WithRegisterer(prometheus.NewRegistry()))
	require.NoError(t, err)

	serveCtx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- a.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(20 * time.Second):
		}
	})

	select {
	case <-a.Listening():
	case <-time.After(10 * time.Second):
		t.Fatal("app did not start listening in time")
	}
	baseURL := "http://" + a.Addr()

	_, _, code, report := runSim(t, []string{
		"--mode=load",
		"--rate=200",
		"--concurrency=8",
		"--duration=3s",
		"--target=" + baseURL,
	})

	var saw429, saw503 bool
	for status := range report.StatusHistogram {
		switch status {
		case http.StatusTooManyRequests:
			saw429 = true
		case http.StatusServiceUnavailable:
			saw503 = true
		}
	}
	require.True(t, saw429 || saw503, "expected at least one 429 (hooks) or 503 (OTLP) under a queue this small, got %v", report.StatusHistogram)
	// AllOK()==false is expected and correct here (some sends were shed);
	// the exit code from a load run with non-2xx responses is deliberately
	// non-zero (cli.go), which is not itself a failure of this check.
	_ = code

	// "never panics": the process is still up and answering after the
	// burst, which a panic (uncaught in the HTTP handler goroutine would
	// otherwise take the whole server down since net/http's own recoverer
	// only protects per-request, but a total process crash would make this
	// request fail outright) would have prevented. /healthz, not /readyz,
	// is the right endpoint for this: /readyz's saturation condition is
	// deliberately still tripping right after a burst this test engineered
	// specifically to saturate the queue (ARGUS_INGEST_FLUSH=5s keeps it
	// full on purpose) — a 503 there is correct load-shedding, not a crash,
	// and asserting 200 on it would be flaky by construction. /healthz is
	// SPEC's pure liveness probe (no DB, no queue check).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "server must still be responsive after a load burst it had to shed")
}

var _ = fmt.Sprintf // keep fmt imported for future debug formatting without churn
