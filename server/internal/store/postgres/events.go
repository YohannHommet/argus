// Package postgres — events.go implements the bulk `events` insert (SPEC
// §2.2, §1.6) WriteBatch issues after sessions/turns: one unnest-driven
// INSERT for every candidate event, sorted by (ts, dedup_key) ascending
// (the lock-ordering invariant, SPEC §1.6), with the parent-level
// `ON CONFLICT (ts, dedup_key) DO NOTHING` SPEC §1.7 rule 2 calls "defence
// in depth" behind the ingest_dedup ledger.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/YohannHommet/argus/server/internal/model"
)

// insertedEvent is one row insertEvents actually persisted: enough to build
// model.EventRef and the rollup_dirty hour marks.
type insertedEvent struct {
	TS  time.Time
	Seq int64
}

// insertEvents bulk-inserts candidates (already too_old-filtered by the
// caller) into `events`, sorted by (ts, dedup_key) ascending, and returns
// exactly the rows the parent-level UNIQUE (ts, dedup_key) constraint
// actually admitted.
func insertEvents(ctx context.Context, tx pgx.Tx, candidates []model.Event) (map[string]insertedEvent, error) {
	if len(candidates) == 0 {
		return map[string]insertedEvent{}, nil
	}
	sorted := make([]model.Event, len(candidates))
	copy(sorted, candidates)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].TS.Equal(sorted[j].TS) {
			return sorted[i].TS.Before(sorted[j].TS)
		}
		return sorted[i].DedupKey < sorted[j].DedupKey
	})

	n := len(sorted)
	ids := make([]string, n)
	ts := make([]time.Time, n)
	ingestedAt := make([]time.Time, n)
	sessionID := make([]string, n)
	promptID := make([]*string, n)
	vendor := make([]string, n)
	source := make([]string, n)
	kind := make([]string, n)
	eventName := make([]string, n)
	vendorSeq := make([]*int64, n)
	toolName := make([]*string, n)
	toolUseID := make([]*string, n)
	decision := make([]*string, n)
	decisionSource := make([]*string, n)
	toolSource := make([]*string, n)
	querySource := make([]*string, n)
	modelName := make([]*string, n)
	inputTokens := make([]*int64, n)
	outputTokens := make([]*int64, n)
	cacheReadTokens := make([]*int64, n)
	cacheCreationTokens := make([]*int64, n)
	costUSD := make([]*float64, n)
	costSource := make([]*string, n)
	durationMS := make([]*int32, n)
	success := make([]*bool, n)
	errorType := make([]*string, n)
	agentID := make([]*string, n)
	parentAgentID := make([]*string, n)
	agentType := make([]*string, n)
	permissionMode := make([]*string, n)
	filePath := make([]*string, n)
	requestID := make([]*string, n)
	messageUUID := make([]*string, n)
	clockSkewed := make([]bool, n)
	dedupKey := make([]string, n)
	attrs := make([]string, n)

	for i, e := range sorted {
		ids[i] = e.ID
		ts[i] = e.TS
		ingestedAt[i] = e.IngestedAt
		sessionID[i] = e.SessionID
		promptID[i] = e.PromptID
		vendor[i] = e.Vendor
		source[i] = string(e.Source)
		kind[i] = string(e.Kind)
		eventName[i] = e.EventName
		vendorSeq[i] = e.VendorSeq
		toolName[i] = e.ToolName
		toolUseID[i] = e.ToolUseID
		decision[i] = e.Decision
		decisionSource[i] = e.DecisionSource
		toolSource[i] = e.ToolSource
		querySource[i] = e.QuerySource
		modelName[i] = e.Model
		inputTokens[i] = e.InputTokens
		outputTokens[i] = e.OutputTokens
		cacheReadTokens[i] = e.CacheReadTokens
		cacheCreationTokens[i] = e.CacheCreationTokens
		costUSD[i] = e.CostUSD
		costSource[i] = e.CostSource
		if e.DurationMS != nil {
			d := int32(*e.DurationMS) //nolint:gosec // duration_ms is an int column (SPEC §2.2); Event.DurationMS is a small millisecond count, never near int32's range.
			durationMS[i] = &d
		}
		success[i] = e.Success
		errorType[i] = e.ErrorType
		agentID[i] = e.AgentID
		parentAgentID[i] = e.ParentAgentID
		agentType[i] = e.AgentType
		permissionMode[i] = e.PermissionMode
		filePath[i] = e.FilePath
		requestID[i] = e.RequestID
		messageUUID[i] = e.MessageUUID
		clockSkewed[i] = e.ClockSkewed
		dedupKey[i] = e.DedupKey

		raw := e.Attrs
		if raw == nil {
			raw = map[string]any{}
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("postgres: marshal event attrs (dedup_key=%s): %w", e.DedupKey, err)
		}
		attrs[i] = string(b)
	}

	rows, err := tx.Query(ctx, insertEventsSQL,
		ids, ts, ingestedAt, sessionID, promptID, vendor, source, kind, eventName, vendorSeq,
		toolName, toolUseID, decision, decisionSource, toolSource, querySource, modelName,
		inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens,
		costUSD, costSource, durationMS, success, errorType,
		agentID, parentAgentID, agentType, permissionMode, filePath,
		requestID, messageUUID, clockSkewed, dedupKey, attrs,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: insert events: %w", err)
	}
	defer rows.Close()

	out := make(map[string]insertedEvent, n)
	for rows.Next() {
		var dk string
		var ins insertedEvent
		if err := rows.Scan(&ins.TS, &ins.Seq, &dk); err != nil {
			return nil, fmt.Errorf("postgres: scan inserted event: %w", err)
		}
		out[dk] = ins
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: insert events: %w", err)
	}
	return out, nil
}

const insertEventsSQL = `
INSERT INTO events (
    id, ts, ingested_at, session_id, prompt_id, vendor, source, kind, event_name, vendor_seq,
    tool_name, tool_use_id, decision, decision_source, tool_source, query_source, model,
    input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
    cost_usd, cost_source, duration_ms, success, error_type,
    agent_id, parent_agent_id, agent_type, permission_mode, file_path,
    request_id, message_uuid, clock_skewed, dedup_key, attrs
)
SELECT * FROM unnest(
    $1::uuid[], $2::timestamptz[], $3::timestamptz[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[], $10::bigint[],
    $11::text[], $12::text[], $13::text[], $14::text[], $15::text[], $16::text[], $17::text[],
    $18::bigint[], $19::bigint[], $20::bigint[], $21::bigint[],
    $22::float8[], $23::text[], $24::int[], $25::bool[], $26::text[],
    $27::text[], $28::text[], $29::text[], $30::text[], $31::text[],
    $32::text[], $33::text[], $34::bool[], $35::text[], $36::jsonb[]
)
ON CONFLICT (ts, dedup_key) DO NOTHING
RETURNING ts, seq, dedup_key
`
