package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
	"github.com/YohannHommet/argus/server/internal/store/postgres"
	storetesting "github.com/YohannHommet/argus/server/internal/store/testing"
)

// testUUID produces a syntactically valid uuid literal (Postgres's uuid type
// only checks the 8-4-4-4-12 hex-digit shape, not RFC 4122 version/variant
// bits), deterministic from n, so tests never depend on a real UUID
// generator.
func testUUID(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
}

var idCounter int

func nextID() string {
	idCounter++
	return testUUID(idCounter)
}

// eventOpt mutates an event fixture; ptrString/ptrInt64/ptrFloat64 helpers
// keep call sites terse.
type eventOpt func(*model.Event)

func ptrString(s string) *string    { return &s }
func ptrInt64(i int64) *int64       { return &i }
func ptrFloat64(f float64) *float64 { return &f }

func withPromptID(p string) eventOpt { return func(e *model.Event) { e.PromptID = &p } }
func withTokens(in, out int64) eventOpt {
	return func(e *model.Event) { e.InputTokens = ptrInt64(in); e.OutputTokens = ptrInt64(out) }
}
func withCost(usd float64, source string) eventOpt {
	return func(e *model.Event) { e.CostUSD = ptrFloat64(usd); e.CostSource = ptrString(source) }
}
func withQuerySource(q string) eventOpt { return func(e *model.Event) { e.QuerySource = &q } }
func withModel(m string) eventOpt       { return func(e *model.Event) { e.Model = &m } }
func withAttrs(a map[string]any) eventOpt {
	return func(e *model.Event) { e.Attrs = a }
}
func withID(id string) eventOpt { return func(e *model.Event) { e.ID = id } }

// mkEvent builds a minimal-but-valid model.Event fixture and computes its
// dedup key with the real DedupKey* helpers (model/dedup.go), so tests
// exercise the same idempotency contract WriteBatch does.
func mkEvent(t *testing.T, sessionID string, kind model.Kind, source model.Source, ts time.Time, opts ...eventOpt) model.Event {
	t.Helper()
	e := model.Event{
		ID:         nextID(),
		TS:         ts,
		IngestedAt: ts,
		SessionID:  sessionID,
		Vendor:     "claude_code",
		Source:     source,
		Kind:       kind,
		EventName:  string(kind),
		Attrs:      map[string]any{},
	}
	for _, opt := range opts {
		opt(&e)
	}
	// Every mkEvent call describes a distinct real-world event by default:
	// stamp a uniqueness discriminator into attrs so the hash-fallback
	// dedup key (model.DedupKeyOTelLog with a nil vendor_seq) doesn't
	// collapse two independently-built fixtures that happen to share every
	// other field. Tests that specifically want a dedup collision (AC (b))
	// go through mkEventDedup, which overwrites DedupKey after this runs,
	// so this discriminator never leaks into their assertions.
	if e.Attrs == nil {
		e.Attrs = map[string]any{}
	}
	e.Attrs["_test_seq"] = nextID()

	var err error
	switch source { //nolint:exhaustive // this fixture builder only distinguishes hook vs. everything-else dedup key construction; SourceOTelMetric/SourceSim events use the OTel-log key form here, matching the hash-fallback path exercised by these tests.
	case model.SourceHook:
		promptID := ""
		if e.PromptID != nil {
			promptID = *e.PromptID
		}
		e.DedupKey, err = model.DedupKeyHook(e.EventName, sessionID, promptID, e.Attrs)
	default:
		var seq *int64
		e.DedupKey, err = model.DedupKeyOTelLog(sessionID, seq, e.EventName, e.Attrs)
	}
	require.NoError(t, err)
	return e
}

// mkEventWithSeq is mkEvent but with an explicit vendor_seq/content
// discriminator baked into Attrs, so two calls describing "the same
// logical event" at different ts (AC (b)'s hook-resend regression) still
// collide on dedup_key while two calls in the same test with different
// discriminators don't.
func mkEventDedup(t *testing.T, sessionID string, kind model.Kind, source model.Source, ts time.Time, dedupKey string, opts ...eventOpt) model.Event {
	t.Helper()
	e := mkEvent(t, sessionID, kind, source, ts, opts...)
	e.DedupKey = dedupKey
	return e
}

func newStore(t *testing.T) (*postgres.Store, *pgxpool.Pool) {
	t.Helper()
	pool := storetesting.NewPool(t)
	st := postgres.New(pool)
	return st, pool
}

// ensureRange ensures partitions covering [from, to] exist so ts values in
// that window are never classified too_old (SPEC §1.7 rule 3).
func ensureRange(t *testing.T, st *postgres.Store, from, to time.Time) {
	t.Helper()
	require.NoError(t, st.EnsurePartitions(context.Background(), from, to))
}

// --- AC (a): 500 mixed events insert in one tx, sessions.event_count matches.

func TestWriteBatch_InsertsBatchAndUpdatesEventCount(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-a"
	events := make([]model.Event, 0, 500)
	for i := 0; i < 500; i++ {
		kind := model.KindLLMRequest
		if i%7 == 0 {
			kind = model.KindToolResult
		}
		events = append(events, mkEvent(t, sessionID, kind, model.SourceOTelLog,
			base.Add(time.Duration(i)*time.Millisecond),
			withAttrs(map[string]any{"i": i}), withTokens(1, 2)))
	}

	result, err := st.WriteBatch(ctx, events)
	require.NoError(t, err)
	require.Equal(t, 500, result.Written)
	require.Equal(t, 0, result.Deduped)
	require.Len(t, result.EventRefs, 500)

	var eventCount int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT event_count FROM sessions WHERE id = $1`, sessionID).Scan(&eventCount))
	require.Equal(t, int64(500), eventCount)
}

// --- AC (b): re-writing the identical batch persists 0, deduped 500 —
// including a hook event resent with a different ts.

func TestWriteBatch_DedupesIdenticalBatchAndHookTsVariance(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-hook-resend"
	body := map[string]any{"cwd": "/repo"}
	dedupKey, err := model.DedupKeyHook("SessionStart", sessionID, "", body)
	require.NoError(t, err)

	first := mkEventDedup(t, sessionID, model.KindSessionStart, model.SourceHook, base, dedupKey, withAttrs(body))
	r1, err := st.WriteBatch(ctx, []model.Event{first})
	require.NoError(t, err)
	require.Equal(t, 1, r1.Written)

	// Same dedup_key (same hook_event_name/session/prompt/body), ts 1s
	// later — the direct regression test for review blocker B2.
	second := mkEventDedup(t, sessionID, model.KindSessionStart, model.SourceHook, base.Add(time.Second), dedupKey, withAttrs(body), withID(nextID()))
	r2, err := st.WriteBatch(ctx, []model.Event{second})
	require.NoError(t, err)
	require.Equal(t, 0, r2.Written)
	require.Equal(t, 1, r2.Deduped)

	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE dedup_key = $1`, dedupKey).Scan(&count))
	require.Equal(t, 1, count)
}

// --- AC (c): a turn.start arriving after its llm.request events yields
// correct started_at and token sums.

func TestWriteBatch_LateTurnStartYieldsCorrectAggregates(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-late-turn-start"
	promptID := "prompt-1"
	t0 := base
	t1 := base.Add(time.Second)
	t2 := base.Add(2 * time.Second)

	req1 := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, t1, withPromptID(promptID), withTokens(10, 20))
	req2 := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, t2, withPromptID(promptID), withTokens(5, 7))
	_, err := st.WriteBatch(ctx, []model.Event{req1, req2})
	require.NoError(t, err)

	var startedAt *time.Time
	var inputTokens int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT started_at, input_tokens FROM turns WHERE session_id=$1 AND prompt_id=$2`, sessionID, promptID).Scan(&startedAt, &inputTokens))
	require.Nil(t, startedAt, "stub turn must have no started_at yet")
	require.Equal(t, int64(15), inputTokens)

	turnStart := mkEvent(t, sessionID, model.KindTurnStart, model.SourceHook, t0, withPromptID(promptID))
	_, err = st.WriteBatch(ctx, []model.Event{turnStart})
	require.NoError(t, err)

	require.NoError(t, pool.QueryRow(ctx, `SELECT started_at, input_tokens FROM turns WHERE session_id=$1 AND prompt_id=$2`, sessionID, promptID).Scan(&startedAt, &inputTokens))
	require.NotNil(t, startedAt)
	require.True(t, startedAt.Equal(t0), "started_at must be the turn.start ts even though it arrived after llm.request events")
	require.Equal(t, int64(15), inputTokens, "token sums must survive the late turn.start")
}

// --- AC (d): an otel_log write does not overwrite a cwd previously set by a
// hook write, while a later hook write does.

func TestWriteBatch_CWDFieldRankPrecedence(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-cwd-rank"

	hook1 := mkEvent(t, sessionID, model.KindSessionStart, model.SourceHook, base, withAttrs(map[string]any{"cwd": "/hook/one", "source": "cli"}))
	_, err := st.WriteBatch(ctx, []model.Event{hook1})
	require.NoError(t, err)

	var cwd string
	require.NoError(t, pool.QueryRow(ctx, `SELECT cwd FROM sessions WHERE id=$1`, sessionID).Scan(&cwd))
	require.Equal(t, "/hook/one", cwd)

	// otel_log carries a higher generic source rank (30 > hook's 20), but
	// cwd is hook-only by construction (SPEC §1.5.3) — this must not win.
	otel := mkEvent(t, sessionID, model.KindSessionStart, model.SourceOTelLog, base.Add(time.Second), withAttrs(map[string]any{"cwd": "/otel/two"}))
	_, err = st.WriteBatch(ctx, []model.Event{otel})
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, `SELECT cwd FROM sessions WHERE id=$1`, sessionID).Scan(&cwd))
	require.Equal(t, "/hook/one", cwd, "an otel_log write must not overwrite a hook-set cwd")

	hook2 := mkEvent(t, sessionID, model.KindWorkspaceCWDChanged, model.SourceHook, base.Add(2*time.Second), withAttrs(map[string]any{"cwd": "/hook/three"}))
	_, err = st.WriteBatch(ctx, []model.Event{hook2})
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, `SELECT cwd FROM sessions WHERE id=$1`, sessionID).Scan(&cwd))
	require.Equal(t, "/hook/three", cwd, "a later hook write must overwrite the earlier hook-set cwd")
}

// --- AC (e): status is a stored column through its full lifecycle.

func TestWriteBatch_StatusStoredColumnLifecycle(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-status-lifecycle"

	stub := mkEvent(t, sessionID, model.KindToolResult, model.SourceOTelLog, base)
	_, err := st.WriteBatch(ctx, []model.Event{stub})
	require.NoError(t, err)
	require.Equal(t, "unknown", sessionStatus(t, pool, sessionID))

	start := mkEvent(t, sessionID, model.KindSessionStart, model.SourceHook, base.Add(time.Second), withAttrs(map[string]any{"cwd": "/repo"}))
	_, err = st.WriteBatch(ctx, []model.Event{start})
	require.NoError(t, err)
	require.Equal(t, "active", sessionStatus(t, pool, sessionID))

	end := mkEvent(t, sessionID, model.KindSessionEnd, model.SourceHook, base.Add(2*time.Second))
	_, err = st.WriteBatch(ctx, []model.Event{end})
	require.NoError(t, err)
	require.Equal(t, "ended", sessionStatus(t, pool, sessionID))

	// A separate, still-active session gone idle: SweepAbandoned must move it.
	staleID := "session-status-stale"
	staleStart := mkEvent(t, staleID, model.KindSessionStart, model.SourceHook, base, withAttrs(map[string]any{"cwd": "/repo"}))
	_, err = st.WriteBatch(ctx, []model.Event{staleStart})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE sessions SET last_event_at = $1 WHERE id = $2`, time.Now().Add(-24*time.Hour), staleID)
	require.NoError(t, err)

	n, err := st.SweepAbandoned(ctx, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	require.Equal(t, "abandoned", sessionStatus(t, pool, staleID))
}

func sessionStatus(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var status string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT status FROM sessions WHERE id=$1`, id).Scan(&status))
	return status
}

// --- AC (f): a batch failure rolls back completely.

func TestWriteBatch_FailureRollsBackCompletely(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-rollback"
	good := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base, withTokens(3, 4))
	bad := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base.Add(time.Second), withTokens(1, 1))
	// Force a Postgres error on the events INSERT so the whole transaction
	// aborts: cost_usd is numeric(16,8), so it holds at most 8 integer digits
	// and 1e9 overflows the column's precision (SQLSTATE 22003). Deliberately
	// not an empty Event.ID — that used to fail as "invalid input syntax for
	// type uuid", so this test passed for the wrong reason until the insert
	// learned to fall back to the column's uuidv7() default.
	overflow := 1e9
	bad.CostUSD = &overflow

	_, err := st.WriteBatch(ctx, []model.Event{good, bad})
	require.Error(t, err)

	var sessionExists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=$1)`, sessionID).Scan(&sessionExists))
	require.False(t, sessionExists, "no session row must survive a rolled-back batch")

	var eventCount, dedupCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE session_id=$1`, sessionID).Scan(&eventCount))
	require.Equal(t, 0, eventCount)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM ingest_dedup WHERE dedup_key = ANY($1)`, []string{good.DedupKey, bad.DedupKey}).Scan(&dedupCount))
	require.Equal(t, 0, dedupCount, "no dedup-ledger rows must survive a rolled-back batch")
}

// --- AC (g): two concurrent overlapping batches both commit, no dropped
// batch, no 40P01 escaping, run repeatedly under -race.

func TestWriteBatch_ConcurrentOverlappingSessions(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	const rounds = 20
	sessionA, sessionB := "session-concurrent-a", "session-concurrent-b"

	for round := 0; round < rounds; round++ {
		batch1 := []model.Event{
			mkEvent(t, sessionA, model.KindLLMRequest, model.SourceOTelLog, base.Add(time.Duration(round)*time.Minute), withTokens(1, 1)),
			mkEvent(t, sessionB, model.KindLLMRequest, model.SourceOTelLog, base.Add(time.Duration(round)*time.Minute+time.Millisecond), withTokens(1, 1)),
		}
		batch2 := []model.Event{
			mkEvent(t, sessionB, model.KindLLMRequest, model.SourceOTelLog, base.Add(time.Duration(round)*time.Minute+2*time.Millisecond), withTokens(1, 1)),
			mkEvent(t, sessionA, model.KindLLMRequest, model.SourceOTelLog, base.Add(time.Duration(round)*time.Minute+3*time.Millisecond), withTokens(1, 1)),
		}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); _, errs[0] = st.WriteBatch(ctx, batch1) }()
		go func() { defer wg.Done(); _, errs[1] = st.WriteBatch(ctx, batch2) }()
		wg.Wait()

		require.NoError(t, errs[0], "round %d batch1", round)
		require.NoError(t, errs[1], "round %d batch2", round)
	}

	var totalA, totalB int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT event_count FROM sessions WHERE id=$1`, sessionA).Scan(&totalA))
	require.NoError(t, pool.QueryRow(ctx, `SELECT event_count FROM sessions WHERE id=$1`, sessionB).Scan(&totalB))
	require.Equal(t, int64(2*rounds), totalA)
	require.Equal(t, int64(2*rounds), totalB)
}

// --- AC (h): cost_by_query_source accumulates an unseen value as a plain key.

func TestWriteBatch_CostByQuerySourceAccumulatesUnseenValue(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-cost-by-query-source"
	e := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base,
		withQuerySource("a_future_query_source"), withCost(1.23, "reported"), withModel("claude-x"))

	_, err := st.WriteBatch(ctx, []model.Event{e})
	require.NoError(t, err)

	var cost float64
	require.NoError(t, pool.QueryRow(ctx, `SELECT (cost_by_query_source->>'a_future_query_source')::float8 FROM sessions WHERE id=$1`, sessionID).Scan(&cost))
	require.InDelta(t, 1.23, cost, 1e-9)
}

// --- AC (i): a batch spanning 3 hours marks exactly 3 rollup_dirty rows;
// a rolled-back batch leaves zero.

func TestWriteBatch_MarksRollupDirtyForEveryTouchedHour(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-rollup-dirty"
	events := []model.Event{
		mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base, withTokens(1, 1)),
		mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base.Add(time.Hour), withTokens(1, 1)),
		mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base.Add(2*time.Hour), withTokens(1, 1)),
	}
	_, err := st.WriteBatch(ctx, events)
	require.NoError(t, err)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM rollup_dirty WHERE source='event' AND bucket IN ($1,$2,$3)`,
		base, base.Add(time.Hour), base.Add(2*time.Hour)).Scan(&n))
	require.Equal(t, 3, n)
}

func TestWriteBatch_RolledBackBatchLeavesNoDirtyRows(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-rollup-dirty-rollback"
	good := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base, withTokens(1, 1))
	bad := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base.Add(time.Hour), withTokens(1, 1))
	// Inject a genuine Postgres error: cost_usd is numeric(16,8), so it holds
	// at most 8 integer digits and 1e9 overflows the column's precision
	// (SQLSTATE 22003). This deliberately does NOT use an empty Event.ID —
	// that used to fail as "invalid input syntax for type uuid" and so made
	// this test pass for the wrong reason, until the events insert learned to
	// fall back to the column's uuidv7() default.
	overflow := 1e9
	bad.CostUSD = &overflow

	_, err := st.WriteBatch(ctx, []model.Event{good, bad})
	require.Error(t, err)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM rollup_dirty WHERE bucket IN ($1,$2)`, base, base.Add(time.Hour)).Scan(&n))
	require.Equal(t, 0, n)
}

// --- too_old classification (SPEC §1.7 rule 3): no covering partition.

func TestWriteBatch_TooOldEventsAreRejectedNotInserted(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	// Deliberately do NOT EnsurePartitions for this ts: no partition exists.
	tooOld := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	e := mkEvent(t, "session-too-old", model.KindLLMRequest, model.SourceOTelLog, tooOld)
	result, err := st.WriteBatch(ctx, []model.Event{e})
	require.NoError(t, err)
	require.Equal(t, 0, result.Written)
	require.Equal(t, 1, result.TooOld)
	require.Equal(t, 1, result.Rejected)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE dedup_key=$1`, e.DedupKey).Scan(&n))
	require.Equal(t, 0, n)
}

// --- WriteMetrics (P2-06's recorded SPEC §3.3 deviation): insert, dedup,
// mark rollup_dirty with source='metric'.

func mkMetric(t *testing.T, name string, ts time.Time, value float64) model.MetricSample {
	t.Helper()
	attrs := map[string]any{"_test_seq": nextID()}
	dedupKey, err := model.DedupKeyMetric(name, ts, attrs)
	require.NoError(t, err)
	return model.MetricSample{
		TS:          ts,
		IngestedAt:  ts,
		Name:        name,
		Vendor:      "claude_code",
		Value:       value,
		Temporality: "delta",
		SeriesHash:  []byte(name),
		Attrs:       attrs,
		DedupKey:    dedupKey,
	}
}

func TestWriteMetrics_InsertsDedupesAndMarksDirty(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	m1 := mkMetric(t, "claude_code.cost.usage", base, 1.5)
	m2 := mkMetric(t, "claude_code.token.usage", base.Add(time.Minute), 100)

	r1, err := st.WriteMetrics(ctx, []model.MetricSample{m1, m2})
	require.NoError(t, err)
	require.Equal(t, 2, r1.Written)
	require.Equal(t, 0, r1.Deduped)

	r2, err := st.WriteMetrics(ctx, []model.MetricSample{m1, m2})
	require.NoError(t, err)
	require.Equal(t, 0, r2.Written)
	require.Equal(t, 2, r2.Deduped)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM rollup_dirty WHERE source='metric' AND bucket=$1`, base).Scan(&n))
	require.Equal(t, 1, n)
}

func TestWriteMetrics_TooOldRejected(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	tooOld := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)

	m := mkMetric(t, "claude_code.cost.usage", tooOld, 1)
	result, err := st.WriteMetrics(ctx, []model.MetricSample{m})
	require.NoError(t, err)
	require.Equal(t, 0, result.Written)
	require.Equal(t, 1, result.TooOld)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM metric_samples WHERE dedup_key=$1`, m.DedupKey).Scan(&n))
	require.Equal(t, 0, n)
}

// --- store.Writer/store.Maintenance compile-time shape used across tests.

var _ store.Writer = (*postgres.Store)(nil)
