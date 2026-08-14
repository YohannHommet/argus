// Package postgres — read_events.go implements store.Reader's ListEvents and
// GetEvent (SPEC §3.3, §4.3, P3-03). ListEvents is one of the three
// hand-built dynamic-filter/dynamic-sort queries SPEC §3.3 carves out of
// sqlc (see filter.go's package doc), serving both the session-scoped
// timeline (`GET /api/v1/sessions/{id}/timeline`) and the cross-session
// search (`GET /api/v1/events`) through the same store.EventFilter.SessionID
// switch. GetEvent is a fixed, single-statement PK lookup and goes through
// sqlc (db/queries/read_events.sql), matching read_sessions.go's GetSession.
//
// Ordering vs keyset (SPEC §1.2, §4.3): the documented response order is
// `(ts, vendor_seq NULLS LAST, seq)`, NOT the same order as the `(ts, seq)`
// primary key. A keyset predicate built only from (ts, seq) — "continue past
// the last row's ts/seq" — would silently skip or repeat rows whenever two
// events share the same ts but differ in vendor_seq in a way that reorders
// them relative to seq (e.g. seq=10/vendor_seq=5 and seq=11/vendor_seq=3 at
// the same ts: the correct order is seq=11 before seq=10, but a (ts, seq)-
// only predicate would treat seq=11's page boundary as "everything with
// seq>11", dropping seq=10 entirely). So the keyset predicate here is the
// exact 3-key lexicographic continuation of (ts, vendor_seq, seq), not a
// (ts, seq) shortcut — see eventKeysetPredicate. This still rides the
// `events (session_id, ts, seq)` index for the session-scoped case (SPEC
// §2.5): the predicate's leading term is ts (the partition key and the
// index's second column), so Postgres both prunes partitions and uses the
// index for the session_id equality + ts range, applying the vendor_seq/seq
// tiebreak as a filter — it does not need to avoid a Sort node to satisfy
// the "index scan with partition pruning" AC.
//
// `order=desc` is the *exact reverse* of `order=asc`, not a naive `DESC` on
// each column: SPEC's `vendor_seq NULLS LAST` ordering is the "NULL sorts as
// +infinity" total order, and Postgres's own DESC default (NULLS FIRST) is
// exactly that order's reverse traversal — a null vendor_seq is still the
// largest possible value, so it appears first when descending. asc/desc
// therefore share one predicate/comparison shape (vendorSeqCompare,
// vendorSeqEqual), parameterized only by direction.
package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
	"github.com/YohannHommet/argus/server/internal/store/postgres/gen"
)

const (
	// defaultEventLimit / maxEventLimit mirror SPEC §4.1's pagination
	// defaults, same as defaultSessionLimit/maxSessionLimit.
	defaultEventLimit = 50
	maxEventLimit     = 500
)

// ErrEventNotFound is GetEvent's not-found signal (SPEC §4.2's `GET
// /api/v1/events/{ref}` 404 response), wrapping pgx.ErrNoRows so callers
// can match on it without importing pgx themselves — same convention as
// ErrSessionNotFound, and like it an alias for the seam-level
// store.ErrEventNotFound rather than its own value.
var ErrEventNotFound = store.ErrEventNotFound

// eventCursorPayload is the wire shape SPEC §4.1 specifies:
// `{"k":"<sort key>","v":[…]}`. V holds exactly three elements: ts,
// vendor_seq (nullable), seq — the full (ts, vendor_seq, seq) tuple the
// ordering AC requires (see package doc). K is the order ("asc"/"desc") the
// cursor was minted under, rejecting a cursor replayed against the opposite
// direction the same way sessionCursorPayload rejects a cursor replayed
// against a different sort.
type eventCursorPayload struct {
	K string            `json:"k"`
	V []json.RawMessage `json:"v"`
}

// eventCursorEncoding matches sessionCursorEncoding's choice (SPEC §4.1):
// URL-safe, unpadded base64.
var eventCursorEncoding = base64.RawURLEncoding

// encodeEventCursor renders the next page's cursor from the last row's own
// (ts, vendor_seq, seq).
func encodeEventCursor(order store.SortOrder, ts time.Time, vendorSeq *int64, seq int64) (store.Cursor, error) {
	tsJSON, err := json.Marshal(ts)
	if err != nil {
		return "", fmt.Errorf("postgres: encode event cursor: marshal ts: %w", err)
	}
	vsJSON, err := json.Marshal(vendorSeq)
	if err != nil {
		return "", fmt.Errorf("postgres: encode event cursor: marshal vendor_seq: %w", err)
	}
	seqJSON, err := json.Marshal(seq)
	if err != nil {
		return "", fmt.Errorf("postgres: encode event cursor: marshal seq: %w", err)
	}
	body, err := json.Marshal(eventCursorPayload{K: string(order), V: []json.RawMessage{tsJSON, vsJSON, seqJSON}})
	if err != nil {
		return "", fmt.Errorf("postgres: encode event cursor: %w", err)
	}
	return store.Cursor(eventCursorEncoding.EncodeToString(body)), nil
}

// decodeEventCursor parses a cursor minted by encodeEventCursor, enforcing
// order binding (a cursor minted under asc is rejected when replayed
// against desc, matching decodeSessionCursor's sort-key binding).
func decodeEventCursor(c store.Cursor, order store.SortOrder) (ts time.Time, vendorSeq *int64, seq int64, err error) {
	raw, err := eventCursorEncoding.DecodeString(string(c))
	if err != nil {
		return time.Time{}, nil, 0, fmt.Errorf("%w: not valid base64: %w", ErrInvalidCursor, err)
	}
	var payload eventCursorPayload
	if jsonErr := json.Unmarshal(raw, &payload); jsonErr != nil {
		return time.Time{}, nil, 0, fmt.Errorf("%w: not valid JSON: %w", ErrInvalidCursor, jsonErr)
	}
	if payload.K == "" || len(payload.V) != 3 {
		return time.Time{}, nil, 0, fmt.Errorf("%w: missing key or malformed values", ErrInvalidCursor)
	}
	if payload.K != string(order) {
		return time.Time{}, nil, 0, fmt.Errorf("%w: minted for order %q, replayed against %q", ErrInvalidCursor, payload.K, order)
	}
	if jsonErr := json.Unmarshal(payload.V[0], &ts); jsonErr != nil {
		return time.Time{}, nil, 0, fmt.Errorf("%w: invalid ts value: %w", ErrInvalidCursor, jsonErr)
	}
	if jsonErr := json.Unmarshal(payload.V[1], &vendorSeq); jsonErr != nil {
		return time.Time{}, nil, 0, fmt.Errorf("%w: invalid vendor_seq value: %w", ErrInvalidCursor, jsonErr)
	}
	if jsonErr := json.Unmarshal(payload.V[2], &seq); jsonErr != nil {
		return time.Time{}, nil, 0, fmt.Errorf("%w: invalid seq value: %w", ErrInvalidCursor, jsonErr)
	}
	return ts, vendorSeq, seq, nil
}

// vendorSeqCompare renders "vendor_seq is strictly further along than v0 in
// the direction of travel" under the NULLS-are-+infinity total order the
// package doc describes: for asc (continuing forward), nothing is "greater"
// than a nil v0 since nil is already the maximum, so "" is returned (the
// caller must fall through to the equality tier); for desc (continuing
// backward) past a nil v0, every non-nil vendor_seq is "less than infinity".
// For a non-nil v0, the nil column value always counts as satisfying "greater
// than v0" (asc) since nil is the maximum, but never satisfies "less than
// v0" (desc).
func vendorSeqCompare(b *clauseBuilder, column string, v0 *int64, asc bool) string {
	if v0 == nil {
		if asc {
			return ""
		}
		return column + " IS NOT NULL"
	}
	ph := b.placeholder(*v0)
	if asc {
		return fmt.Sprintf("(%s > %s OR %s IS NULL)", column, ph, column)
	}
	return fmt.Sprintf("%s < %s", column, ph)
}

// vendorSeqEqual renders the tie-tier comparison for vendorSeqCompare's v0:
// "vendor_seq IS NULL" when v0 itself was nil, else an exact equality.
func vendorSeqEqual(b *clauseBuilder, column string, v0 *int64) string {
	if v0 == nil {
		return column + " IS NULL"
	}
	return fmt.Sprintf("%s = %s", column, b.placeholder(*v0))
}

// eventKeysetPredicate renders the "seek past the last row of the previous
// page" WHERE fragment for the full (ts, vendor_seq NULLS LAST, seq) sort
// key (SPEC §1.2, §4.3) — see package doc for why this must be the exact
// 3-tuple continuation, not a (ts, seq) shortcut. asc/desc share this one
// shape; only the comparison operator and vendorSeqCompare's direction
// differ (package doc: desc is the tuple order's exact reverse).
func eventKeysetPredicate(b *clauseBuilder, order store.SortOrder, tsCol, vendorSeqCol, seqCol string, ts time.Time, vendorSeq *int64, seq int64) string {
	asc := order != store.OrderDesc
	op := ">"
	if !asc {
		op = "<"
	}
	tsPH := b.placeholder(ts)
	seqPH := b.placeholder(seq)

	clauses := []string{fmt.Sprintf("%s %s %s", tsCol, op, tsPH)}
	if vsGreater := vendorSeqCompare(b, vendorSeqCol, vendorSeq, asc); vsGreater != "" {
		clauses = append(clauses, fmt.Sprintf("(%s = %s AND %s)", tsCol, tsPH, vsGreater))
	}
	vsEqual := vendorSeqEqual(b, vendorSeqCol, vendorSeq)
	clauses = append(clauses, fmt.Sprintf("(%s = %s AND %s AND %s %s %s)", tsCol, tsPH, vsEqual, seqCol, op, seqPH))

	return "(" + strings.Join(clauses, " OR ") + ")"
}

// eventColumnsSlim is the column list (and order) both ListEvents' scan
// destinations and eventColumnsFull (which appends ", e.attrs") agree on.
// fields=slim never selects attrs at all (SPEC ticket note: "the point is
// not transferring it"), not merely omitting it from the response.
const eventColumnsSlim = `e.seq, e.id, e.ts, e.ingested_at, e.session_id, e.prompt_id, e.vendor, e.source, e.kind, e.event_name, e.vendor_seq,
	e.tool_name, e.tool_use_id, e.decision, e.decision_source, e.tool_source, e.query_source, e.model,
	e.input_tokens, e.output_tokens, e.cache_read_tokens, e.cache_creation_tokens,
	e.cost_usd, e.cost_source, e.duration_ms, e.success, e.error_type,
	e.agent_id, e.parent_agent_id, e.agent_type, e.permission_mode, e.file_path,
	e.request_id, e.message_uuid, e.clock_skewed, e.dedup_key`

const eventColumnsFull = eventColumnsSlim + `, e.attrs`

// scanEvent scans one row produced by eventColumnsSlim (withAttrs=false) or
// eventColumnsFull (withAttrs=true) into a model.Event.
func scanEvent(rows pgx.Rows, withAttrs bool) (model.Event, error) {
	var e model.Event
	dest := []any{
		&e.Seq, &e.ID, &e.TS, &e.IngestedAt, &e.SessionID, &e.PromptID, &e.Vendor, &e.Source, &e.Kind, &e.EventName, &e.VendorSeq,
		&e.ToolName, &e.ToolUseID, &e.Decision, &e.DecisionSource, &e.ToolSource, &e.QuerySource, &e.Model,
		&e.InputTokens, &e.OutputTokens, &e.CacheReadTokens, &e.CacheCreationTokens,
		&e.CostUSD, &e.CostSource, &e.DurationMS, &e.Success, &e.ErrorType,
		&e.AgentID, &e.ParentAgentID, &e.AgentType, &e.PermissionMode, &e.FilePath,
		&e.RequestID, &e.MessageUUID, &e.ClockSkewed, &e.DedupKey,
	}
	if withAttrs {
		dest = append(dest, &e.Attrs)
	}
	if err := rows.Scan(dest...); err != nil {
		return model.Event{}, fmt.Errorf("postgres: scan event: %w", err)
	}
	return e, nil
}

// listEventsQuery is what buildListEventsQuery returns: the full SQL text
// plus its positional args, ready for s.pool.Query — and, prefixed with
// "EXPLAIN ", for read_events_test.go's index-usage assertions. Matches
// listSessionsQuery's shape/purpose.
type listEventsQuery struct {
	SQL       string
	Args      []any
	Order     store.SortOrder
	Limit     int
	WithAttrs bool
}

// buildListEventsQuery renders ListEvents' full dynamic SQL: filter.go's
// whitelist WHERE clause, the keyset predicate for an incoming cursor (if
// any), and the ORDER BY/LIMIT for SPEC §1.2's (ts, vendor_seq NULLS LAST,
// seq) sort — or its exact reverse. Factored out of ListEvents itself so
// read_events_test.go's EXPLAIN assertions run against the EXACT query
// ListEvents executes.
func buildListEventsQuery(f store.EventFilter, p store.Page) (listEventsQuery, error) {
	order := f.Order
	if order == "" {
		order = store.OrderAsc
	}
	if order != store.OrderAsc && order != store.OrderDesc {
		return listEventsQuery{}, fmt.Errorf("postgres: list events: unknown order %q", order)
	}

	withAttrs := f.Fields == store.FieldsFull

	limit := p.Limit
	if limit <= 0 {
		limit = defaultEventLimit
	}
	if limit > maxEventLimit {
		limit = maxEventLimit
	}

	b := newClauseBuilder()
	var clauses []string
	if where := eventWhereClause(b, f); where != "" {
		clauses = append(clauses, where)
	}
	if p.Cursor != "" {
		ts, vendorSeq, seq, err := decodeEventCursor(p.Cursor, order)
		if err != nil {
			return listEventsQuery{}, err
		}
		clauses = append(clauses, eventKeysetPredicate(b, order, "e.ts", "e.vendor_seq", "e.seq", ts, vendorSeq, seq))
	}

	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}

	dir, nullsClause := "ASC", "NULLS LAST"
	if order == store.OrderDesc {
		dir, nullsClause = "DESC", "NULLS FIRST"
	}
	limitPH := b.placeholder(int32(limit + 1))

	columns := eventColumnsSlim
	if withAttrs {
		columns = eventColumnsFull
	}

	sql := fmt.Sprintf(`
		SELECT %s
		FROM events e
		%s
		ORDER BY e.ts %s, e.vendor_seq %s %s, e.seq %s
		LIMIT %s`, columns, where, dir, dir, nullsClause, dir, limitPH)

	return listEventsQuery{SQL: sql, Args: b.args, Order: order, Limit: limit, WithAttrs: withAttrs}, nil
}

// ListEvents implements store.Reader (SPEC §3.3, §4.3): filtered,
// keyset-paginated events, serving both the session-scoped timeline and the
// cross-session search through store.EventFilter.SessionID. Fetches limit+1
// rows to learn has_more without a second COUNT query, trimming the extra
// row before returning — same convention as ListSessions.
func (s *Store) ListEvents(ctx context.Context, f store.EventFilter, p store.Page) ([]model.Event, store.Cursor, error) {
	q, err := buildListEventsQuery(f, p)
	if err != nil {
		return nil, "", err
	}

	rows, err := s.pool.Query(ctx, q.SQL, q.Args...)
	if err != nil {
		return nil, "", fmt.Errorf("postgres: list events: %w", err)
	}
	defer rows.Close()

	var events []model.Event
	for rows.Next() {
		e, scanErr := scanEvent(rows, q.WithAttrs)
		if scanErr != nil {
			return nil, "", scanErr
		}
		events = append(events, e)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, "", fmt.Errorf("postgres: list events: %w", rowsErr)
	}

	hasMore := len(events) > q.Limit
	if hasMore {
		events = events[:q.Limit]
	}

	var nextCursor store.Cursor
	if hasMore && len(events) > 0 {
		last := events[len(events)-1]
		var cursorErr error
		nextCursor, cursorErr = encodeEventCursor(q.Order, last.TS, last.VendorSeq, last.Seq)
		if cursorErr != nil {
			return nil, "", cursorErr
		}
	}

	return events, nextCursor, nil
}

// GetEvent implements store.Reader (SPEC §1.2, §4.2): a PK lookup on (ts,
// seq) — the event_ref's decoded form — always with attrs (SPEC's
// EventDetail is TimelineEvent plus attrs, unconditionally). Fixed,
// single-statement, no filter/sort, so it goes through sqlc
// (db/queries/read_events.sql), matching GetSession's own reasoning. There
// is no index on events.id (SPEC §1.2, §2.2), so ref is the only lookup key
// this method accepts.
func (s *Store) GetEvent(ctx context.Context, ref model.EventRef) (*model.Event, error) {
	row, err := gen.New(s.pool).GetEventByRef(ctx, gen.GetEventByRefParams{
		Ts:  pgtype.Timestamptz{Time: ref.TS, Valid: true},
		Seq: ref.Seq,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEventNotFound
		}
		return nil, fmt.Errorf("postgres: get event: %w", err)
	}

	e := model.Event{
		Seq:                 row.Seq,
		ID:                  uuidToString(row.ID),
		TS:                  row.Ts.Time,
		IngestedAt:          row.IngestedAt.Time,
		SessionID:           row.SessionID,
		PromptID:            textOrNil(row.PromptID),
		Vendor:              row.Vendor,
		Source:              model.Source(row.Source),
		Kind:                model.Kind(row.Kind),
		EventName:           row.EventName,
		VendorSeq:           int8OrNil(row.VendorSeq),
		ToolName:            textOrNil(row.ToolName),
		ToolUseID:           textOrNil(row.ToolUseID),
		Decision:            textOrNil(row.Decision),
		DecisionSource:      textOrNil(row.DecisionSource),
		ToolSource:          textOrNil(row.ToolSource),
		QuerySource:         textOrNil(row.QuerySource),
		Model:               textOrNil(row.Model),
		InputTokens:         int8OrNil(row.InputTokens),
		OutputTokens:        int8OrNil(row.OutputTokens),
		CacheReadTokens:     int8OrNil(row.CacheReadTokens),
		CacheCreationTokens: int8OrNil(row.CacheCreationTokens),
		CostSource:          textOrNil(row.CostSource),
		DurationMS:          int4OrNil(row.DurationMs),
		Success:             boolOrNil(row.Success),
		ErrorType:           textOrNil(row.ErrorType),
		AgentID:             textOrNil(row.AgentID),
		ParentAgentID:       textOrNil(row.ParentAgentID),
		AgentType:           textOrNil(row.AgentType),
		PermissionMode:      textOrNil(row.PermissionMode),
		FilePath:            textOrNil(row.FilePath),
		RequestID:           textOrNil(row.RequestID),
		MessageUUID:         textOrNil(row.MessageUuid),
		ClockSkewed:         row.ClockSkewed,
		DedupKey:            row.DedupKey,
	}
	if row.CostUsd.Valid {
		f, fErr := row.CostUsd.Float64Value()
		if fErr != nil {
			return nil, fmt.Errorf("postgres: get event: decode cost_usd: %w", fErr)
		}
		e.CostUSD = &f.Float64
	}
	if len(row.Attrs) > 0 {
		if jsonErr := json.Unmarshal(row.Attrs, &e.Attrs); jsonErr != nil {
			return nil, fmt.Errorf("postgres: get event: decode attrs: %w", jsonErr)
		}
	}

	return &e, nil
}

// uuidToString renders a sqlc-generated pgtype.UUID as the plain string
// model.Event.ID and model.ToolCall.ID use (SPEC §1.2: "id uuid ... opaque
// stable identifier"), "" for SQL NULL (never expected on a NOT NULL uuid
// column, but a total function is simpler than one more error path here).
func uuidToString(v pgtype.UUID) string {
	if !v.Valid {
		return ""
	}
	return uuid.UUID(v.Bytes).String()
}

// textOrNil converts a sqlc-generated pgtype.Text into the *string
// model.Event's nullable text fields use.
func textOrNil(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	v := t.String
	return &v
}

// int8OrNil converts a sqlc-generated pgtype.Int8 into the *int64
// model.Event's nullable bigint fields (VendorSeq, InputTokens, ...) use.
func int8OrNil(n pgtype.Int8) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// boolOrNil converts a sqlc-generated pgtype.Bool into the *bool
// model.Event.Success uses.
func boolOrNil(b pgtype.Bool) *bool {
	if !b.Valid {
		return nil
	}
	v := b.Bool
	return &v
}
