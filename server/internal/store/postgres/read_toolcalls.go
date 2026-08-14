// Package postgres — read_toolcalls.go implements store.Reader's
// ListToolCalls (SPEC §3.3, §4.2, P3-03): one hand-built dynamic-filter
// query (filter.go's whitelist clause builder) serving both the
// session-scoped `GET /api/v1/sessions/{id}/tool-calls` and the
// cross-session "decision-provenance drill-down" `GET /api/v1/tool-calls`
// (SPEC §4.2) through store.ToolCallFilter.SessionID.
//
// Sort/keyset: SPEC's openapi.yaml exposes no `order`/`sort` parameter on
// either tool-calls endpoint (unlike ListEvents/ListSessions), so there is
// exactly one order — `started_at DESC, id` — matching the two SPEC §2.3
// indexes tool_calls carries for this access pattern
// (`(session_id, started_at)`, `(tool_name, started_at DESC)`) and the
// natural "most recent decisions first" reading of a drill-down list. `id`
// (a UUID, always present and unique — SPEC §2.3's PK) is the keyset
// tiebreak, the same role SessionFilter's `id` plays for ListSessions.
package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

const (
	// defaultToolCallLimit / maxToolCallLimit mirror SPEC §4.1's pagination
	// defaults, same as defaultSessionLimit/maxSessionLimit.
	defaultToolCallLimit = 50
	maxToolCallLimit     = 500

	// toolCallCursorKey is the fixed sort-key tag ListToolCalls' cursor
	// binds to (there being only one sort, unlike ListSessions/ListEvents),
	// rejecting a cursor minted by some other endpoint that happens to
	// decode as valid base64/JSON.
	toolCallCursorKey = "started_at"
)

// toolCallCursorPayload mirrors sessionCursorPayload's wire shape (SPEC
// §4.1): `{"k":"<fixed key>","v":[started_at, id]}`.
type toolCallCursorPayload struct {
	K string            `json:"k"`
	V []json.RawMessage `json:"v"`
}

// toolCallCursorEncoding matches sessionCursorEncoding's choice (SPEC
// §4.1): URL-safe, unpadded base64.
var toolCallCursorEncoding = base64.RawURLEncoding

// encodeToolCallCursor renders the next page's cursor from the last row's
// own started_at/id.
func encodeToolCallCursor(startedAt time.Time, id string) (store.Cursor, error) {
	saJSON, err := json.Marshal(startedAt)
	if err != nil {
		return "", fmt.Errorf("postgres: encode tool call cursor: marshal started_at: %w", err)
	}
	idJSON, err := json.Marshal(id)
	if err != nil {
		return "", fmt.Errorf("postgres: encode tool call cursor: marshal id: %w", err)
	}
	body, err := json.Marshal(toolCallCursorPayload{K: toolCallCursorKey, V: []json.RawMessage{saJSON, idJSON}})
	if err != nil {
		return "", fmt.Errorf("postgres: encode tool call cursor: %w", err)
	}
	return store.Cursor(toolCallCursorEncoding.EncodeToString(body)), nil
}

// decodeToolCallCursor parses a cursor minted by encodeToolCallCursor.
func decodeToolCallCursor(c store.Cursor) (startedAt time.Time, id string, err error) {
	raw, err := toolCallCursorEncoding.DecodeString(string(c))
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: not valid base64: %w", ErrInvalidCursor, err)
	}
	var payload toolCallCursorPayload
	if jsonErr := json.Unmarshal(raw, &payload); jsonErr != nil {
		return time.Time{}, "", fmt.Errorf("%w: not valid JSON: %w", ErrInvalidCursor, jsonErr)
	}
	if payload.K != toolCallCursorKey || len(payload.V) != 2 {
		return time.Time{}, "", fmt.Errorf("%w: missing key or malformed values", ErrInvalidCursor)
	}
	if jsonErr := json.Unmarshal(payload.V[0], &startedAt); jsonErr != nil {
		return time.Time{}, "", fmt.Errorf("%w: invalid started_at value: %w", ErrInvalidCursor, jsonErr)
	}
	if jsonErr := json.Unmarshal(payload.V[1], &id); jsonErr != nil {
		return time.Time{}, "", fmt.Errorf("%w: invalid id value: %w", ErrInvalidCursor, jsonErr)
	}
	return startedAt, id, nil
}

// toolCallKeysetPredicate renders the "seek past the last row of the
// previous page" WHERE fragment for the `started_at DESC, id` sort
// (started_at is NOT NULL — SPEC §2.3 — so this needs no NULLS-handling
// branch, unlike sessionKeysetPredicate's started_at case).
func toolCallKeysetPredicate(b *clauseBuilder, startedAt time.Time, id string) string {
	saPH := b.placeholder(startedAt)
	idPH := b.placeholder(id)
	return fmt.Sprintf("(tc.started_at < %s OR (tc.started_at = %s AND tc.id < %s))", saPH, saPH, idPH)
}

// toolCallColumns is the exact column list (and order) ListToolCalls' scan
// destinations agree on, matching model.ToolCall's field order.
const toolCallColumns = `tc.id, tc.session_id, tc.prompt_id, tc.tool_use_id, tc.tool_name,
	tc.tool_source, tc.agent_id, tc.decision, tc.decision_source, tc.permission_mode,
	tc.started_at, tc.decided_at, tc.ended_at, tc.duration_ms, tc.wait_ms,
	tc.success, tc.error_type, tc.file_path, tc.input_size_bytes, tc.result_size_bytes,
	tc.correlation, tc.event_count`

// listToolCallsQuery is what buildListToolCallsQuery returns, matching
// listSessionsQuery/listEventsQuery's shape/purpose.
type listToolCallsQuery struct {
	SQL   string
	Args  []any
	Limit int
}

// buildListToolCallsQuery renders ListToolCalls' full dynamic SQL:
// filter.go's whitelist WHERE clause, the keyset predicate for an incoming
// cursor (if any), and the fixed `started_at DESC, id DESC` ORDER BY/LIMIT.
// Factored out of ListToolCalls itself so a future EXPLAIN test runs
// against the EXACT query ListToolCalls executes, matching
// buildListSessionsQuery/buildListEventsQuery's convention.
func buildListToolCallsQuery(f store.ToolCallFilter, p store.Page) (listToolCallsQuery, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = defaultToolCallLimit
	}
	if limit > maxToolCallLimit {
		limit = maxToolCallLimit
	}

	b := newClauseBuilder()
	var clauses []string
	if where := toolCallWhereClause(b, f); where != "" {
		clauses = append(clauses, where)
	}
	if p.Cursor != "" {
		startedAt, id, err := decodeToolCallCursor(p.Cursor)
		if err != nil {
			return listToolCallsQuery{}, err
		}
		clauses = append(clauses, toolCallKeysetPredicate(b, startedAt, id))
	}

	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	limitPH := b.placeholder(int32(limit + 1))

	sql := fmt.Sprintf(`
		SELECT %s
		FROM tool_calls tc
		%s
		ORDER BY tc.started_at DESC, tc.id DESC
		LIMIT %s`, toolCallColumns, where, limitPH)

	return listToolCallsQuery{SQL: sql, Args: b.args, Limit: limit}, nil
}

// ListToolCalls implements store.Reader (SPEC §3.3, §4.2): filtered,
// keyset-paginated tool calls, serving both the session-scoped list and the
// cross-session decision-provenance drill-down through
// store.ToolCallFilter.SessionID. Fetches limit+1 rows to learn has_more
// without a second COUNT query, matching ListSessions/ListEvents.
func (s *Store) ListToolCalls(ctx context.Context, f store.ToolCallFilter, p store.Page) ([]model.ToolCall, store.Cursor, error) {
	q, err := buildListToolCallsQuery(f, p)
	if err != nil {
		return nil, "", err
	}

	rows, err := s.pool.Query(ctx, q.SQL, q.Args...)
	if err != nil {
		return nil, "", fmt.Errorf("postgres: list tool calls: %w", err)
	}
	defer rows.Close()

	var calls []model.ToolCall
	for rows.Next() {
		var (
			tc          model.ToolCall
			correlation string
		)
		if scanErr := rows.Scan(
			&tc.ID, &tc.SessionID, &tc.PromptID, &tc.ToolUseID, &tc.ToolName,
			&tc.ToolSource, &tc.AgentID, &tc.Decision, &tc.DecisionSource, &tc.PermissionMode,
			&tc.StartedAt, &tc.DecidedAt, &tc.EndedAt, &tc.DurationMS, &tc.WaitMS,
			&tc.Success, &tc.ErrorType, &tc.FilePath, &tc.InputSizeBytes, &tc.ResultSizeBytes,
			&correlation, &tc.EventCount,
		); scanErr != nil {
			return nil, "", fmt.Errorf("postgres: list tool calls: scan: %w", scanErr)
		}
		tc.Correlation = model.Correlation(correlation)
		calls = append(calls, tc)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, "", fmt.Errorf("postgres: list tool calls: %w", rowsErr)
	}

	hasMore := len(calls) > q.Limit
	if hasMore {
		calls = calls[:q.Limit]
	}

	var nextCursor store.Cursor
	if hasMore && len(calls) > 0 {
		last := calls[len(calls)-1]
		var cursorErr error
		nextCursor, cursorErr = encodeToolCallCursor(last.StartedAt, last.ID)
		if cursorErr != nil {
			return nil, "", cursorErr
		}
	}

	return calls, nextCursor, nil
}
