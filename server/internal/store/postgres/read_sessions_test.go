// read_sessions_test.go is a black-box (package postgres_test) integration
// suite for ListSessions/GetSession/ListTurns, matching this package's
// existing convention (write_test.go, upsert_toolcall_test.go, ...) and
// reusing their newStore/ensureRange/ptrString helpers. Every test exercises
// the cursor codec, keyset pagination, filtering, and detail assembly
// through the exported Store API only — including the cursor round-trip and
// sort-key-binding ACs, verified indirectly via postgres.ErrInvalidCursor
// and successive ListSessions calls rather than by importing read_sessions.go's
// unexported codec: a white-box (package postgres) test file in this
// package cannot import internal/store/testing at all (storetesting itself
// imports postgres, so an internal test file importing storetesting would
// be an import cycle — verified empirically, `go vet` rejects it), which is
// why this file, like its siblings, stays external.
//
// Seeding helpers below insert sessions/tool_calls/events directly via SQL
// rather than through WriteBatch/upsertToolCalls: these tests exercise the
// READ side in isolation and need precise control over
// last_event_at/started_at/cost_usd/event_count/decision/correlation values
// that would be awkward to steer indirectly through the write path.
package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
	"github.com/YohannHommet/argus/server/internal/store/postgres"
)

// sessionSeed is the direct-SQL fixture seedSession inserts. Zero values are
// filled with sane defaults (see seedSession) so a test only sets the
// fields it cares about.
type sessionSeed struct {
	ID                string
	Vendor            string
	Project           string
	CWD               string
	Status            string
	StartedAt         *time.Time
	EndedAt           *time.Time
	FirstSeenAt       time.Time
	LastEventAt       time.Time
	EventCount        int64
	CostUSD           float64
	CostEstimatedUSD  float64
	CostByQuerySource map[string]float64
	Models            []string
}

func seedSession(t *testing.T, pool *pgxpool.Pool, seed sessionSeed) {
	t.Helper()
	if seed.Vendor == "" {
		seed.Vendor = "claude_code"
	}
	if seed.Status == "" {
		seed.Status = "active"
	}
	if seed.LastEventAt.IsZero() {
		seed.LastEventAt = time.Now().UTC()
	}
	if seed.FirstSeenAt.IsZero() {
		seed.FirstSeenAt = seed.LastEventAt
	}
	if seed.Models == nil {
		seed.Models = []string{}
	}
	cqsJSON, err := json.Marshal(seed.CostByQuerySource)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(), `
		INSERT INTO sessions (id, vendor, project, cwd, status, started_at, ended_at,
		                       first_seen_at, last_event_at, event_count,
		                       cost_usd, cost_estimated_usd, cost_by_query_source, models)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		seed.ID, seed.Vendor, nullIfEmpty(seed.Project), nullIfEmpty(seed.CWD), seed.Status,
		seed.StartedAt, seed.EndedAt, seed.FirstSeenAt, seed.LastEventAt, seed.EventCount,
		seed.CostUSD, seed.CostEstimatedUSD, cqsJSON, seed.Models,
	)
	require.NoError(t, err)
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type toolCallSeed struct {
	ID             string
	SessionID      string
	ToolName       string
	Decision       *string
	DecisionSource *string
	Correlation    string
	StartedAt      time.Time
	DurationMS     *int
}

func seedToolCall(t *testing.T, pool *pgxpool.Pool, seed toolCallSeed) {
	t.Helper()
	if seed.Correlation == "" {
		seed.Correlation = string(model.CorrelationExact)
	}
	if seed.StartedAt.IsZero() {
		seed.StartedAt = time.Now().UTC()
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO tool_calls (id, session_id, tool_name, decision, decision_source, correlation, started_at, duration_ms, event_count)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1)`,
		seed.ID, seed.SessionID, seed.ToolName, seed.Decision, seed.DecisionSource, seed.Correlation, seed.StartedAt, seed.DurationMS,
	)
	require.NoError(t, err)
}

// seedEvent inserts a minimal events row of kind `kind` for permission-mode
// and hook-latency fixtures. dedupKey must be unique per row (events'
// parent-level UNIQUE (ts, dedup_key) constraint, SPEC §2.2).
func seedEvent(t *testing.T, pool *pgxpool.Pool, sessionID, kind string, ts time.Time, dedupKey string, permissionMode *string, durationMS *int, attrs map[string]any) {
	t.Helper()
	attrsJSON, err := json.Marshal(attrs)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO events (ts, session_id, vendor, source, kind, event_name, permission_mode, duration_ms, dedup_key, attrs)
		VALUES ($1,$2,'claude_code','otel_log',$3,$3,$4,$5,$6,$7)`,
		ts, sessionID, kind, permissionMode, durationMS, dedupKey, attrsJSON,
	)
	require.NoError(t, err)
}

var sessionIDSeq int

func nextTestSessionID(prefix string) string {
	sessionIDSeq++
	return fmt.Sprintf("%s-%d", prefix, sessionIDSeq)
}

func intPtr(n int) *int { return &n }

// --- cursor codec (verified through the exported API only, see file doc) --

func TestListSessions_CursorRejectsWrongSortKey(t *testing.T) {
	st, pool := newStore(t)
	base := time.Now().UTC()
	for i := 0; i < 3; i++ {
		seedSession(t, pool, sessionSeed{ID: nextTestSessionID("wrongsort"), CostUSD: float64(i), LastEventAt: base.Add(time.Duration(i) * time.Second)})
	}

	_, cursor, err := st.ListSessions(context.Background(), store.SessionFilter{Sort: store.SessionSortCostUSD}, store.Page{Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, cursor)

	_, _, err = st.ListSessions(context.Background(), store.SessionFilter{Sort: store.SessionSortLastEventAt}, store.Page{Limit: 1, Cursor: cursor})
	require.Error(t, err)
	require.ErrorIsf(t, err, postgres.ErrInvalidCursor, "a cursor minted under one sort must be rejected when replayed against another")
}

func TestListSessions_CursorTamperRejection(t *testing.T) {
	st, pool := newStore(t)
	seedSession(t, pool, sessionSeed{ID: nextTestSessionID("tamper")})

	_, _, err := st.ListSessions(context.Background(), store.SessionFilter{}, store.Page{Limit: 1, Cursor: "not-a-valid-cursor!!"})
	require.Error(t, err)
	require.ErrorIs(t, err, postgres.ErrInvalidCursor)
}

// --- ListSessions: filtering, sorting, pagination -----------------------

func TestListSessions_StatusFilterUsesStoredColumn(t *testing.T) {
	st, pool := newStore(t)
	base := time.Now().UTC().Add(-time.Hour)

	active := nextTestSessionID("active")
	abandoned := nextTestSessionID("abandoned")
	seedSession(t, pool, sessionSeed{ID: active, Status: "active", LastEventAt: base})
	seedSession(t, pool, sessionSeed{ID: abandoned, Status: "abandoned", LastEventAt: base.Add(time.Second)})

	got, _, err := st.ListSessions(context.Background(), store.SessionFilter{
		Status: []model.SessionStatus{model.SessionStatusActive},
	}, store.Page{Limit: 10})
	require.NoError(t, err)

	ids := make([]string, len(got))
	for i, s := range got {
		ids[i] = s.ID
	}
	require.Contains(t, ids, active)
	require.NotContains(t, ids, abandoned)
	for _, s := range got {
		require.Equal(t, model.SessionStatusActive, s.Status)
	}
}

func TestListSessions_25SeededSessions_PaginatesInPagesOf7_ZeroDuplicatesZeroOmissions_MidPaginationInsert(t *testing.T) {
	st, pool := newStore(t)
	base := time.Now().UTC().Add(-time.Hour)

	const total = 25
	expected := make(map[string]struct{}, total)
	for i := 0; i < total; i++ {
		id := nextTestSessionID("page")
		seedSession(t, pool, sessionSeed{
			ID:          id,
			LastEventAt: base.Add(time.Duration(i) * time.Second),
			EventCount:  int64(i),
		})
		expected[id] = struct{}{}
	}

	seen := map[string]int{}
	var cursor store.Cursor
	pagesFetched := 0
	insertedMidway := false

	for {
		page, next, err := st.ListSessions(context.Background(), store.SessionFilter{}, store.Page{
			Limit:  7,
			Cursor: cursor,
		})
		require.NoError(t, err)
		pagesFetched++

		for _, s := range page {
			seen[s.ID]++
		}

		// Insert one more session mid-pagination (after the 2nd page), at a
		// sort position already paged past — the keyset guarantee under
		// test: rows fetched before the insert must not be duplicated or
		// omitted by it (SPEC's mid-pagination-insert AC).
		if pagesFetched == 2 && !insertedMidway {
			midID := nextTestSessionID("mid-insert")
			seedSession(t, pool, sessionSeed{
				ID:          midID,
				LastEventAt: base.Add(-time.Minute), // sorts after everything already seeded
			})
			insertedMidway = true
		}

		if next == "" {
			break
		}
		cursor = next
		require.Less(t, pagesFetched, 20, "pagination did not terminate")
	}

	require.True(t, insertedMidway)
	require.LessOrEqual(t, pagesFetched, 4, "25 rows at 7/page should take at most 4 pages")

	for id := range expected {
		require.Equalf(t, 1, seen[id], "session %s seen %d times, want exactly 1", id, seen[id])
	}
	for id, n := range seen {
		require.LessOrEqualf(t, n, 1, "session %s duplicated across pages", id)
	}
}

func TestListSessions_SortKeys_KeysetOrderMatchesFullOrder(t *testing.T) {
	st, pool := newStore(t)
	base := time.Now().UTC().Add(-time.Hour)

	type seeded struct {
		id         string
		lastEvent  time.Time
		startedAt  *time.Time
		costUSD    float64
		eventCount int64
	}
	var rows []seeded
	for i := 0; i < 12; i++ {
		id := nextTestSessionID("sort")
		var startedAt *time.Time
		if i%4 != 0 { // every 4th session has NULL started_at
			v := base.Add(time.Duration(i) * time.Minute)
			startedAt = &v
		}
		r := seeded{
			id:         id,
			lastEvent:  base.Add(time.Duration(i) * time.Second),
			startedAt:  startedAt,
			costUSD:    float64(i) * 1.5,
			eventCount: int64(i * 10),
		}
		rows = append(rows, r)
		seedSession(t, pool, sessionSeed{
			ID: id, LastEventAt: r.lastEvent, StartedAt: r.startedAt,
			CostUSD: r.costUSD, EventCount: r.eventCount,
		})
	}

	tests := []struct {
		name string
		sort store.SessionSort
	}{
		{"last_event_at", store.SessionSortLastEventAt},
		{"started_at", store.SessionSortStartedAt},
		{"cost_usd", store.SessionSortCostUSD},
		{"event_count", store.SessionSortEventCount},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotIDs []string
			var cursor store.Cursor
			for {
				page, next, err := st.ListSessions(context.Background(), store.SessionFilter{Sort: tt.sort}, store.Page{Limit: 3, Cursor: cursor})
				require.NoError(t, err)
				for _, s := range page {
					gotIDs = append(gotIDs, s.ID)
				}
				if next == "" {
					break
				}
				cursor = next
			}

			byID := map[string]seeded{}
			for _, r := range rows {
				byID[r.id] = r
			}
			for i := 1; i < len(gotIDs); i++ {
				a, b := byID[gotIDs[i-1]], byID[gotIDs[i]]
				var ok bool
				switch tt.sort {
				case store.SessionSortLastEventAt:
					ok = !a.lastEvent.Before(b.lastEvent)
				case store.SessionSortCostUSD:
					ok = a.costUSD >= b.costUSD
				case store.SessionSortEventCount:
					ok = a.eventCount >= b.eventCount
				case store.SessionSortStartedAt:
					ok = startedAtDescOK(a.startedAt, b.startedAt)
				}
				require.Truef(t, ok, "sort %s: %s (%v) should be >= %s (%v)", tt.sort, gotIDs[i-1], a, gotIDs[i], b)
			}
		})
	}
}

// startedAtDescOK checks DESC NULLS LAST ordering between two consecutive
// rows: a real value must come before any NULL, and among real values the
// earlier row's value must be >= the later row's.
func startedAtDescOK(a, b *time.Time) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil {
		return false // a NULL must never precede a non-NULL under NULLS LAST
	}
	if b == nil {
		return true // non-NULL correctly precedes NULL
	}
	return !a.Before(*b)
}

// TestListSessions_UsesMatchingIndex verifies SPEC §2.5's AC ("EXPLAIN for
// each of the 4 sorts uses the matching sessions_* index") through
// pg_stat_user_indexes rather than parsing EXPLAIN text: idx_scan on the
// expected index must increase after a real ListSessions call for that
// sort, which proves actual execution used the index — strictly stronger
// evidence than a planner's stated intent, and available without exposing
// read_sessions.go's private query builder to this external test package
// (see file doc for why this file cannot be package postgres).
// pg_stat_force_next_flush (PG 14+) makes this backend's counters visible
// immediately instead of waiting for the periodic stats flush.
func TestListSessions_UsesMatchingIndex(t *testing.T) {
	st, pool := newStore(t)
	base := time.Now().UTC().Add(-24 * time.Hour)

	const n = 400
	for i := 0; i < n; i++ {
		id := nextTestSessionID("explain")
		var startedAt *time.Time
		if i%3 != 0 {
			v := base.Add(time.Duration(i) * time.Second)
			startedAt = &v
		}
		seedSession(t, pool, sessionSeed{
			ID: id, LastEventAt: base.Add(time.Duration(i) * time.Second),
			StartedAt: startedAt, CostUSD: float64(i % 97), EventCount: int64(i % 251),
		})
	}
	_, err := pool.Exec(context.Background(), "ANALYZE sessions")
	require.NoError(t, err)

	tests := []struct {
		sort  store.SessionSort
		index string
	}{
		{store.SessionSortLastEventAt, "sessions_last_event_idx"},
		{store.SessionSortStartedAt, "sessions_started_idx"},
		{store.SessionSortCostUSD, "sessions_cost_idx"},
		{store.SessionSortEventCount, "sessions_events_idx"},
	}

	for _, tt := range tests {
		t.Run(string(tt.sort), func(t *testing.T) {
			before := indexScanCount(t, pool, tt.index)

			_, _, err := st.ListSessions(context.Background(), store.SessionFilter{Sort: tt.sort}, store.Page{Limit: 10})
			require.NoError(t, err)
			_, err = pool.Exec(context.Background(), "SELECT pg_stat_force_next_flush()")
			require.NoError(t, err)

			after := indexScanCount(t, pool, tt.index)
			require.Greaterf(t, after, before, "expected %s's idx_scan to increase after ListSessions(sort=%s)", tt.index, tt.sort)
		})
	}
}

// indexScanCount reads pg_stat_user_indexes for indexName, scoped to
// current_schema(): storetesting.NewPool gives each test its own migrated
// schema, so every test's "sessions_last_event_idx" is a DISTINCT catalog
// object sharing that same relname across schemas — without the schemaname
// filter, QueryRow's single-row result would come back from an arbitrary
// one of them, not necessarily this test's own.
func indexScanCount(t *testing.T, pool *pgxpool.Pool, indexName string) int64 {
	t.Helper()
	var n int64
	err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(idx_scan, 0) FROM pg_stat_user_indexes WHERE schemaname = current_schema() AND indexrelname = $1`, indexName,
	).Scan(&n)
	require.NoError(t, err)
	return n
}

// --- GetSession ----------------------------------------------------------

func TestGetSession_NotFound(t *testing.T) {
	st, _ := newStore(t)
	_, err := st.GetSession(context.Background(), "does-not-exist")
	require.ErrorIs(t, err, postgres.ErrSessionNotFound)
}

func TestGetSession_DetailBlocks(t *testing.T) {
	st, pool := newStore(t)
	ensureRange(t, st, time.Now().Add(-48*time.Hour), time.Now().Add(48*time.Hour))

	sessionID := nextTestSessionID("detail")
	firstSeen := time.Now().UTC().Add(-time.Hour)
	seedSession(t, pool, sessionSeed{
		ID: sessionID, FirstSeenAt: firstSeen, LastEventAt: firstSeen.Add(30 * time.Minute),
		StartedAt:         &firstSeen,
		CostUSD:           4.0,
		CostEstimatedUSD:  0,
		CostByQuerySource: map[string]float64{"": 3.0, "sdk": 1.0},
		Models:            []string{"claude-opus-5"},
	})

	// top_tools + decision_summary fixtures: 3 Edit calls (2 accept exact, 1
	// reject heuristic), 1 Read call (accept exact, NULL duration).
	seedToolCall(t, pool, toolCallSeed{
		ID: nextID(), SessionID: sessionID, ToolName: "Edit",
		Decision: ptrString("accept"), DecisionSource: ptrString("config"),
		Correlation: string(model.CorrelationExact), StartedAt: firstSeen, DurationMS: intPtr(100),
	})
	seedToolCall(t, pool, toolCallSeed{
		ID: nextID(), SessionID: sessionID, ToolName: "Edit",
		Decision: ptrString("accept"), DecisionSource: ptrString("config"),
		Correlation: string(model.CorrelationExact), StartedAt: firstSeen, DurationMS: intPtr(200),
	})
	seedToolCall(t, pool, toolCallSeed{
		ID: nextID(), SessionID: sessionID, ToolName: "Edit",
		Decision: ptrString("reject"), DecisionSource: ptrString("user_reject"),
		Correlation: string(model.CorrelationHeuristic), StartedAt: firstSeen, DurationMS: intPtr(300),
	})
	seedToolCall(t, pool, toolCallSeed{
		ID: nextID(), SessionID: sessionID, ToolName: "Read",
		Decision: ptrString("accept"), DecisionSource: ptrString("config"),
		Correlation: string(model.CorrelationExact), StartedAt: firstSeen, DurationMS: nil,
	})

	// permission_mode_changed event.
	seedEvent(t, pool, sessionID, "permission.mode_changed", firstSeen.Add(time.Minute), "dedup-perm-1",
		ptrString("acceptEdits"), nil, map[string]any{"from_mode": "default", "trigger": "user"})

	// hook.execution_end events: 2 PostToolUse at 10ms/20ms.
	seedEvent(t, pool, sessionID, "hook.execution_end", firstSeen.Add(2*time.Minute), "dedup-hook-1",
		nil, intPtr(10), map[string]any{"hook_event": "PostToolUse"})
	seedEvent(t, pool, sessionID, "hook.execution_end", firstSeen.Add(3*time.Minute), "dedup-hook-2",
		nil, intPtr(20), map[string]any{"hook_event": "PostToolUse"})

	detail, err := st.GetSession(context.Background(), sessionID)
	require.NoError(t, err)

	require.Equal(t, sessionID, detail.ID)
	require.False(t, detail.Partial)
	// Postgres timestamptz has microsecond precision; firstSeen (time.Now())
	// has nanosecond precision, so compare with a small tolerance rather
	// than exact equality.
	require.WithinDuration(t, firstSeen, detail.FirstSeenAt, time.Microsecond)

	require.Len(t, detail.PermissionModeHistory, 1)
	require.Equal(t, "default", detail.PermissionModeHistory[0].From)
	require.Equal(t, "acceptEdits", detail.PermissionModeHistory[0].To)
	require.Equal(t, "user", detail.PermissionModeHistory[0].Trigger)

	require.Len(t, detail.TopTools, 2)
	require.Equal(t, "Edit", detail.TopTools[0].ToolName)
	require.Equal(t, 3, detail.TopTools[0].Calls)
	require.Equal(t, 1, detail.TopTools[0].Rejects)
	require.NotNil(t, detail.TopTools[0].P50MS)
	require.Equal(t, "Read", detail.TopTools[1].ToolName)
	require.Nil(t, detail.TopTools[1].P50MS, "Read's only call has a NULL duration_ms")

	require.Equal(t, 3, detail.DecisionSummary.Accept)
	require.Equal(t, 1, detail.DecisionSummary.Reject)
	require.Equal(t, 3, detail.DecisionSummary.BySource["config"])
	require.Equal(t, 1, detail.DecisionSummary.BySource["user_reject"])
	require.InDelta(t, 0.75, detail.DecisionSummary.ExactShare, 0.0001, "3 of 4 decisions are non-heuristic")

	require.NotNil(t, detail.HookLatency)
	require.Equal(t, int64(15), detail.HookLatency.P50MS)
	// by_hook_event is the p50 latency per hook event, not the execution
	// count: the block is named hook_latency, its siblings are p50_ms/p95_ms,
	// and SPEC §4.3's example pairs p50_ms with an identical by_hook_event
	// value for a session whose only hook event is PostToolUse. Both
	// executions here are PostToolUse (10ms, 20ms), so its p50 is the
	// overall p50 — 15, not the count 2.
	require.Equal(t, int64(15), detail.HookLatency.ByHookEvent["PostToolUse"])

	require.False(t, detail.RawEventsExpired)
}

func TestGetSession_HookLatencyNilWithoutHookCoverage(t *testing.T) {
	st, pool := newStore(t)
	sessionID := nextTestSessionID("nohooks")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	detail, err := st.GetSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.Nil(t, detail.HookLatency, "no hook.execution_end events at all must render hook_latency as null, not a zero struct")
}

func TestGetSession_RawEventsExpired(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()

	// Ensure a partition exists starting at the current month only (not
	// backward), then seed a session whose first_seen_at predates it.
	now := time.Now().UTC()
	require.NoError(t, st.EnsurePartitions(ctx, now, now))

	oldSessionID := nextTestSessionID("old")
	oldFirstSeen := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -6, 0)
	seedSession(t, pool, sessionSeed{ID: oldSessionID, FirstSeenAt: oldFirstSeen, LastEventAt: oldFirstSeen})

	newSessionID := nextTestSessionID("new")
	seedSession(t, pool, sessionSeed{ID: newSessionID, FirstSeenAt: now, LastEventAt: now})

	oldDetail, err := st.GetSession(ctx, oldSessionID)
	require.NoError(t, err)
	require.True(t, oldDetail.RawEventsExpired, "session predating the oldest partition must report raw_events_expired=true")

	newDetail, err := st.GetSession(ctx, newSessionID)
	require.NoError(t, err)
	require.False(t, newDetail.RawEventsExpired)
}

// --- ListTurns -------------------------------------------------------------

func TestListTurns_OrderedBySessionStartedAt(t *testing.T) {
	st, pool := newStore(t)
	sessionID := nextTestSessionID("turns")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	base := time.Now().UTC().Add(-time.Hour)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO turns (session_id, prompt_id, turn_index, started_at, first_seen_at, last_event_at, status)
		VALUES ($1,'p2',2,$2,$2,$2,'complete'), ($1,'p1',1,$3,$3,$3,'complete'), ($1,'p3',3,NULL,$4,$4,'open')`,
		sessionID, base.Add(2*time.Minute), base.Add(time.Minute), base.Add(3*time.Minute),
	)
	require.NoError(t, err)

	turns, err := st.ListTurns(context.Background(), sessionID)
	require.NoError(t, err)
	require.Len(t, turns, 3)
	require.Equal(t, "p1", turns[0].PromptID)
	require.Equal(t, "p2", turns[1].PromptID)
	require.Equal(t, "p3", turns[2].PromptID, "NULL started_at sorts last")
}
