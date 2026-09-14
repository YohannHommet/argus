// Package postgres implements store.Reader's ListSessions, GetSession, and ListTurns (SPEC §3.3, §4.3, P3-02).
// Cursor codec mirrors httpapi's format (SPEC §4.1) independently due to depguard constraint (SPEC §3.1).
package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
	"github.com/YohannHommet/argus/server/internal/store/postgres/gen"
)

const (
	// defaultSessionLimit / maxSessionLimit mirror SPEC §4.1's pagination defaults.
	defaultSessionLimit = 50
	maxSessionLimit     = 500

	// defaultTopToolsLimit caps SessionDetail.TopTools (bounded response for long tails).
	defaultTopToolsLimit = 10
)

// sessionSortColumns maps SessionSort to sessions columns and whitelists values.
var sessionSortColumns = map[store.SessionSort]string{
	store.SessionSortLastEventAt: "last_event_at",
	store.SessionSortStartedAt:   "started_at",
	store.SessionSortCostUSD:     "cost_usd",
	store.SessionSortEventCount:  "event_count",
}

// ErrInvalidCursor is an alias for store.ErrInvalidCursor (D-22 pattern: seam-level sentinel).
var ErrInvalidCursor = store.ErrInvalidCursor

// ErrSessionNotFound is an alias for store.ErrSessionNotFound (D-22 pattern: seam-level sentinel).
var ErrSessionNotFound = store.ErrSessionNotFound

// sessionCursorPayload is the SPEC §4.1 wire shape: K is sort key, V is [sortValue, id tiebreak].
type sessionCursorPayload struct {
	K string            `json:"k"`
	V []json.RawMessage `json:"v"`
}

// sessionCursorEncoding is URL-safe, unpadded base64 (SPEC §4.1).
var sessionCursorEncoding = base64.RawURLEncoding

// encodeSessionCursor encodes the next page's cursor (sortKey, sortValue, id).
func encodeSessionCursor(sortKey store.SessionSort, sortValue any, id string) (store.Cursor, error) {
	vJSON, err := json.Marshal(sortValue)
	if err != nil {
		return "", fmt.Errorf("postgres: encode session cursor: marshal sort value: %w", err)
	}
	idJSON, err := json.Marshal(id)
	if err != nil {
		return "", fmt.Errorf("postgres: encode session cursor: marshal id: %w", err)
	}
	body, err := json.Marshal(sessionCursorPayload{K: string(sortKey), V: []json.RawMessage{vJSON, idJSON}})
	if err != nil {
		return "", fmt.Errorf("postgres: encode session cursor: %w", err)
	}
	return store.Cursor(sessionCursorEncoding.EncodeToString(body)), nil
}

// decodeSessionCursor parses and validates a cursor, rejecting if minted under a different sort key.
func decodeSessionCursor(c store.Cursor, sortKey store.SessionSort) (sortValueRaw json.RawMessage, id string, err error) {
	raw, err := sessionCursorEncoding.DecodeString(string(c))
	if err != nil {
		return nil, "", fmt.Errorf("%w: not valid base64: %w", ErrInvalidCursor, err)
	}
	var payload sessionCursorPayload
	if jsonErr := json.Unmarshal(raw, &payload); jsonErr != nil {
		return nil, "", fmt.Errorf("%w: not valid JSON: %w", ErrInvalidCursor, jsonErr)
	}
	if payload.K == "" || len(payload.V) != 2 {
		return nil, "", fmt.Errorf("%w: missing key or malformed values", ErrInvalidCursor)
	}
	if payload.K != string(sortKey) {
		return nil, "", fmt.Errorf("%w: minted for sort %q, replayed against %q", ErrInvalidCursor, payload.K, sortKey)
	}
	if jsonErr := json.Unmarshal(payload.V[1], &id); jsonErr != nil {
		return nil, "", fmt.Errorf("%w: invalid id value: %w", ErrInvalidCursor, jsonErr)
	}
	return payload.V[0], id, nil
}

// decodeSessionSortValue converts a cursor's raw JSON sort value to Go type for sortKey's column.
func decodeSessionSortValue(sortKey store.SessionSort, raw json.RawMessage) (any, error) {
	switch sortKey {
	case store.SessionSortLastEventAt, store.SessionSortStartedAt:
		if string(raw) == "null" {
			if sortKey == store.SessionSortStartedAt {
				return nil, nil
			}
			return nil, fmt.Errorf("%w: last_event_at cannot be null", ErrInvalidCursor)
		}
		var t time.Time
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("%w: invalid %s value: %w", ErrInvalidCursor, sortKey, err)
		}
		return t, nil
	case store.SessionSortCostUSD:
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("%w: invalid cost_usd value: %w", ErrInvalidCursor, err)
		}
		return f, nil
	case store.SessionSortEventCount:
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, fmt.Errorf("%w: invalid event_count value: %w", ErrInvalidCursor, err)
		}
		return n, nil
	default:
		return nil, fmt.Errorf("postgres: list sessions: unknown sort %q", sortKey)
	}
}

// sessionKeysetPredicate renders the keyset predicate for pagination.
// DESC + id DESC tiebreak: continuing means strictly smaller sortValue, or equal with smaller id.
// started_at is nullable (NULLS LAST); past non-NULL must include all NULLs, past NULL uses only id tiebreak.
func sessionKeysetPredicate(b *clauseBuilder, sortKey store.SessionSort, column string, sortValue any, id string) string {
	idPH := b.placeholder(id)
	if sortKey == store.SessionSortStartedAt && sortValue == nil {
		return fmt.Sprintf("(%s IS NULL AND s.id < %s)", column, idPH)
	}
	vPH := b.placeholder(sortValue)
	if sortKey == store.SessionSortStartedAt {
		return fmt.Sprintf("(%s < %s OR (%s = %s AND s.id < %s) OR %s IS NULL)", column, vPH, column, vPH, idPH, column)
	}
	return fmt.Sprintf("(%s < %s OR (%s = %s AND s.id < %s))", column, vPH, column, vPH, idPH)
}

// sessionRowData is the Go-typed shape both ListSessions and GetSession convert to before toSummary.
type sessionRowData struct {
	ID, Vendor, Project, CWD, Status, StartType string
	StartedAt, EndedAt                          *time.Time
	LastEventAt                                 time.Time
	TurnCount, ToolCallCount, ToolRejectCount   int32
	SubagentCount, ErrorCount                   int32
	EventCount                                  int64
	InputTokens, OutputTokens                   int64
	CacheReadTokens, CacheCreateTokens          int64
	CostUSD, CostEstimatedUSD                   float64
	CostByQuerySource                           []byte
	Models                                      []string
	AppVersion, Entrypoint, TerminalType        string
}

// sessionListColumns is the column list both ListSessions and toSummary agree on.
// Vendor strings are COALESCEd to ” (model uses plain strings, not pointers).
const sessionListColumns = `id, vendor, COALESCE(project, ''), COALESCE(cwd, ''), status, COALESCE(start_type, ''),
	started_at, ended_at, last_event_at,
	turn_count, event_count, tool_call_count, tool_reject_count, subagent_count, error_count,
	input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
	cost_usd, cost_estimated_usd, cost_by_query_source, models,
	COALESCE(app_version, ''), COALESCE(entrypoint, ''), COALESCE(terminal_type, '')`

// toSummary builds SessionSummary from sessionRowData, computing derived fields (duration_ms, partial).
func (d sessionRowData) toSummary() (model.SessionSummary, error) {
	cost, err := buildSessionCost(d.CostUSD, d.CostEstimatedUSD, d.CostByQuerySource)
	if err != nil {
		return model.SessionSummary{}, err
	}
	return model.SessionSummary{
		ID:              d.ID,
		Vendor:          d.Vendor,
		Project:         d.Project,
		CWD:             d.CWD,
		Status:          model.SessionStatus(d.Status),
		StartType:       d.StartType,
		StartedAt:       d.StartedAt,
		EndedAt:         d.EndedAt,
		LastEventAt:     d.LastEventAt,
		DurationMS:      sessionDurationMS(d.StartedAt, d.EndedAt, d.LastEventAt),
		TurnCount:       int(d.TurnCount),
		EventCount:      d.EventCount,
		ToolCallCount:   int(d.ToolCallCount),
		ToolRejectCount: int(d.ToolRejectCount),
		SubagentCount:   int(d.SubagentCount),
		ErrorCount:      int(d.ErrorCount),
		Tokens: model.TokenUsage{
			Input:         d.InputTokens,
			Output:        d.OutputTokens,
			CacheRead:     d.CacheReadTokens,
			CacheCreation: d.CacheCreateTokens,
		},
		Cost:         cost,
		Models:       d.Models,
		Partial:      d.StartedAt == nil,
		AppVersion:   d.AppVersion,
		Entrypoint:   d.Entrypoint,
		TerminalType: d.TerminalType,
	}, nil
}

// sortValue extracts the value for sortKey from an already-scanned row.
func (d sessionRowData) sortValue(sortKey store.SessionSort) any {
	switch sortKey {
	case store.SessionSortLastEventAt:
		return d.LastEventAt
	case store.SessionSortStartedAt:
		if d.StartedAt == nil {
			return nil
		}
		return *d.StartedAt
	case store.SessionSortCostUSD:
		return d.CostUSD
	case store.SessionSortEventCount:
		return d.EventCount
	default:
		return nil
	}
}

// sessionDurationMS computes duration_ms: nil until started_at, else gap to ended_at or last_event_at.
func sessionDurationMS(startedAt, endedAt *time.Time, lastEventAt time.Time) *int64 {
	if startedAt == nil {
		return nil
	}
	end := lastEventAt
	if endedAt != nil {
		end = *endedAt
	}
	ms := end.Sub(*startedAt).Milliseconds()
	return &ms
}

// buildSessionCost assembles SessionCost from reported/estimated totals and cost_by_query_source jsonb.
// estimatedShare is 0 (not NaN) when total is 0 to avoid divide-by-zero.
func buildSessionCost(reportedUSD, estimatedUSD float64, costByQuerySourceJSON []byte) (model.SessionCost, error) {
	byQuerySource := map[string]float64{}
	if len(costByQuerySourceJSON) > 0 {
		if err := json.Unmarshal(costByQuerySourceJSON, &byQuerySource); err != nil {
			return model.SessionCost{}, fmt.Errorf("postgres: decode cost_by_query_source: %w", err)
		}
	}
	dominant, other := dominantQuerySource(byQuerySource)

	total := reportedUSD + estimatedUSD
	estimatedShare := 0.0
	if total != 0 {
		estimatedShare = estimatedUSD / total
	}

	return model.SessionCost{
		USD:                 total,
		ReportedUSD:         reportedUSD,
		EstimatedUSD:        estimatedUSD,
		EstimatedShare:      estimatedShare,
		ByQuerySource:       byQuerySource,
		DominantQuerySource: dominant,
		OtherQuerySourceUSD: other,
	}, nil
}

// dominantQuerySource picks the highest-cost key, tie-breaking deterministically on key order.
func dominantQuerySource(byQuerySource map[string]float64) (dominant string, otherUSD float64) {
	total := 0.0
	dominantCost := -1.0
	keys := make([]string, 0, len(byQuerySource))
	for k := range byQuerySource {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := byQuerySource[k]
		total += v
		if v > dominantCost {
			dominant, dominantCost = k, v
		}
	}
	if dominantCost < 0 {
		return "", 0
	}
	return dominant, total - dominantCost
}

// listSessionsQuery is the full SQL + args from buildListSessionsQuery (named type for code sharing).
type listSessionsQuery struct {
	SQL     string
	Args    []any
	SortKey store.SessionSort
	Limit   int
}

// buildListSessionsQuery renders the full dynamic SQL with WHERE, keyset predicate, ORDER BY, LIMIT.
// Factored out so tests can run EXPLAIN against the exact query ListSessions executes.
func buildListSessionsQuery(f store.SessionFilter, p store.Page) (listSessionsQuery, error) {
	sortKey := f.Sort
	if sortKey == "" {
		sortKey = store.SessionSortLastEventAt
	}
	column, ok := sessionSortColumns[sortKey]
	if !ok {
		return listSessionsQuery{}, fmt.Errorf("postgres: list sessions: unknown sort %q", sortKey)
	}

	limit := p.Limit
	if limit <= 0 {
		limit = defaultSessionLimit
	}
	if limit > maxSessionLimit {
		limit = maxSessionLimit
	}

	b := newClauseBuilder()
	var clauses []string
	if where := sessionWhereClause(b, f); where != "" {
		clauses = append(clauses, where)
	}
	if p.Cursor != "" {
		rawValue, id, err := decodeSessionCursor(p.Cursor, sortKey)
		if err != nil {
			return listSessionsQuery{}, err
		}
		sortValue, err := decodeSessionSortValue(sortKey, rawValue)
		if err != nil {
			return listSessionsQuery{}, err
		}
		clauses = append(clauses, sessionKeysetPredicate(b, sortKey, column, sortValue, id))
	}

	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	nullsClause := ""
	if sortKey == store.SessionSortStartedAt {
		nullsClause = " NULLS LAST"
	}
	limitPH := b.placeholder(int32(limit + 1))

	sql := fmt.Sprintf(`
		SELECT %s
		FROM sessions s
		%s
		ORDER BY %s DESC%s, s.id DESC
		LIMIT %s`, sessionListColumns, where, column, nullsClause, limitPH)

	return listSessionsQuery{SQL: sql, Args: b.args, SortKey: sortKey, Limit: limit}, nil
}

// ListSessions implements store.Reader: filtered, keyset-paginated, one of four sort keys.
// Fetches limit+1 rows to detect has_more without a second COUNT.
func (s *Store) ListSessions(ctx context.Context, f store.SessionFilter, p store.Page) ([]model.SessionSummary, store.Cursor, error) {
	q, err := buildListSessionsQuery(f, p)
	if err != nil {
		return nil, "", err
	}
	sortKey, limit := q.SortKey, q.Limit

	rows, err := s.pool.Query(ctx, q.SQL, q.Args...)
	if err != nil {
		return nil, "", fmt.Errorf("postgres: list sessions: %w", err)
	}
	defer rows.Close()

	var results []sessionRowData
	for rows.Next() {
		var d sessionRowData
		if scanErr := rows.Scan(
			&d.ID, &d.Vendor, &d.Project, &d.CWD, &d.Status, &d.StartType,
			&d.StartedAt, &d.EndedAt, &d.LastEventAt,
			&d.TurnCount, &d.EventCount, &d.ToolCallCount, &d.ToolRejectCount, &d.SubagentCount, &d.ErrorCount,
			&d.InputTokens, &d.OutputTokens, &d.CacheReadTokens, &d.CacheCreateTokens,
			&d.CostUSD, &d.CostEstimatedUSD, &d.CostByQuerySource, &d.Models,
			&d.AppVersion, &d.Entrypoint, &d.TerminalType,
		); scanErr != nil {
			return nil, "", fmt.Errorf("postgres: list sessions: scan: %w", scanErr)
		}
		results = append(results, d)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, "", fmt.Errorf("postgres: list sessions: %w", rowsErr)
	}

	hasMore := len(results) > limit
	if hasMore {
		results = results[:limit]
	}

	summaries := make([]model.SessionSummary, len(results))
	for i, d := range results {
		sum, sumErr := d.toSummary()
		if sumErr != nil {
			return nil, "", sumErr
		}
		summaries[i] = sum
	}

	var nextCursor store.Cursor
	if hasMore && len(results) > 0 {
		last := results[len(results)-1]
		var cursorErr error
		nextCursor, cursorErr = encodeSessionCursor(sortKey, last.sortValue(sortKey), last.ID)
		if cursorErr != nil {
			return nil, "", cursorErr
		}
	}

	return summaries, nextCursor, nil
}

// GetSession implements store.Reader: session summary plus SessionDetail blocks.
// Each block is its own query to avoid row multiplication across unrelated one-to-many relationships.
func (s *Store) GetSession(ctx context.Context, id string) (*model.SessionDetail, error) {
	q := gen.New(s.pool)

	row, err := q.GetSessionRow(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("postgres: get session: %w", err)
	}

	costUSD, err := row.CostUsd.Float64Value()
	if err != nil {
		return nil, fmt.Errorf("postgres: get session: decode cost_usd: %w", err)
	}
	costEstUSD, err := row.CostEstimatedUsd.Float64Value()
	if err != nil {
		return nil, fmt.Errorf("postgres: get session: decode cost_estimated_usd: %w", err)
	}

	d := sessionRowData{
		ID:                id,
		Vendor:            row.Vendor,
		Project:           textOrEmpty(row.Project),
		CWD:               textOrEmpty(row.Cwd),
		Status:            row.Status,
		StartType:         textOrEmpty(row.StartType),
		StartedAt:         timestamptzOrNil(row.StartedAt),
		EndedAt:           timestamptzOrNil(row.EndedAt),
		LastEventAt:       row.LastEventAt.Time,
		TurnCount:         row.TurnCount,
		ToolCallCount:     row.ToolCallCount,
		ToolRejectCount:   row.ToolRejectCount,
		SubagentCount:     row.SubagentCount,
		ErrorCount:        row.ErrorCount,
		EventCount:        row.EventCount,
		InputTokens:       row.InputTokens,
		OutputTokens:      row.OutputTokens,
		CacheReadTokens:   row.CacheReadTokens,
		CacheCreateTokens: row.CacheCreationTokens,
		CostUSD:           costUSD.Float64,
		CostEstimatedUSD:  costEstUSD.Float64,
		CostByQuerySource: row.CostByQuerySource,
		Models:            row.Models,
		AppVersion:        textOrEmpty(row.AppVersion),
		Entrypoint:        textOrEmpty(row.Entrypoint),
		TerminalType:      textOrEmpty(row.TerminalType),
	}
	summary, err := d.toSummary()
	if err != nil {
		return nil, fmt.Errorf("postgres: get session: %w", err)
	}

	permHistoryRows, err := q.SessionPermissionModeHistory(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("postgres: get session: permission mode history: %w", err)
	}
	permHistory := make([]model.PermissionModeChange, len(permHistoryRows))
	for i, r := range permHistoryRows {
		permHistory[i] = model.PermissionModeChange{
			TS:      r.Ts.Time,
			From:    anyToString(r.FromMode),
			To:      r.ToMode,
			Trigger: anyToString(r.Trigger),
		}
	}

	topTools, err := sessionTopTools(ctx, s.pool, id, defaultTopToolsLimit)
	if err != nil {
		return nil, fmt.Errorf("postgres: get session: top tools: %w", err)
	}

	decisionTotals, err := q.SessionDecisionTotals(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("postgres: get session: decision totals: %w", err)
	}
	decisionBySourceRows, err := q.SessionDecisionBySource(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("postgres: get session: decision by source: %w", err)
	}
	bySource := make(map[string]int, len(decisionBySourceRows))
	for _, r := range decisionBySourceRows {
		bySource[r.DecisionSource] = int(r.N)
	}
	exactShare := 1.0 // vacuously exact when there is nothing decided yet (see read_sessions.sql)
	if decisionTotals.Decided > 0 {
		exactShare = float64(decisionTotals.ExactDecided) / float64(decisionTotals.Decided)
	}
	decisionSummary := model.SessionDecisionSummary{
		Accept:     int(decisionTotals.Accept),
		Reject:     int(decisionTotals.Reject),
		BySource:   bySource,
		ExactShare: exactShare,
	}

	sourcesSeenRaw, err := q.SessionSourcesSeen(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("postgres: get session: sources seen: %w", err)
	}
	sourcesSeen := make([]model.Source, len(sourcesSeenRaw))
	for i, v := range sourcesSeenRaw {
		sourcesSeen[i] = model.Source(v)
	}

	hookLatency, err := sessionHookLatency(ctx, s.pool, id)
	if err != nil {
		return nil, fmt.Errorf("postgres: get session: hook latency: %w", err)
	}

	rawEventsExpired, err := sessionRawEventsExpired(ctx, s.pool, row.FirstSeenAt.Time)
	if err != nil {
		return nil, fmt.Errorf("postgres: get session: raw events expired: %w", err)
	}

	return &model.SessionDetail{
		SessionSummary:        summary,
		PermissionModeHistory: permHistory,
		TopTools:              topTools,
		DecisionSummary:       decisionSummary,
		SourcesSeen:           sourcesSeen,
		RawEventsExpired:      rawEventsExpired,
		HookLatency:           hookLatency,
		FirstSeenAt:           row.FirstSeenAt.Time,
		User:                  textOrEmpty(row.UserEmail),
		OrganizationID:        textOrEmpty(row.OrganizationID),
	}, nil
}

// SessionSummary returns SessionSummary for one session by ID, reusing ListSessions' projection.
// NOT in store.Reader: only HubPublisher uses it via a narrow consumer-owned SessionReader port.
func (s *Store) SessionSummary(ctx context.Context, id string) (*model.SessionSummary, error) {
	var d sessionRowData
	err := s.pool.QueryRow(ctx, `SELECT `+sessionListColumns+` FROM sessions WHERE id = $1`, id).Scan(
		&d.ID, &d.Vendor, &d.Project, &d.CWD, &d.Status, &d.StartType,
		&d.StartedAt, &d.EndedAt, &d.LastEventAt,
		&d.TurnCount, &d.EventCount, &d.ToolCallCount, &d.ToolRejectCount, &d.SubagentCount, &d.ErrorCount,
		&d.InputTokens, &d.OutputTokens, &d.CacheReadTokens, &d.CacheCreateTokens,
		&d.CostUSD, &d.CostEstimatedUSD, &d.CostByQuerySource, &d.Models,
		&d.AppVersion, &d.Entrypoint, &d.TerminalType,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("postgres: session summary: %w", err)
	}

	summary, err := d.toSummary()
	if err != nil {
		return nil, fmt.Errorf("postgres: session summary: %w", err)
	}
	return &summary, nil
}

// ActiveSessionCount counts sessions with status='active' (NOT in store.Reader; see SessionSummary).
func (s *Store) ActiveSessionCount(ctx context.Context) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE status = 'active'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres: active session count: %w", err)
	}
	return n, nil
}

// ListTurns implements store.Reader: every turn of a session in one page.
func (s *Store) ListTurns(ctx context.Context, sessionID string) ([]model.Turn, error) {
	rows, err := gen.New(s.pool).ListTurnsBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list turns: %w", err)
	}

	turns := make([]model.Turn, len(rows))
	for i, r := range rows {
		costUSD, err := r.CostUsd.Float64Value()
		if err != nil {
			return nil, fmt.Errorf("postgres: list turns: decode cost_usd: %w", err)
		}
		costEstUSD, err := r.CostEstimatedUsd.Float64Value()
		if err != nil {
			return nil, fmt.Errorf("postgres: list turns: decode cost_estimated_usd: %w", err)
		}
		turns[i] = model.Turn{
			SessionID:         r.SessionID,
			PromptID:          r.PromptID,
			TurnIndex:         int4OrNil(r.TurnIndex),
			StartedAt:         timestamptzOrNil(r.StartedAt),
			EndedAt:           timestamptzOrNil(r.EndedAt),
			FirstSeenAt:       r.FirstSeenAt.Time,
			LastEventAt:       r.LastEventAt.Time,
			DurationMS:        int4OrNil(r.DurationMs),
			Status:            model.TurnStatus(r.Status),
			APIRequestCount:   int(r.ApiRequestCount),
			ToolCallCount:     int(r.ToolCallCount),
			ToolRejectCount:   int(r.ToolRejectCount),
			ErrorCount:        int(r.ErrorCount),
			InputTokens:       r.InputTokens,
			OutputTokens:      r.OutputTokens,
			CacheReadTokens:   r.CacheReadTokens,
			CacheCreateTokens: r.CacheCreationTokens,
			CostUSD:           costUSD.Float64,
			CostEstimatedUSD:  costEstUSD.Float64,
			Models:            r.Models,
		}
	}
	return turns, nil
}

// sessionTopTools is hand-written pgx SQL (not sqlc) because sqlc mis-infers percentile_cont as NOT NULL.
func sessionTopTools(ctx context.Context, pool *pgxpool.Pool, sessionID string, limit int) ([]model.ToolUsageSummary, error) {
	rows, err := pool.Query(ctx, `
		SELECT tool_name,
		       count(*)::int AS calls,
		       count(*) FILTER (WHERE decision = 'reject')::int AS rejects,
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms) AS p50_ms
		FROM tool_calls
		WHERE session_id = $1
		GROUP BY tool_name
		ORDER BY calls DESC, tool_name
		LIMIT $2`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: session top tools: %w", err)
	}
	defer rows.Close()

	var out []model.ToolUsageSummary
	for rows.Next() {
		var (
			toolName       string
			calls, rejects int32
			p50Ms          *float64
		)
		if err := rows.Scan(&toolName, &calls, &rejects, &p50Ms); err != nil {
			return nil, fmt.Errorf("postgres: session top tools: scan: %w", err)
		}
		out = append(out, model.ToolUsageSummary{
			ToolName: toolName,
			Calls:    int(calls),
			Rejects:  int(rejects),
			P50MS:    roundToIntPtr(p50Ms),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: session top tools: %w", err)
	}
	return out, nil
}

// sessionHookLatency implements SessionDetail.HookLatency (SPEC §4.3).
// Nil when zero hook.execution_end events; otherwise overall p50/p95 + p50 per hook_event.
// by_hook_event carries latency (not execution counts): panel needs the p50 breakdown.
func sessionHookLatency(ctx context.Context, pool *pgxpool.Pool, sessionID string) (*model.SessionHookLatency, error) {
	var (
		executions int64
		p50, p95   *float64
	)
	err := pool.QueryRow(ctx, `
		SELECT count(*)::bigint AS executions,
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms) AS p50_ms,
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) AS p95_ms
		FROM events
		WHERE session_id = $1 AND kind = 'hook.execution_end'`, sessionID,
	).Scan(&executions, &p50, &p95)
	if err != nil {
		return nil, fmt.Errorf("postgres: session hook latency: %w", err)
	}
	if executions == 0 {
		return nil, nil //nolint:nilnil // absence IS the value here: SPEC §4.3 documents hook_latency as `null` for "no hook coverage", not an empty/zero struct.
	}

	// Hand-written for same percentile_cont NOT-NULL-mis-inference reason.
	// hook_event is read from attrs (not a promoted column); NULL p50 skips the event.
	byEventRows, err := pool.Query(ctx, `
		SELECT COALESCE(attrs->>'hook_event', '') AS hook_event,
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms) AS p50_ms
		FROM events
		WHERE session_id = $1 AND kind = 'hook.execution_end'
		GROUP BY 1
		ORDER BY 1`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: session hook latency by event: %w", err)
	}
	defer byEventRows.Close()

	byEvent := map[string]int64{}
	for byEventRows.Next() {
		var hookEvent string
		var p50ForEvent *float64
		if err := byEventRows.Scan(&hookEvent, &p50ForEvent); err != nil {
			return nil, fmt.Errorf("postgres: scanning session hook latency by event: %w", err)
		}
		if p50ForEvent == nil {
			continue
		}
		byEvent[hookEvent] = int64FromFloatPtr(p50ForEvent)
	}
	if err := byEventRows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: session hook latency by event rows: %w", err)
	}

	return &model.SessionHookLatency{
		P50MS:       int64FromFloatPtr(p50),
		P95MS:       int64FromFloatPtr(p95),
		ByHookEvent: byEvent,
	}, nil
}

// sessionRawEventsExpired reports true when first_seen_at predates the oldest events partition.
func sessionRawEventsExpired(ctx context.Context, pool *pgxpool.Pool, firstSeenAt time.Time) (bool, error) {
	oldest, ok, err := oldestEventsPartitionStart(ctx, pool)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return firstSeenAt.Before(oldest), nil
}

// oldestEventsPartitionStart returns the earliest attached events partition's lower bound.
func oldestEventsPartitionStart(ctx context.Context, pool *pgxpool.Pool) (time.Time, bool, error) {
	rows, err := pool.Query(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE p.relname = 'events' AND n.nspname = current_schema()`)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("postgres: oldest events partition: %w", err)
	}
	defer rows.Close()

	var oldest time.Time
	found := false
	for rows.Next() {
		var relname string
		if err := rows.Scan(&relname); err != nil {
			return time.Time{}, false, fmt.Errorf("postgres: oldest events partition: scan: %w", err)
		}
		m := monthlyPartitionName.FindStringSubmatch(relname)
		if m == nil || m[1] != "events" {
			continue
		}
		year, yerr := strconv.Atoi(m[2])
		month, merr := strconv.Atoi(m[3])
		if yerr != nil || merr != nil {
			continue
		}
		start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		if !found || start.Before(oldest) {
			oldest, found = start, true
		}
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, false, fmt.Errorf("postgres: oldest events partition: %w", err)
	}
	return oldest, found, nil
}

// roundToIntPtr rounds a nullable float64 percentile to *int, preserving nil.
func roundToIntPtr(f *float64) *int {
	if f == nil {
		return nil
	}
	n := int(math.Round(*f))
	return &n
}

// int64FromFloatPtr rounds a nullable float64 percentile to int64 (nil → 0).
func int64FromFloatPtr(f *float64) int64 {
	if f == nil {
		return 0
	}
	return int64(math.Round(*f))
}

// anyToString converts interface{}-scanned jsonb text extraction to string.
func anyToString(v any) string {
	if v == nil {
		return ""
	}
	if str, ok := v.(string); ok {
		return str
	}
	return fmt.Sprint(v)
}

// textOrEmpty converts pgtype.Text to string ("" for SQL NULL).
func textOrEmpty(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

// timestamptzOrNil converts pgtype.Timestamptz to *time.Time (nil for SQL NULL).
func timestamptzOrNil(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// int4OrNil converts pgtype.Int4 to *int (nil for SQL NULL).
func int4OrNil(n pgtype.Int4) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int32)
	return &v
}
