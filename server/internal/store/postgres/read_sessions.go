// Package postgres — read_sessions.go implements store.Reader's ListSessions,
// GetSession, and ListTurns (SPEC §3.3, §4.3, P3-02). ListSessions is one of
// the three hand-built dynamic-filter/dynamic-sort queries SPEC §3.3 carves
// out of sqlc, built from filter.go's whitelist clause builder plus this
// file's own keyset-pagination predicate and cursor codec. GetSession and
// ListTurns are fixed, single-session-parameter reads and go through sqlc
// (db/queries/read_sessions.sql) — except SessionDetail's two
// percentile-based aggregates (top_tools, hook_latency), which are
// hand-written pgx queries here for the reason documented in
// read_sessions.sql: sqlc mis-infers percentile_cont's result column as
// NOT NULL.
//
// Cursor codec note: this file's encode/decodeSessionCursor implement the
// SAME "base64url(json({k,v}))" wire format httpapi/cursor.go documents
// (SPEC §4.1), independently rather than by importing that package —
// depguard forbids internal/store depending on internal/httpapi (SPEC
// §3.1: dependency direction is strictly inward). The duplication is a
// handful of lines on each side of a documented wire contract, not two
// independently-evolving formats.
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
	// defaultSessionLimit / maxSessionLimit mirror SPEC §4.1's pagination
	// defaults ("?limit= (default 50, max 500)"), applied when the caller
	// supplies a non-positive or over-large Page.Limit.
	defaultSessionLimit = 50
	maxSessionLimit     = 500

	// defaultTopToolsLimit caps SessionDetail.TopTools. SPEC §4.3 documents
	// the shape but not a size; capping keeps the detail response bounded
	// for a session with a long tail of one-off tool names, the same
	// "review the top N, not everything" posture the fleet-wide
	// /analytics/breakdown endpoint states explicitly (SPEC §4.3's
	// `limit=20` default there) — 10 is a judgment call for the session
	// scope, not a SPEC citation.
	defaultTopToolsLimit = 10
)

// sessionSortColumns maps store.SessionSort to the sessions column each
// backs (SPEC §2.1's four `sessions_*` sort indexes), and doubles as the
// whitelist that rejects any SessionSort value this package doesn't
// recognize.
var sessionSortColumns = map[store.SessionSort]string{
	store.SessionSortLastEventAt: "last_event_at",
	store.SessionSortStartedAt:   "started_at",
	store.SessionSortCostUSD:     "cost_usd",
	store.SessionSortEventCount:  "event_count",
}

// ErrInvalidCursor is ListSessions' cursor decode failure (SPEC §4.1:
// "opaque, validated, 400 on tamper") — a distinct sentinel from
// httpapi.ErrInvalidCursor because store cannot depend on httpapi (see
// package doc), but wrapping the identical semantics so a future caller can
// map it onto the same problem+json response.
var ErrInvalidCursor = errors.New("postgres: invalid cursor")

// ErrSessionNotFound is GetSession's not-found signal (SPEC §4.3's `GET
// /api/v1/sessions/{id}` 404 response), wrapping pgx.ErrNoRows so callers
// can match on it without importing pgx themselves.
var ErrSessionNotFound = errors.New("postgres: session not found")

// sessionCursorPayload is the wire shape SPEC §4.1 specifies:
// `{"k":"<sort key>","v":[…]}`. V holds exactly two elements for a session
// cursor: the sort column's own value, then the `id` tiebreak (SPEC §2.1:
// every sort index carries `id DESC`).
type sessionCursorPayload struct {
	K string            `json:"k"`
	V []json.RawMessage `json:"v"`
}

// sessionCursorEncoding is URL-safe, unpadded base64 (SPEC §4.1), matching
// httpapi/cursor.go's choice for the identical reason: a cursor travels as
// a query-string value where '=' padding buys nothing.
var sessionCursorEncoding = base64.RawURLEncoding

// encodeSessionCursor renders the next page's cursor for sortKey, given the
// last row's own sort-column value (nil when it is a NULL started_at) and
// id.
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

// decodeSessionCursor parses a cursor minted by encodeSessionCursor,
// enforcing sort-key binding: a cursor minted under one sort is rejected
// when replayed against another (SPEC §4.1; see httpapi/cursor.go's package
// doc for the full "why structural validation is enough" reasoning, which
// applies identically here).
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

// decodeSessionSortValue converts a cursor's raw JSON sort value into the Go
// type sessionKeysetPredicate needs for sortKey's column: time.Time for the
// two timestamp sorts (nil for started_at's NULL case), float64 for
// cost_usd, int64 for event_count.
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

// sessionKeysetPredicate renders the "seek past the last row of the
// previous page" WHERE fragment for one sort key (SPEC §2.1, §4.3): DESC
// order with `id DESC` as the tiebreak, so continuing after (sortValue, id)
// means "a strictly smaller sortValue, or an equal sortValue with a
// strictly smaller id". started_at is the one nullable sort column (SPEC
// §2.1: `sessions_started_idx ... DESC NULLS LAST`) — NULLS LAST means NULL
// rows sort after every real value, so continuing past a non-NULL sortValue
// must also include every NULL row (they are all still "further down" the
// page), while continuing past a NULL sortValue (the previous page's last
// row itself had a NULL started_at) only needs the id tiebreak among the
// remaining NULL rows.
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

// sessionRowData is the plain-Go-typed shape both ListSessions' hand-rolled
// scan and GetSession's sqlc-generated row convert into before building the
// wire model.SessionSummary — one shared conversion (toSummary) instead of
// two copies of the duration/partial/cost derivation logic.
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

// sessionListColumns is the exact column list (and order) both the
// ListSessions hand-rolled query and toSummary's scan destinations agree
// on. Nullable vendor-string columns are COALESCEd to ” at the SQL layer
// because model.SessionSummary's corresponding fields (Project, CWD,
// StartType, AppVersion, Entrypoint, TerminalType) are plain strings, not
// pointers (SPEC §4.3's example never renders these as null) — only
// StartedAt/EndedAt stay nullable, since SessionSummary types those as
// *time.Time.
const sessionListColumns = `id, vendor, COALESCE(project, ''), COALESCE(cwd, ''), status, COALESCE(start_type, ''),
	started_at, ended_at, last_event_at,
	turn_count, event_count, tool_call_count, tool_reject_count, subagent_count, error_count,
	input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
	cost_usd, cost_estimated_usd, cost_by_query_source, models,
	COALESCE(app_version, ''), COALESCE(entrypoint, ''), COALESCE(terminal_type, '')`

// toSummary builds the wire model.SessionSummary from d, computing the two
// derived fields SPEC §4.3 documents but the schema doesn't store directly:
// duration_ms (nil until started_at is known; ended_at else last_event_at
// as the end bound) and partial (true iff started_at is still nil — SPEC
// §1.7's stub-on-reference state, "no session.start was ever seen").
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

// sortValue extracts the value sessionKeysetPredicate/encodeSessionCursor
// need for sortKey out of an already-scanned row.
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

// sessionDurationMS is SPEC §4.3's `duration_ms`: nil until started_at is
// known (SPEC §1.7 stub-on-reference), else the gap to ended_at once the
// session has ended, else to last_event_at while it is still open — the
// same "best known end bound" reasoning SPEC §2.3 documents for
// `tool_calls.wait_ms`.
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

// buildSessionCost assembles model.SessionCost (SPEC §4.3) from the stored
// reported/estimated totals and the raw cost_by_query_source jsonb map
// (SPEC §2.1: "raw query_source value -> summed reported cost.
// Uninterpreted (§1.9)"). estimatedShare is 0 (not NaN) when total cost is
// 0 — an honest "nothing to share" rather than a divide-by-zero.
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

// dominantQuerySource picks the highest-cost key of byQuerySource (SPEC
// §4.3: "dominant_query_source is the highest-cost key; other_query_source_usd
// is the rest"), tie-breaking deterministically on key order — the same
// convention subagent_tree.go's buildCostAttribution uses, duplicated here
// rather than shared because the two functions build different result
// structs (model.SessionCost vs model.SubagentCostAttribution) and this is
// the only piece they'd otherwise share.
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

// listSessionsQuery is what buildListSessionsQuery returns: the full SQL
// text plus its positional args, ready for s.pool.Query — and, prefixed
// with "EXPLAIN ", for read_sessions_test.go's index-usage assertions (SPEC
// §2.5's AC that each of the 4 sorts rides its `sessions_*` index). Kept as
// a named type rather than raw returns so ListSessions and the test share
// one field list.
type listSessionsQuery struct {
	SQL     string
	Args    []any
	SortKey store.SessionSort
	Limit   int
}

// buildListSessionsQuery renders ListSessions' full dynamic SQL: filter.go's
// whitelist WHERE clause, the keyset predicate for an incoming cursor (if
// any), and the ORDER BY/LIMIT for one of SPEC §2.1's four sort keys.
// Factored out of ListSessions itself so read_sessions_test.go's EXPLAIN
// assertions run against the EXACT query ListSessions executes, rather than
// a hand-copied approximation that could silently drift from it.
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

// ListSessions implements store.Reader (SPEC §3.3, §4.3): filtered,
// keyset-paginated, one of the four SPEC §2.1 sort keys. Fetches limit+1
// rows to learn has_more without a second COUNT query, trimming the extra
// row before returning.
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

// GetSession implements store.Reader (SPEC §3.3, §4.3): the session summary
// plus every SessionDetail-only block. Each block is its own query rather
// than one giant join — the row counts involved (permission changes, tool
// names, decision sources, hook events) are all small per session, and a
// single mega-join would multiply rows across unrelated one-to-many
// relationships (permission changes x tool_calls x hook events) for no
// benefit.
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

// ListTurns implements store.Reader (SPEC §3.3: "takes no filter/page ...
// every turn of a session in one page").
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

// sessionTopTools is hand-written pgx SQL, not sqlc, for the reason
// documented in read_sessions.sql: sqlc mis-infers percentile_cont's result
// as NOT NULL. Destination is *float64 so a NULL p50 (every call for that
// tool_name lacked a duration_ms) scans cleanly instead of erroring.
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

// sessionHookLatency implements SessionDetail.HookLatency (SPEC §4.3): nil
// when the session has zero `hook.execution_end` events at all ("no hook
// coverage", SPEC §4.1's null-vs-zero rule — 0ms would be a lie about a
// session where hooks never ran), else the overall p50/p95 (hand-written
// pgx, same NOT-NULL-mis-inference reason as sessionTopTools) plus the p50
// per `hook_event` name.
//
// by_hook_event carries latency, not execution counts: the block is named
// hook_latency, its siblings are p50_ms/p95_ms, and SPEC §4.3's example
// pairs `p50_ms: 9` with `by_hook_event: { PostToolUse: 9 }` — the same
// number, because that session's only hook event is PostToolUse. Per-event
// execution counts are what GET /api/v1/quality/hook-latency reports
// (`executions`), so returning them here too would leave the p50 breakdown
// the panel needs unavailable anywhere.
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

	// Hand-written for the same percentile_cont NOT-NULL-mis-inference
	// reason as the overall p50/p95 above. `hook_event` is never promoted to
	// its own column (SPEC §1.5.1 promotes only duration_ms/success from
	// hook.execution_end), so it is read out of attrs. A hook event whose
	// every execution lacked a duration_ms yields a NULL p50 and is skipped
	// rather than reported as 0ms.
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

// sessionRawEventsExpired implements SPEC's rule: true when the session's
// first_seen_at precedes the oldest events partition currently attached
// (SPEC §2.4: raw retention drops whole months, so a session older than
// every remaining partition has no timeline left even though its row and
// aggregates survive). No partitions at all (a database with none ever
// created) reports false rather than true — there is nothing to have
// "expired" against.
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

// oldestEventsPartitionStart reads the currently-attached `events`
// partitions (same pg_inherits introspection partitionCoverage uses in
// partitions.go, reused here rather than duplicated) and returns the
// earliest partition's lower bound. ok is false when `events` has no
// partitions at all.
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

// roundToIntPtr rounds a nullable float64 percentile (milliseconds) to the
// nearest *int SPEC §4.3's ToolUsageSummary.p50_ms wants, preserving nil.
func roundToIntPtr(f *float64) *int {
	if f == nil {
		return nil
	}
	n := int(math.Round(*f))
	return &n
}

// int64FromFloatPtr rounds a nullable float64 percentile to int64, treating
// a nil input as 0 — only ever called after sessionHookLatency has already
// confirmed executions > 0, so a genuinely nil p50/p95 at that point would
// mean every duration_ms was NULL despite hook coverage existing, and 0ms
// is the least misleading placeholder for that edge case (a true `null`
// would collapse the whole hook_latency block to null too, which SPEC §4.1
// reserves for "no hook coverage at all", a different condition).
func int64FromFloatPtr(f *float64) int64 {
	if f == nil {
		return 0
	}
	return int64(math.Round(*f))
}

// anyToString converts an `interface{}`-scanned jsonb text extraction
// (COALESCE((attrs->>'x')::text, ”) — sqlc cannot resolve the `->>`
// operator's result type against this schema and generates `interface{}`
// fields instead of `string`, see read_sessions.sql) to a string. pgx
// decodes a text-OID value scanned into `interface{}` as a Go string, so
// the type assertion always succeeds in practice; the fallback exists so a
// future pgx behaviour change degrades to fmt.Sprint rather than a panic.
func anyToString(v any) string {
	if v == nil {
		return ""
	}
	if str, ok := v.(string); ok {
		return str
	}
	return fmt.Sprint(v)
}

// textOrEmpty converts a sqlc-generated pgtype.Text into model.SessionDetail's
// plain-string fields (User, OrganizationID, and — via sessionRowData — the
// same nullable vendor columns ListSessions' own COALESCE already handles
// at the SQL layer): "" for SQL NULL, matching SPEC §4.3's examples, which
// never render these as null.
func textOrEmpty(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

// timestamptzOrNil converts a sqlc-generated pgtype.Timestamptz into the
// *time.Time model.SessionSummary/model.Turn use for their nullable
// timestamp fields (StartedAt, EndedAt).
func timestamptzOrNil(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// int4OrNil converts a sqlc-generated pgtype.Int4 into the *int
// model.Turn's TurnIndex/DurationMS fields use for their nullable integer
// columns.
func int4OrNil(n pgtype.Int4) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int32)
	return &v
}
