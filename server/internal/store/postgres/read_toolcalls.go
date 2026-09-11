// Package postgres — read_toolcalls.go implements store.Reader's ListToolCalls.
// Hand-built dynamic-filter query serving both session-scoped and cross-session drill-down.
// Sort/keyset: exactly one order (started_at DESC, id), indexed for both access patterns.
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
	// defaultToolCallLimit / maxToolCallLimit mirror SPEC §4.1's pagination defaults.
	defaultToolCallLimit = 50
	maxToolCallLimit     = 500

	// toolCallCursorKey is the fixed sort-key tag ListToolCalls' cursor binds to.
	toolCallCursorKey = "started_at"
)

// toolCallCursorPayload mirrors sessionCursorPayload (SPEC §4.1): K is sort key, V is [started_at, id].
type toolCallCursorPayload struct {
	K string            `json:"k"`
	V []json.RawMessage `json:"v"`
}

// toolCallCursorEncoding is URL-safe, unpadded base64 (SPEC §4.1).
var toolCallCursorEncoding = base64.RawURLEncoding

// encodeToolCallCursor encodes the next page's cursor from started_at and id.
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

// toolCallKeysetPredicate renders the keyset predicate for started_at DESC, id sort.
func toolCallKeysetPredicate(b *clauseBuilder, startedAt time.Time, id string) string {
	saPH := b.placeholder(startedAt)
	idPH := b.placeholder(id)
	return fmt.Sprintf("(tc.started_at < %s OR (tc.started_at = %s AND tc.id < %s))", saPH, saPH, idPH)
}

// toolCallColumns is the exact column list ListToolCalls' scan destinations agree on.
const toolCallColumns = `tc.id, tc.session_id, tc.prompt_id, tc.tool_use_id, tc.tool_name,
	tc.tool_source, tc.agent_id, tc.decision, tc.decision_source, tc.permission_mode,
	tc.started_at, tc.decided_at, tc.ended_at, tc.duration_ms, tc.wait_ms,
	tc.success, tc.error_type, tc.file_path, tc.input_size_bytes, tc.result_size_bytes,
	tc.correlation, tc.event_count`

// listToolCallsQuery is what buildListToolCallsQuery returns.
type listToolCallsQuery struct {
	SQL   string
	Args  []any
	Limit int
}

// buildListToolCallsQuery renders the full dynamic SQL with WHERE, keyset predicate, ORDER BY, LIMIT.
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

// ListToolCalls implements store.Reader: filtered, keyset-paginated tool calls.
// Fetches limit+1 rows to detect has_more without a second COUNT.
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
