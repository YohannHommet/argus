package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
	"github.com/YohannHommet/argus/server/internal/model"
)

// rollupLockKeyForTest mirrors rollups.go's unexported rollupLockKey
// (0x41_52_47_55_53_30_32, "ARGUS02"): TestRunRollups_SecondConcurrentInvocationReturnsImmediately
// holds this same key from a separate session to simulate a concurrent
// rollup pass, so it must stay in sync with that constant.
const rollupLockKeyForTest = int64(0x41_52_47_55_53_30_32)

var metricIDCounter int

// mkMetricSample builds a model.MetricSample fixture with a caller-chosen
// temporality/series/attrs/session — unlike write_test.go's mkMetric
// (fixed temporality="delta", no session, attrs limited to a uniqueness
// discriminator), these tests need cumulative series, per-sample attrs
// (type/model/decision), and session attribution. It does not run a real
// dedup-key hasher: WriteMetrics only needs DedupKey to be unique per
// logical sample.
func mkMetricSample(t *testing.T, ts time.Time, name string, sessionID *string, value float64, temporality string, seriesHash []byte, attrs map[string]any) model.MetricSample {
	t.Helper()
	metricIDCounter++
	return model.MetricSample{
		TS:          ts,
		IngestedAt:  ts,
		Name:        name,
		Vendor:      "claude_code",
		SessionID:   sessionID,
		Value:       value,
		Temporality: temporality,
		SeriesHash:  seriesHash,
		Attrs:       attrs,
		DedupKey:    fmt.Sprintf("test-metric-dedup-%d", metricIDCounter),
	}
}

// --- AC: rollup totals equal direct events aggregates --------------------

func TestRunRollups_EventTotalsMatchDirectAggregate(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 16, 8, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-direct-agg"
	events := []model.Event{
		mkEvent(t, sessionID, model.KindSessionStart, model.SourceHook, base, withAttrs(map[string]any{"cwd": "/x/proj", "source": "startup"})),
		mkEvent(t, sessionID, model.KindTurnStart, model.SourceOTelLog, base.Add(time.Minute)),
		mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base.Add(2*time.Minute), withModel("claude-a"), withTokens(10, 20), withCost(0.05, "reported")),
		mkEvent(t, sessionID, model.KindLLMError, model.SourceOTelLog, base.Add(3*time.Minute), withModel("claude-a")),
	}
	_, err := st.WriteBatch(ctx, events)
	require.NoError(t, err)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var sessionsStarted, turns int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT sessions_started, turns FROM rollup_hourly WHERE bucket=$1 AND project='proj' AND model='' AND source='event'`,
		base).Scan(&sessionsStarted, &turns))
	require.Equal(t, 1, sessionsStarted)
	require.Equal(t, 1, turns)

	var apiRequests, apiErrors int
	var inputTokens, outputTokens int64
	var cost float64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT api_requests, api_errors, input_tokens, output_tokens, cost_reported_usd::float8
		 FROM rollup_hourly WHERE bucket=$1 AND project='proj' AND model='claude-a' AND source='event'`,
		base).Scan(&apiRequests, &apiErrors, &inputTokens, &outputTokens, &cost))
	require.Equal(t, 1, apiRequests)
	require.Equal(t, 1, apiErrors)
	require.Equal(t, int64(10), inputTokens)
	require.Equal(t, int64(20), outputTokens)
	require.InDelta(t, 0.05, cost, 1e-9)

	var directCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE session_id=$1 AND kind='llm.request'`, sessionID).Scan(&directCount))
	require.Equal(t, apiRequests, directCount)
}

// --- AC: running twice changes nothing ------------------------------------

func TestRunRollups_RunningTwiceChangesNothing(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 15, 5, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-idempotent"
	ev := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base, withTokens(7, 8), withModel("claude-z"), withCost(1.5, "reported"))
	_, err := st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var apiReq1 int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT api_requests FROM rollup_hourly WHERE bucket=$1 AND model='claude-z' AND source='event'`, base).Scan(&apiReq1))
	require.Equal(t, 1, apiReq1)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var apiReq2 int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT api_requests FROM rollup_hourly WHERE bucket=$1 AND model='claude-z' AND source='event'`, base).Scan(&apiReq2))
	require.Equal(t, apiReq1, apiReq2)
}

// --- AC: an event inserted 20 minutes in the past self-corrects exactly its
// bucket, and no other bucket.

func TestRunRollups_LateEventSelfCorrectsExactlyItsBucket(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	pastBucket := time.Date(2026, 6, 17, 11, 0, 0, 0, time.UTC)
	ensureRange(t, st, pastBucket, pastBucket)

	// A pass with nothing to do yet.
	_, err := st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var existsBefore bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rollup_hourly WHERE bucket=$1 AND source='event')`, pastBucket).Scan(&existsBefore))
	require.False(t, existsBefore)

	sessionID := "session-late-event"
	ev := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, pastBucket.Add(20*time.Minute), withModel("claude-late"))
	_, err = st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var apiRequests int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT api_requests FROM rollup_hourly WHERE bucket=$1 AND model='claude-late' AND source='event'`, pastBucket).Scan(&apiRequests))
	require.Equal(t, 1, apiRequests)

	// This test's schema is otherwise empty (storetesting.NewPool scopes
	// each test to its own schema), so exactly one rollup_hourly row must
	// exist in total — the current/previous real-wall-clock hour (always
	// reprocessed, SPEC §2.4 step 3) never produced a row since it has no
	// data.
	var totalRows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM rollup_hourly`).Scan(&totalRows))
	require.Equal(t, 1, totalRows)
}

// --- AC: review blocker B4's concurrency test -----------------------------
//
// Two transactions insert into different hours and commit in the REVERSE
// order of their seq allocation: txA's INSERT runs first (lower seq) but
// commits last; txB's INSERT runs second (higher seq) but commits first.
// The rollup job runs between the two commits.
//
// Why this would fail under a seq watermark (reasoned through, not
// simulated): a watermark design advances "last processed seq" to the
// highest seq visible at each run. After txB commits and the job runs
// once, the only visible row is txB's (say seq=101); the watermark would
// advance to 101. When txA then commits its row (seq=100, allocated before
// B's but committed after), a query of "events WHERE seq > 101" can never
// see it again — seq 100 is permanently below the watermark. rollup_dirty
// has no such window: txA's dirty mark for its own bucket is written
// inside txA's own transaction (dirty.go's markRollupDirty) and becomes
// visible atomically with txA's commit, regardless of any watermark a
// concurrent job run may have already advanced past. This test asserts the
// bucket txA committed to is correct *after* txA's commit, which a
// watermark implementation would get wrong (0 rather than 1) and this
// rollup_dirty-based one gets right.
func TestRunRollups_B4_ReverseCommitOrderStillCorrectsEarlierBucket(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base.Add(3*time.Hour))

	hourX := base                    // txA's bucket: lower seq, commits SECOND
	hourY := base.Add(2 * time.Hour) // txB's bucket: higher seq, commits FIRST

	txA, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = txA.Rollback(ctx) }()

	var seqX int64
	require.NoError(t, txA.QueryRow(ctx, `
		INSERT INTO events (ts, session_id, vendor, source, kind, event_name, model, cost_source, cost_usd, dedup_key)
		VALUES ($1, 'session-b4-x', 'claude_code', 'otel_log', 'llm.request', 'api_request', 'claude-b4', 'reported', 1.0, 'b4-dedup-x')
		RETURNING seq`, hourX).Scan(&seqX))
	_, err = txA.Exec(ctx, `INSERT INTO rollup_dirty (bucket, source) VALUES ($1, 'event') ON CONFLICT DO NOTHING`, hourX)
	require.NoError(t, err)
	// txA does NOT commit yet.

	txB, err := pool.Begin(ctx)
	require.NoError(t, err)
	var seqY int64
	require.NoError(t, txB.QueryRow(ctx, `
		INSERT INTO events (ts, session_id, vendor, source, kind, event_name, model, cost_source, cost_usd, dedup_key)
		VALUES ($1, 'session-b4-y', 'claude_code', 'otel_log', 'llm.request', 'api_request', 'claude-b4', 'reported', 1.0, 'b4-dedup-y')
		RETURNING seq`, hourY).Scan(&seqY))
	_, err = txB.Exec(ctx, `INSERT INTO rollup_dirty (bucket, source) VALUES ($1, 'event') ON CONFLICT DO NOTHING`, hourY)
	require.NoError(t, err)
	require.NoError(t, txB.Commit(ctx)) // B commits FIRST.

	require.Less(t, seqX, seqY, "txA's insert (issued first) must allocate the lower seq even though it commits second — the whole point of this test")

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var apiRequestsY int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT api_requests FROM rollup_hourly WHERE bucket=$1 AND model='claude-b4' AND source='event'`, hourY).Scan(&apiRequestsY))
	require.Equal(t, 1, apiRequestsY)

	var existsX bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rollup_hourly WHERE bucket=$1 AND source='event')`, hourX).Scan(&existsX))
	require.False(t, existsX, "hourX has nothing to show yet — txA has not committed")

	require.NoError(t, txA.Commit(ctx)) // A commits SECOND, after the job already ran once.

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var apiRequestsX int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT api_requests FROM rollup_hourly WHERE bucket=$1 AND model='claude-b4' AND source='event'`, hourX).Scan(&apiRequestsX))
	require.Equal(t, 1, apiRequestsX, "txA's bucket must self-correct once txA commits, regardless of its lower seq")
}

// --- AC: a rolled-back job leaves the dirty rows intact -------------------

func TestRunRollups_RolledBackJobLeavesDirtyRowsIntact(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 14, 3, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	// Two individually valid cost_usd values (numeric(16,8) allows up to 8
	// integer digits), on two DIFFERENT sessions so sessions.cost_usd (which
	// sums per-session, same precision) never overflows at ingest time —
	// only rollup_hourly.cost_reported_usd's cross-session SUM does, once
	// the event pass aggregates both sessions' events into the same
	// (bucket, project, vendor, model) group. That is a genuine Postgres
	// error mid-recompute, forcing RunRollups's whole transaction
	// (including its earlier dirty-bucket claim) to roll back.
	e1 := mkEvent(t, "session-rollback-rollup-1", model.KindLLMRequest, model.SourceOTelLog, base, withCost(90_000_000, "reported"))
	e2 := mkEvent(t, "session-rollback-rollup-2", model.KindLLMRequest, model.SourceOTelLog, base.Add(time.Second), withCost(90_000_000, "reported"))
	_, err := st.WriteBatch(ctx, []model.Event{e1, e2})
	require.NoError(t, err)

	var dirtyBefore int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM rollup_dirty WHERE bucket=$1 AND source='event'`, base).Scan(&dirtyBefore))
	require.Equal(t, 1, dirtyBefore)

	_, err = st.RunRollups(ctx, 200)
	require.Error(t, err)

	var dirtyAfter int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM rollup_dirty WHERE bucket=$1 AND source='event'`, base).Scan(&dirtyAfter))
	require.Equal(t, 1, dirtyAfter, "a rolled-back rollup pass must leave the dirty row intact for the next run")

	var rowExists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rollup_hourly WHERE bucket=$1)`, base).Scan(&rowExists))
	require.False(t, rowExists, "no partial rollup_hourly row must survive a rolled-back pass")
}

// --- AC: review M4's late-project test ------------------------------------

func TestRunRollups_LateProjectMovesFromUnknownToRealProject(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-m4"

	// The event arrives before SessionStart: upsert_session creates the
	// session row with project=NULL, so the rollup attributes it to
	// project=''.
	llm := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base, withModel("claude-m4"), withCost(0.5, "reported"))
	_, err := st.WriteBatch(ctx, []model.Event{llm})
	require.NoError(t, err)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var apiRequestsUnknown int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT api_requests FROM rollup_hourly WHERE bucket=$1 AND project='' AND model='claude-m4' AND source='event'`, base).Scan(&apiRequestsUnknown))
	require.Equal(t, 1, apiRequestsUnknown)

	// The SessionStart hook lands late, naming the real project — a
	// NULL -> value project transition, which upsert_session re-marks
	// dirty for the session's whole [first_seen_at, last_event_at] span
	// (dirty.go's projectChangeRemarks).
	start := mkEvent(t, sessionID, model.KindSessionStart, model.SourceHook, base.Add(30*time.Second), withAttrs(map[string]any{"cwd": "/home/user/realproj", "source": "startup"}))
	_, err = st.WriteBatch(ctx, []model.Event{start})
	require.NoError(t, err)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var unknownRowExists bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM rollup_hourly WHERE bucket=$1 AND project='' AND model='claude-m4' AND source='event')`, base).Scan(&unknownRowExists))
	require.False(t, unknownRowExists, "the project='' bucket must self-correct away once the real project is known")

	var apiRequestsReal int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT api_requests FROM rollup_hourly WHERE bucket=$1 AND project='realproj' AND model='claude-m4' AND source='event'`, base).Scan(&apiRequestsReal))
	require.Equal(t, 1, apiRequestsReal, "the event must now be attributed to the real project")
}

// --- AC: cumulative metric points produce correct deltas; a value decrease
// takes the raw value.

func TestRunRollups_CumulativeMetricDeltasAndCounterReset(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-metric-cum"
	seriesHash := []byte("test-series-cumulative-1")
	samples := []model.MetricSample{
		mkMetricSample(t, base, "claude_code.token.usage", &sessionID, 100, "cumulative", seriesHash, map[string]any{"type": "input", "model": "claude-x"}),
		mkMetricSample(t, base.Add(time.Minute), "claude_code.token.usage", &sessionID, 150, "cumulative", seriesHash, map[string]any{"type": "input", "model": "claude-x"}),
		// A counter reset: the raw value is taken instead of a negative diff.
		mkMetricSample(t, base.Add(2*time.Minute), "claude_code.token.usage", &sessionID, 80, "cumulative", seriesHash, map[string]any{"type": "input", "model": "claude-x"}),
	}
	_, err := st.WriteMetrics(ctx, samples)
	require.NoError(t, err)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var inputTokens int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT input_tokens FROM rollup_hourly WHERE bucket=$1 AND source='metric' AND model='claude-x'`, base).Scan(&inputTokens))
	// deltas: 100 (no prior point), 150-100=50, then a reset (80 < 150) takes the raw value 80.
	require.Equal(t, int64(100+50+80), inputTokens)

	rows, err := pool.Query(ctx, `SELECT value, delta FROM metric_samples WHERE series_hash=$1 ORDER BY ts`, seriesHash)
	require.NoError(t, err)
	defer rows.Close()
	var got []float64
	for rows.Next() {
		var value float64
		var delta *float64
		require.NoError(t, rows.Scan(&value, &delta))
		require.NotNil(t, delta, "the rollup job must fill delta for every cumulative sample")
		got = append(got, *delta)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []float64{100, 50, 80}, got)
}

// --- AC: a second concurrent job invocation returns immediately ----------

func TestRunRollups_SecondConcurrentInvocationReturnsImmediately(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()

	holder, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(ctx) }()

	var locked bool
	require.NoError(t, holder.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, rollupLockKeyForTest).Scan(&locked))
	require.True(t, locked, "test setup must hold the lock for this assertion to mean anything")

	stats, err := st.RunRollups(ctx, 200)
	require.NoError(t, err)
	require.Zero(t, stats.BucketsClaimed)
	require.Zero(t, stats.BucketsRecomputed)

	require.NoError(t, holder.Rollback(ctx))

	// Sanity check: with the lock free, a normal call actually does work,
	// proving the zero result above was the lock and not a broken RunRollups.
	stats2, err := st.RunRollups(ctx, 200)
	require.NoError(t, err)
	require.NotZero(t, stats2.BucketsRecomputed)
}

// --- AC: source='event' and source='metric' rows coexist and are never
// summed.

func TestRunRollups_EventAndMetricSourcesCoexistNeverSummed(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 13, 14, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-coexist"
	ev := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base, withModel("claude-y"), withCost(2.0, "reported"))
	_, err := st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err)

	seriesHash := []byte("test-series-coexist-1")
	metric := mkMetricSample(t, base, "claude_code.cost.usage", &sessionID, 3.0, "delta", seriesHash, map[string]any{"model": "claude-y"})
	_, err = st.WriteMetrics(ctx, []model.MetricSample{metric})
	require.NoError(t, err)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var eventCost, metricCost float64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT cost_reported_usd::float8 FROM rollup_hourly WHERE bucket=$1 AND source='event' AND model='claude-y'`, base).Scan(&eventCost))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT cost_reported_usd::float8 FROM rollup_hourly WHERE bucket=$1 AND source='metric' AND model='claude-y'`, base).Scan(&metricCost))
	require.InDelta(t, 2.0, eventCost, 1e-9)
	require.InDelta(t, 3.0, metricCost, 1e-9)
}

// --- P3-05 defect 1: rollup_hourly.tool_calls/tool_rejects must count one
// row per real tool call regardless of which surface reported it, and stay
// consistent with sessions.tool_call_count/tool_reject_count. Before the
// fix, AggregateEventRollup counted `count(*) FILTER (WHERE kind =
// 'tool.pre')`, and tool.pre is produced ONLY by the PreToolUse hook (SPEC
// §1.5.1/§1.5.2) — so an OTel-only deployment (hooks disabled,
// `/api/v1/meta`'s hooks_seen: false) always rolled up tool_calls=0 no
// matter how many tool calls actually happened. This test seeds a session
// whose only tool-call evidence is OTel log events (a tool_decision, which
// carries no tool.pre companion) and asserts the rollup shows the real
// count. Run against the pre-fix AggregateEventRollup query, this test
// fails with tool_calls=0 (verified: reverting AggregateEventRollup's
// COUNT(*) FILTER (WHERE kind='tool.pre') and re-running reproduces
// exactly that).

func TestRunRollups_ToolCallsFromOTelOnlyDeployment_CountsRealCalls(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-tc-otel-only-rollup"
	toolUseID := "toolu_rollup_otelonly_1"
	promptID := "prompt-otel-only"

	// No tool.pre anywhere — simulates an OTel-only deployment where the
	// PreToolUse hook never fires. tool_decision alone is enough to create
	// a tool_calls row (SPEC §1.6) with a real started_at.
	decision := mkEvent(t, sessionID, model.KindToolDecision, model.SourceOTelLog, base.Add(time.Minute),
		withPromptID(promptID), func(e *model.Event) {
			e.ToolName = ptrString("Read")
			e.ToolUseID = &toolUseID
			e.Decision = ptrString("accept")
			e.DecisionSource = ptrString("config")
		})
	result := mkEvent(t, sessionID, model.KindToolResult, model.SourceOTelLog, base.Add(2*time.Minute),
		withPromptID(promptID), func(e *model.Event) {
			e.ToolName = ptrString("Read")
			e.ToolUseID = &toolUseID
			ok := true
			e.Success = &ok
		})

	_, err := st.WriteBatch(ctx, []model.Event{decision, result})
	require.NoError(t, err)

	calls, rejects := sessionToolCounters(t, pool, sessionID)
	require.Equal(t, 1, calls, "sessions.tool_call_count must see the OTel-only call")
	require.Equal(t, 0, rejects)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var toolCalls, toolRejects int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT tool_calls, tool_rejects FROM rollup_hourly WHERE bucket=$1 AND project='' AND model='' AND source='event'`,
		base).Scan(&toolCalls, &toolRejects))
	require.Equal(t, 1, toolCalls, "an OTel-only tool call must not roll up to 0 (SPEC §4.1: no silent zero on the headline metric)")
	require.Equal(t, 0, toolRejects)
	require.Equal(t, calls, toolCalls, "rollup tool_calls must agree with sessions.tool_call_count")
}

// TestRunRollups_ToolRejectsFromOTelOnlyDeployment_CountsRealRejects is the
// tool_rejects half of the same defect: a reject decision reported only by
// OTel (no PermissionDenied hook) must still be counted.
func TestRunRollups_ToolRejectsFromOTelOnlyDeployment_CountsRealRejects(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-tc-otel-only-reject"
	toolUseID := "toolu_rollup_otelonly_reject"
	promptID := "prompt-otel-only-reject"

	decision := mkEvent(t, sessionID, model.KindToolDecision, model.SourceOTelLog, base.Add(time.Minute),
		withPromptID(promptID), func(e *model.Event) {
			e.ToolName = ptrString("Bash")
			e.ToolUseID = &toolUseID
			e.Decision = ptrString("reject")
			e.DecisionSource = ptrString("user_permanent_reject")
		})

	_, err := st.WriteBatch(ctx, []model.Event{decision})
	require.NoError(t, err)

	calls, rejects := sessionToolCounters(t, pool, sessionID)
	require.Equal(t, 1, calls)
	require.Equal(t, 1, rejects, "sessions.tool_reject_count must see the OTel-only reject")

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var toolCalls, toolRejects int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT tool_calls, tool_rejects FROM rollup_hourly WHERE bucket=$1 AND project='' AND model='' AND source='event'`,
		base).Scan(&toolCalls, &toolRejects))
	require.Equal(t, 1, toolCalls)
	require.Equal(t, 1, toolRejects, "an OTel-only reject must not roll up to 0")
}

// TestRunRollups_ToolCallsFromHookOnlyDeployment_StillCounted keeps hook-
// sourced coverage alongside the new OTel-only coverage above: a call known
// only via PreToolUse/PostToolUse hooks (no tool_use_id at all, so
// correlation is hook_only) must also roll up correctly — the fix must not
// have traded one blind spot for another.
func TestRunRollups_ToolCallsFromHookOnlyDeployment_StillCounted(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 19, 11, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-tc-hook-only-rollup"
	promptID := "prompt-hook-only"

	pre := hookEvent(t, base.Add(time.Minute), "PreToolUse", sessionID, map[string]any{
		"prompt_id": promptID, "tool_name": "Write",
	})
	post := hookEvent(t, base.Add(2*time.Minute), "PostToolUse", sessionID, map[string]any{
		"prompt_id": promptID, "tool_name": "Write",
	})

	_, err := st.WriteBatch(ctx, []model.Event{pre, post})
	require.NoError(t, err)

	calls, _ := sessionToolCounters(t, pool, sessionID)
	require.Equal(t, 1, calls)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var toolCalls int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT tool_calls FROM rollup_hourly WHERE bucket=$1 AND project='' AND model='' AND source='event'`,
		base).Scan(&toolCalls))
	require.Equal(t, 1, toolCalls, "a hook-only tool call must still be counted after the fix")
}

// TestRunRollups_ToolCallDirtyMarkFollowsStartedAtHourNotTriggeringEventHour
// is the dirty-marking half of defect 1: tool_calls/tool_rejects are now
// bucketed on tool_calls.started_at, not on the ts of whichever event most
// recently touched the row, so the bucket that needs recomputing can differ
// from the hour the triggering event's own ts falls in. A PreToolUse hook
// opens the call in hourX; a slow OTel tool_decision (reject) for the same
// tool_use_id is delivered in hourY, three hours later — started_at stays
// LEAST(hourX, hourY) = hourX, but the triggering decision event's own ts
// is in hourY. Only hourX's rollup_hourly row is expected to show the
// reject; this asserts write.go's dirty-marking fix (marking
// tool_calls.started_at's hour, not just the event's own ts hour) actually
// gets hourX recomputed.
func TestRunRollups_ToolCallDirtyMarkFollowsStartedAtHourNotTriggeringEventHour(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	hourX := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	hourY := hourX.Add(3 * time.Hour)
	ensureRange(t, st, hourX, hourY)

	sessionID := "session-tc-dirty-cross-hour"
	toolUseID := "toolu_dirty_cross_hour"
	promptID := "prompt-dirty-cross-hour"

	pre := hookEvent(t, hourX.Add(time.Minute), "PreToolUse", sessionID, map[string]any{
		"prompt_id": promptID, "tool_name": "Bash", "tool_use_id": toolUseID,
	})
	_, err := st.WriteBatch(ctx, []model.Event{pre})
	require.NoError(t, err)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var toolCallsBefore, toolRejectsBefore int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT tool_calls, tool_rejects FROM rollup_hourly WHERE bucket=$1 AND project='' AND model='' AND source='event'`,
		hourX).Scan(&toolCallsBefore, &toolRejectsBefore))
	require.Equal(t, 1, toolCallsBefore)
	require.Equal(t, 0, toolRejectsBefore)

	// The reject decision arrives three hours later, in hourY, correlating
	// to the same call by tool_use_id — this updates the EXISTING
	// tool_calls row (started_at stays hourX via LEAST) rather than
	// creating a new one.
	decision := mkEvent(t, sessionID, model.KindToolDecision, model.SourceOTelLog, hourY.Add(time.Minute),
		withPromptID(promptID), func(e *model.Event) {
			e.ToolName = ptrString("Bash")
			e.ToolUseID = &toolUseID
			e.Decision = ptrString("reject")
			e.DecisionSource = ptrString("hook")
		})
	_, err = st.WriteBatch(ctx, []model.Event{decision})
	require.NoError(t, err)

	// Sanity check: the row's started_at truly did not move to hourY.
	id := normalize.ToolCallID(sessionID, &toolUseID, nil, "", 0).String()
	row := queryToolCall(t, pool, id)
	require.True(t, row.StartedAt.Equal(hourX.Add(time.Minute)), "started_at must stay anchored to the original PreToolUse hook, not the later decision")

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var toolCallsAfter, toolRejectsAfter int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT tool_calls, tool_rejects FROM rollup_hourly WHERE bucket=$1 AND project='' AND model='' AND source='event'`,
		hourX).Scan(&toolCallsAfter, &toolRejectsAfter))
	require.Equal(t, 1, toolCallsAfter)
	require.Equal(t, 1, toolRejectsAfter, "hourX must self-correct to show the reject even though the triggering event's own ts was in hourY")

	var hourYHasRow bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM rollup_hourly WHERE bucket=$1 AND project='' AND model='' AND source='event' AND tool_calls > 0)`,
		hourY).Scan(&hourYHasRow))
	require.False(t, hourYHasRow, "the call must be attributed to hourX (its started_at), never hourY (the decision event's own ts)")
}

// --- P3-05 defect 3: genuinely untested behaviour in P3-05's own code
// (cost estimation for uncosted llm.request tokens, and the merge branch
// where a rollup_hourly group is produced ONLY by AggregateToolCallRollup
// because the events that created it are gone from the retained `events`
// table). Neither had a single assertion anywhere in this package before.

// TestRunRollups_UncostedLLMRequestEstimatedFromModelPrices exercises
// recomputeEventBuckets's estimateCost call (rollups.go) end to end: an
// llm.request with real token counts and no reported cost_usd must have
// its cost_estimated_usd computed from the seeded model_prices table via
// internal/pricing.Estimate (defect 2's single implementation), matching
// that package's own documented rate for claude-opus-5.
func TestRunRollups_UncostedLLMRequestEstimatedFromModelPrices(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	summary, err := st.ImportPrices(ctx)
	require.NoError(t, err)
	require.Positive(t, summary.Inserted)

	sessionID := "session-uncosted-priced"
	// No withCost: cost_usd stays NULL, so this llm.request is exactly the
	// "uncosted" case AggregateEventRollup's uncosted_* sums select.
	ev := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base,
		withModel("claude-opus-5"), withTokens(1_000_000, 1_000_000))
	_, err = st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var reported, estimated float64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT cost_reported_usd::float8, cost_estimated_usd::float8 FROM rollup_hourly
		 WHERE bucket=$1 AND model='claude-opus-5' AND source='event'`, base).Scan(&reported, &estimated))
	require.Zero(t, reported, "an uncosted request must not contribute to cost_reported_usd")
	// db/prices/model_prices.json seeds claude-opus-5 at input=15, output=75
	// per Mtok effective 2025-01-01 (also asserted directly by
	// internal/pricing's own TestEstimate) — 1M input + 1M output tokens.
	require.InDelta(t, 15+75, estimated, 1e-6, "cost_estimated_usd must match internal/pricing.Estimate's rate for claude-opus-5")
}

// TestRunRollups_UncostedLLMRequestWithNoMatchingPrice_StaysZero is the
// estimateCost ok=false half of the same code path: an uncosted request for
// a model with no model_prices row must roll up to an estimated cost of
// exactly 0 (never dropped, never a stand-in from another model), while its
// other already-computed counters (api_requests, tokens) are unaffected.
func TestRunRollups_UncostedLLMRequestWithNoMatchingPrice_StaysZero(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	// Deliberately no ImportPrices call: model_prices is empty, so every
	// model is unresolvable.
	sessionID := "session-uncosted-unpriced"
	ev := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base,
		withModel("some-unknown-model"), withTokens(1_000_000, 1_000_000))
	_, err := st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var apiRequests int
	var inputTokens int64
	var estimated float64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT api_requests, input_tokens, cost_estimated_usd::float8 FROM rollup_hourly
		 WHERE bucket=$1 AND model='some-unknown-model' AND source='event'`, base).Scan(&apiRequests, &inputTokens, &estimated))
	require.Equal(t, 1, apiRequests, "an unresolvable price must not drop the request from its other counters")
	require.Equal(t, int64(1_000_000), inputTokens)
	require.Zero(t, estimated, "an unresolvable price must estimate exactly 0, never another model's rate")
}

// TestRunRollups_ToolCallSurvivesRawEventDeletion_StillRolledUp is the
// AggregateToolCallRollup-only merge branch (recomputeEventBuckets's `get`
// helper creating a fresh zero eventGroupAgg because AggregateEventRollup
// produced no row for that key): SPEC §2.4 states raw retention drops
// `events` partitions but never rollups or projections, so a tool_calls row
// whose originating events have since been deleted must still roll up
// correctly the next time its bucket is (re)computed — exactly the
// "tool_calls survives event retention" case this merge exists for. This
// test does not run the real retention job; it deletes the row directly
// (SPEC's documented end state of retention) and re-arms rollup_dirty by
// hand, the same manual-dirty-mark pattern the B4 test above uses.
func TestRunRollups_ToolCallSurvivesRawEventDeletion_StillRolledUp(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 20, 11, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-tc-survives-retention"
	promptID := "prompt-survives-retention"
	pre := hookEvent(t, base.Add(time.Minute), "PreToolUse", sessionID, map[string]any{
		"prompt_id": promptID, "tool_name": "Grep",
	})
	_, err := st.WriteBatch(ctx, []model.Event{pre})
	require.NoError(t, err)

	// Simulate raw retention: the events partition is dropped, but
	// tool_calls (a projection) is untouched (SPEC §2.4).
	_, err = pool.Exec(ctx, `DELETE FROM events WHERE session_id = $1`, sessionID)
	require.NoError(t, err)

	var eventsLeft int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE session_id = $1`, sessionID).Scan(&eventsLeft))
	require.Zero(t, eventsLeft, "test setup: the raw event must actually be gone")

	// Re-arm rollup_dirty for this bucket by hand, exactly like the B4 test
	// does, since deleting events directly bypasses the normal
	// markRollupDirty path.
	_, err = pool.Exec(ctx, `INSERT INTO rollup_dirty (bucket, source) VALUES ($1, 'event') ON CONFLICT DO NOTHING`, base)
	require.NoError(t, err)

	_, err = st.RunRollups(ctx, 200)
	require.NoError(t, err)

	var toolCalls int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT tool_calls FROM rollup_hourly WHERE bucket=$1 AND project='' AND model='' AND source='event'`,
		base).Scan(&toolCalls))
	require.Equal(t, 1, toolCalls, "a tool call must still roll up even when AggregateEventRollup has no row at all for its bucket")
}
