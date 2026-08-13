// Package postgres — upsert_toolcall.go builds the `tool_calls` upsert
// (SPEC §1.6, §1.5.3, §2.3): the P2-07 seam in write.go, filled in.
//
// # The normalize/store split (lead note 1)
//
// normalize/correlate.go owns everything that can be computed with no I/O:
// the deterministic id (ToolCallID), what one event contributes
// (ExtractContribution), and the heuristic's actual one-to-one matching
// decision (AssignKeylessContributions). This file owns everything that
// needs a transaction: querying which existing tool_calls rows are "open"
// candidates for the heuristic (queryOpenCalls, GetOpenToolCalls),
// allocating ordinals for brand-new keyless calls from a persisted count
// (nextOrdinalFunc, CountKeylessToolCalls), folding contributions into
// per-row deltas, issuing the actual upsert SQL, and recomputing
// sessions.tool_call_count/tool_reject_count and their turns.* counterparts
// from the tool_calls table itself (lead note 4).
//
// # Determinism and late data (lead note 5)
//
// A keyed id (tool_use_id present) is a pure hash of (session_id,
// tool_use_id) — always reproducible, order-independent, no edge case.
//
// A keyless id's ordinal is seeded from CountKeylessToolCalls: "how many
// keyless rows already exist for this (session, prompt, tool) key",
// queried fresh at the start of each upsertToolCalls call and incremented
// locally as new calls are minted within that same call, in (ts, seq)
// order. This is exactly right for `argusd rebuild-projections`, which
// starts from an empty tool_calls table and replays every event globally
// in (ts, seq) order in one pass — the invariant P3-10 depends on. It is a
// documented, accepted approximation for live incremental ingestion: a
// keyless event arriving late (in a batch processed after later-ts keyless
// events for the same key already got lower ordinals) is appended at the
// end of the ordinal sequence rather than being inserted at its true
// chronological position, so its id depends on arrival order, not ts order,
// for that one case. This cannot silently corrupt data — every id is still
// unique and every row still correct — it only means a full rebuild may
// assign a *different* id to that specific late keyless call than live
// ingestion did. No v1 feature keys off keyless ids across a rebuild
// boundary, and SPEC's own guarantee (§1.6) is that decision provenance
// never depends on the heuristic in the first place.
//
// # started_at fallback (lead note 7)
//
// tool_calls.started_at is NOT NULL, but a tool.result can arrive with no
// preceding tool.pre/tool.decision (e.g. the pre event was too_old-dropped,
// or hooks are disabled). started_at is folded from two trackers: the
// SPEC-mandated "earliest of tool.pre/tool.decision" when either exists,
// falling back to the earliest timestamp of *any* folded contribution
// otherwise, so the NOT NULL constraint is always satisfiable. Because the
// column is always written via LEAST(existing, incoming) on every write, a
// later-arriving tool.pre with an earlier true timestamp self-heals a
// previously-fallback-estimated started_at automatically — no special-case
// code needed for that healing.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store/postgres/gen"
)

// decisionRank ranks a contribution's authority over the decision /
// decision_source / tool_source triplet specifically (SPEC §1.5.3:
// "tool_decision > tool_result > hook"). The generic sourceRank (used
// elsewhere in this package, otel_log=30/hook=20/otel_metric=10) cannot
// express this: tool_decision and tool_result are BOTH source=otel_log, so
// a plain source-based rank can't tell them apart. This function encodes
// the finer precedence, scoped to exactly this one triplet of fields (and
// decided_at, which by construction is only ever offered by a
// KindToolDecision contribution).
func decisionRank(source model.Source, kind model.Kind) int {
	switch {
	case kind == model.KindToolDecision && source == model.SourceOTelLog:
		return 30
	case kind == model.KindToolResult && source == model.SourceOTelLog:
		return 20
	case source == model.SourceHook:
		return 10
	default:
		return 0
	}
}

// rankedBoolField / rankedIntField mirror rankedValue (upsert_session.go)
// for bool/int-typed field_ranks-governed columns; rankedValue itself stays
// string-only because "skip on empty string" (its zero-value sentinel) has
// no bool/int analogue.
type rankedBoolField struct {
	val  bool
	rank int
	ts   time.Time
	set  bool
}

func (r *rankedBoolField) offer(val *bool, rank int, ts time.Time) {
	if val == nil {
		return
	}
	if !r.set || rank > r.rank || (rank == r.rank && ts.After(r.ts)) {
		*r = rankedBoolField{val: *val, rank: rank, ts: ts, set: true}
	}
}

type rankedIntField struct {
	val  int
	rank int
	ts   time.Time
	set  bool
}

func (r *rankedIntField) offer(val *int, rank int, ts time.Time) {
	if val == nil {
		return
	}
	if !r.set || rank > r.rank || (rank == r.rank && ts.After(r.ts)) {
		*r = rankedIntField{val: *val, rank: rank, ts: ts, set: true}
	}
}

// rankedTime is like rankedBoolField but for decided_at: the only column
// whose *value itself* is a timestamp governed by a rank (every other
// timestamp column on tool_calls — started_at, ended_at — is a plain
// LEAST/GREATEST, not rank-governed, per SPEC §1.5.3's table).
type rankedTime struct {
	ts   time.Time
	rank int
	set  bool
}

func (r *rankedTime) offer(ts time.Time, rank int) {
	if !r.set || rank > r.rank || (rank == r.rank && ts.After(r.ts)) {
		*r = rankedTime{ts: ts, rank: rank, set: true}
	}
}

// toolCallAgg accumulates one tool_calls row's contribution from the
// batch's candidate events (SPEC §1.6 projections table: "built from
// tool.pre / tool.decision / tool.permission_request / tool.result").
type toolCallAgg struct {
	id        uuid.UUID
	sessionID string
	promptID  *string
	toolUseID *string // nil is meaningful even for a heuristically-matched
	// keyless contribution targeting an id that IS keyed in the database —
	// see this file's package doc: the upsert SQL COALESCEs with the
	// existing row's tool_use_id, so a nil here never stomps a real value.
	toolName string

	startedAtStrict *time.Time // MIN over tool.pre/tool.decision ts
	startedAtAny    *time.Time // MIN over every folded contribution (fallback, lead note 7)
	endedAt         *time.Time // MAX over tool.result ts
	decidedAt       rankedTime

	decision, decisionSource, toolSource        rankedValue
	permissionMode, agentID, filePath           rankedValue
	success                                     rankedBoolField
	errorType                                   rankedValue
	durationMS, inputSizeBytes, resultSizeBytes rankedIntField

	eventCount int

	// otelSeen/hookSeen/heuristicSeen are this batch's DELTA evidence for
	// the correlation classification (SPEC §1.6), merged with whatever the
	// row already recorded via field_ranks bits — see upsertToolCalls's SQL
	// for how the four correlation values fall out of
	// (final tool_use_id nullness, merged otelSeen, merged hookSeen, merged
	// heuristicSeen). hookSeen means "a hook contribution that itself
	// carried tool_use_id" (exact join evidence) — never set for a keyless
	// contribution, which is the whole distinction between exact and
	// heuristic.
	otelSeen, hookSeen, heuristicSeen bool
}

func (a *toolCallAgg) fold(c normalize.ToolCallContribution) {
	a.eventCount++
	if a.toolName == "" && c.ToolName != "" {
		a.toolName = c.ToolName
	}
	if a.promptID == nil && c.PromptID != nil {
		a.promptID = c.PromptID
	}
	if c.ToolUseID != nil && *c.ToolUseID != "" {
		a.toolUseID = c.ToolUseID
	}

	if c.Kind == model.KindToolPre || c.Kind == model.KindToolDecision {
		if a.startedAtStrict == nil || c.TS.Before(*a.startedAtStrict) {
			ts := c.TS
			a.startedAtStrict = &ts
		}
	}
	if a.startedAtAny == nil || c.TS.Before(*a.startedAtAny) {
		ts := c.TS
		a.startedAtAny = &ts
	}
	if c.Kind == model.KindToolResult {
		if a.endedAt == nil || c.TS.After(*a.endedAt) {
			ts := c.TS
			a.endedAt = &ts
		}
	}

	dr := decisionRank(c.Source, c.Kind)
	if c.Decision != nil {
		a.decision.offer(*c.Decision, dr, c.TS)
	}
	if c.DecisionSource != nil {
		a.decisionSource.offer(*c.DecisionSource, dr, c.TS)
	}
	if c.ToolSource != nil {
		a.toolSource.offer(*c.ToolSource, dr, c.TS)
	}
	if c.Kind == model.KindToolDecision {
		a.decidedAt.offer(c.TS, dr)
	}

	sr := sourceRank(c.Source)
	if c.PermissionMode != nil {
		a.permissionMode.offer(*c.PermissionMode, sr, c.TS)
	}
	if c.AgentID != nil {
		a.agentID.offer(*c.AgentID, sr, c.TS)
	}
	if c.FilePath != nil {
		a.filePath.offer(*c.FilePath, sr, c.TS)
	}
	if c.ErrorType != nil {
		a.errorType.offer(*c.ErrorType, sr, c.TS)
	}
	a.success.offer(c.Success, sr, c.TS)
	a.durationMS.offer(c.DurationMS, sr, c.TS)
	a.inputSizeBytes.offer(c.InputSizeBytes, sr, c.TS)
	a.resultSizeBytes.offer(c.ResultSizeBytes, sr, c.TS)

	switch {
	case c.Source == model.SourceOTelLog:
		a.otelSeen = true
	case c.Source == model.SourceHook && c.ToolUseID != nil && *c.ToolUseID != "":
		a.hookSeen = true
	}
}

func (a *toolCallAgg) startedAt() time.Time {
	if a.startedAtStrict != nil {
		return *a.startedAtStrict
	}
	return *a.startedAtAny // always set: fold() runs at least once before this is called
}

func promptKeyPart(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// upsertToolCalls is the P2-07 fill-in of write.go's named seam (SPEC
// §1.6, §2.3). Its slot in the lock order is right after events, before
// subagents — unchanged from the seam's placeholder.
func upsertToolCalls(ctx context.Context, tx pgx.Tx, candidates []model.Event) error {
	var keyed, keyless []normalize.ToolCallContribution
	for _, e := range candidates {
		c, ok := normalize.ExtractContribution(e)
		if !ok {
			continue
		}
		if c.ToolUseID != nil && *c.ToolUseID != "" {
			keyed = append(keyed, c)
		} else {
			keyless = append(keyless, c)
		}
	}
	if len(keyed) == 0 && len(keyless) == 0 {
		return nil
	}

	aggs := map[uuid.UUID]*toolCallAgg{}
	getOrCreate := func(id uuid.UUID, sessionID string, promptID, toolUseID *string, toolName string) *toolCallAgg {
		a, ok := aggs[id]
		if !ok {
			a = &toolCallAgg{id: id, sessionID: sessionID, promptID: promptID, toolUseID: toolUseID, toolName: toolName}
			aggs[id] = a
		}
		return a
	}

	for _, c := range keyed {
		id := normalize.ToolCallID(c.SessionID, c.ToolUseID, nil, "", 0)
		getOrCreate(id, c.SessionID, c.PromptID, c.ToolUseID, c.ToolName).fold(c)
	}

	if len(keyless) > 0 {
		if err := resolveKeylessContributions(ctx, tx, keyless, aggs, getOrCreate); err != nil {
			return err
		}
	}

	if err := execToolCallUpsert(ctx, tx, aggs); err != nil {
		return err
	}

	sessionIDs := make(map[string]bool, len(aggs))
	for _, a := range aggs {
		sessionIDs[a.sessionID] = true
	}
	ids := make([]string, 0, len(sessionIDs))
	for id := range sessionIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	q := gen.New(tx)
	if err := q.RecomputeSessionToolCallCounts(ctx, ids); err != nil {
		return fmt.Errorf("postgres: recompute session tool_call_count: %w", err)
	}
	if err := q.RecomputeTurnToolCallCounts(ctx, ids); err != nil {
		return fmt.Errorf("postgres: recompute turn tool_call_count: %w", err)
	}
	return nil
}

// resolveKeylessContributions runs the SPEC §1.6 heuristic: it queries the
// I/O this batch needs (open call candidates, existing keyless-ordinal
// counts — see this file's package doc), then delegates the actual
// one-to-one matching decision to the pure
// normalize.AssignKeylessContributions, and finally folds each keyless
// contribution into its resolved agg.
func resolveKeylessContributions(
	ctx context.Context, tx pgx.Tx,
	keyless []normalize.ToolCallContribution,
	aggs map[uuid.UUID]*toolCallAgg,
	getOrCreate func(id uuid.UUID, sessionID string, promptID, toolUseID *string, toolName string) *toolCallAgg,
) error {
	sessionSet := map[string]bool{}
	for _, c := range keyless {
		sessionSet[c.SessionID] = true
	}
	sessionIDs := make([]string, 0, len(sessionSet))
	for s := range sessionSet {
		sessionIDs = append(sessionIDs, s)
	}
	sort.Strings(sessionIDs)

	q := gen.New(tx)

	dbOpenRows, err := q.GetOpenToolCalls(ctx, sessionIDs)
	if err != nil {
		return fmt.Errorf("postgres: query open tool_calls: %w", err)
	}
	openPool := make([]normalize.OpenCall, 0, len(dbOpenRows))
	dbOpenIDs := map[uuid.UUID]bool{}
	for _, r := range dbOpenRows {
		id, idErr := uuidFromPgtype(r.ID)
		if idErr != nil {
			return fmt.Errorf("postgres: decode open tool_calls id: %w", idErr)
		}
		dbOpenIDs[id] = true
		openPool = append(openPool, normalize.OpenCall{
			ID:          id,
			SessionID:   r.SessionID,
			PromptID:    r.PromptID.String, // "" when !Valid, matching promptOrEmpty semantics
			ToolName:    r.ToolName,
			StartedAt:   r.StartedAt.Time,
			Correlation: model.Correlation(r.Correlation),
		})
	}

	// Batch-local keyed aggs not yet in the database are also valid
	// heuristic-attachment targets (a hook event without tool_use_id and
	// the OTel event that would otherwise correlate it can land in the very
	// same WriteBatch call). A row the DB already returned takes priority —
	// it reflects the fully-merged cross-batch truth this local agg cannot
	// (this local agg only knows about *this* batch's contributions).
	for id, a := range aggs {
		if dbOpenIDs[id] || a.endedAt != nil {
			continue
		}
		corr := model.CorrelationOTelOnly
		switch {
		case a.otelSeen && a.hookSeen:
			corr = model.CorrelationExact
		case !a.otelSeen && a.hookSeen:
			corr = model.CorrelationHookOnly
		}
		openPool = append(openPool, normalize.OpenCall{
			ID: id, SessionID: a.sessionID, PromptID: promptKeyPart(a.promptID),
			ToolName: a.toolName, StartedAt: a.startedAt(), Correlation: corr,
		})
	}

	countRows, err := q.CountKeylessToolCalls(ctx, sessionIDs)
	if err != nil {
		return fmt.Errorf("postgres: count keyless tool_calls: %w", err)
	}
	ordinalNext := make(map[string]int, len(countRows))
	for _, r := range countRows {
		key := r.SessionID + "|" + r.PromptID + "|" + r.ToolName
		ordinalNext[key] = int(r.N)
	}
	nextOrdinal := func(sessionID string, promptID *string, toolName string) int {
		key := sessionID + "|" + promptKeyPart(promptID) + "|" + toolName
		n := ordinalNext[key]
		ordinalNext[key] = n + 1
		return n
	}

	assignments := normalize.AssignKeylessContributions(keyless, openPool, nextOrdinal)
	for i, c := range keyless {
		asg, ok := assignments[i]
		if !ok {
			return fmt.Errorf("postgres: heuristic left contribution %d unassigned", i) // unreachable: AssignKeylessContributions assigns every index
		}
		a := getOrCreate(asg.CallID, c.SessionID, c.PromptID, nil, c.ToolName)
		a.fold(c)
		if asg.Correlation == model.CorrelationHeuristic {
			a.heuristicSeen = true
		}
	}
	return nil
}

func uuidFromPgtype(v pgtype.UUID) (uuid.UUID, error) {
	if !v.Valid {
		return uuid.UUID{}, errors.New("postgres: NULL tool_calls.id")
	}
	return v.Bytes, nil
}

// execToolCallUpsert runs the bulk unnest upsert for every id touched this
// batch, sorted ascending (the lock-ordering invariant, SPEC §1.6:
// "tool_calls (by id)").
func execToolCallUpsert(ctx context.Context, tx pgx.Tx, aggs map[uuid.UUID]*toolCallAgg) error {
	if len(aggs) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(aggs))
	for id := range aggs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })

	n := len(ids)
	idStr := make([]string, n)
	sessionID := make([]string, n)
	promptID := make([]*string, n)
	toolUseID := make([]*string, n)
	toolName := make([]string, n)
	startedAt := make([]time.Time, n)
	endedAt := make([]*time.Time, n)
	decidedAtVal := make([]*time.Time, n)
	decidedAtRank := make([]int, n)

	decisionVal, decisionRankArr := make([]*string, n), make([]int, n)
	decisionSourceVal, decisionSourceRank := make([]*string, n), make([]int, n)
	toolSourceVal, toolSourceRank := make([]*string, n), make([]int, n)
	permissionModeVal, permissionModeRank := make([]*string, n), make([]int, n)
	agentIDVal, agentIDRank := make([]*string, n), make([]int, n)
	filePathVal, filePathRank := make([]*string, n), make([]int, n)
	errorTypeVal, errorTypeRank := make([]*string, n), make([]int, n)
	successVal, successRank := make([]*bool, n), make([]int, n)
	durationVal, durationRank := make([]*int, n), make([]int, n)
	inputSizeVal, inputSizeRank := make([]*int, n), make([]int, n)
	resultSizeVal, resultSizeRank := make([]*int, n), make([]int, n)

	eventCountDelta := make([]int, n)
	otelSeen, hookSeen, heuristicSeen := make([]int, n), make([]int, n), make([]int, n)

	for i, id := range ids {
		a := aggs[id]
		idStr[i] = id.String()
		sessionID[i] = a.sessionID
		promptID[i] = a.promptID
		toolUseID[i] = a.toolUseID
		toolName[i] = a.toolName
		startedAt[i] = a.startedAt()
		endedAt[i] = a.endedAt
		if a.decidedAt.set {
			ts := a.decidedAt.ts
			decidedAtVal[i] = &ts
			decidedAtRank[i] = a.decidedAt.rank
		} else {
			decidedAtRank[i] = -1
		}

		decisionVal[i], decisionRankArr[i] = rvPtr(a.decision)
		decisionSourceVal[i], decisionSourceRank[i] = rvPtr(a.decisionSource)
		toolSourceVal[i], toolSourceRank[i] = rvPtr(a.toolSource)
		permissionModeVal[i], permissionModeRank[i] = rvPtr(a.permissionMode)
		agentIDVal[i], agentIDRank[i] = rvPtr(a.agentID)
		filePathVal[i], filePathRank[i] = rvPtr(a.filePath)
		errorTypeVal[i], errorTypeRank[i] = rvPtr(a.errorType)

		if a.success.set {
			v := a.success.val
			successVal[i] = &v
			successRank[i] = a.success.rank
		} else {
			successRank[i] = -1
		}
		if a.durationMS.set {
			v := a.durationMS.val
			durationVal[i] = &v
			durationRank[i] = a.durationMS.rank
		} else {
			durationRank[i] = -1
		}
		if a.inputSizeBytes.set {
			v := a.inputSizeBytes.val
			inputSizeVal[i] = &v
			inputSizeRank[i] = a.inputSizeBytes.rank
		} else {
			inputSizeRank[i] = -1
		}
		if a.resultSizeBytes.set {
			v := a.resultSizeBytes.val
			resultSizeVal[i] = &v
			resultSizeRank[i] = a.resultSizeBytes.rank
		} else {
			resultSizeRank[i] = -1
		}

		eventCountDelta[i] = a.eventCount
		otelSeen[i] = boolToInt(a.otelSeen)
		hookSeen[i] = boolToInt(a.hookSeen)
		heuristicSeen[i] = boolToInt(a.heuristicSeen)
	}

	_, err := tx.Exec(ctx, toolCallUpsertSQL,
		idStr, sessionID, promptID, toolUseID, toolName,
		startedAt, endedAt, decidedAtVal, decidedAtRank,
		decisionVal, decisionRankArr, decisionSourceVal, decisionSourceRank, toolSourceVal, toolSourceRank,
		permissionModeVal, permissionModeRank, agentIDVal, agentIDRank, filePathVal, filePathRank,
		errorTypeVal, errorTypeRank, successVal, successRank,
		durationVal, durationRank, inputSizeVal, inputSizeRank, resultSizeVal, resultSizeRank,
		eventCountDelta, otelSeen, hookSeen, heuristicSeen,
	)
	if err != nil {
		return fmt.Errorf("postgres: upsert tool_calls: %w", err)
	}
	return nil
}

func rvPtr(r rankedValue) (*string, int) {
	if !r.set {
		return nil, -1
	}
	val := r.val
	return &val, r.rank
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// toolCallUpsertSQL is the tool_calls upsert (SPEC §1.6, §1.5.3, §2.3).
//
// Conflict target (lead note 3): ON CONFLICT (id), not the partial
// tool_calls_use_id_uk index. id is the deterministic UUIDv5 primary key
// and is always non-null, so it is always a valid conflict target — unlike
// tool_calls_use_id_uk, which is `WHERE tool_use_id IS NOT NULL` and
// therefore (a) cannot be inferred by a statement whose incoming row has a
// NULL tool_use_id (verified: Postgres requires the inference predicate be
// satisfied by every candidate row, and a mixed batch of keyed/keyless rows
// in one unnest cannot satisfy a partial predicate uniformly) and (b) is
// redundant here anyway, since two different ids can never legitimately
// share one (session_id, tool_use_id) pair — id is *computed from*
// (session_id, tool_use_id) for a keyed row, so collisions on that index
// would only ever be a Go-side bug the index catches as defence in depth,
// never a normal upsert path.
//
// tool_use_id is written as COALESCE(existing, incoming): a keyless
// contribution attaching to an already-keyed row (heuristic match) always
// carries a nil tool_use_id in its own delta, and must never null out the
// row's real one.
//
// correlation (SPEC §1.6) is computed, not stored as a Go-decided value,
// from the row's FINAL tool_use_id nullness plus the merged otel_seen /
// hook_seen / heuristic_seen bits tracked in field_ranks: this is what lets
// a keyless contribution that heuristically attaches to a row it did not
// create (and therefore does not fully know the history of) still produce
// the right classification, by reading whatever the row already recorded.
const toolCallUpsertSQL = `
INSERT INTO tool_calls (
    id, session_id, prompt_id, tool_use_id, tool_name,
    started_at, ended_at, decided_at,
    decision, decision_source, tool_source, permission_mode, agent_id, file_path,
    error_type, success, duration_ms, input_size_bytes, result_size_bytes,
    correlation, event_count, field_ranks
)
SELECT
    u.id, u.session_id, u.prompt_id, u.tool_use_id, u.tool_name,
    u.started_at, u.ended_at,
    CASE WHEN u.decided_at_rank >= 0 THEN u.decided_at ELSE NULL END,
    u.decision_val, u.decision_source_val, u.tool_source_val, u.permission_mode_val, u.agent_id_val, u.file_path_val,
    u.error_type_val, u.success_val, u.duration_val, u.input_size_val, u.result_size_val,
    CASE WHEN u.tool_use_id IS NOT NULL THEN
        CASE WHEN u.otel_seen = 1 AND u.hook_seen = 1 THEN 'exact'
             WHEN u.otel_seen = 1 AND u.heuristic_seen = 1 THEN 'heuristic'
             WHEN u.otel_seen = 1 THEN 'otel_only'
             ELSE 'hook_only' END
    ELSE 'hook_only' END,
    u.event_count_delta,
    jsonb_build_object(
        'decision', u.decision_rank, 'decision_source', u.decision_source_rank, 'tool_source', u.tool_source_rank,
        'decided_at', u.decided_at_rank,
        'permission_mode', u.permission_mode_rank, 'agent_id', u.agent_id_rank, 'file_path', u.file_path_rank,
        'error_type', u.error_type_rank, 'success', u.success_rank,
        'duration_ms', u.duration_rank, 'input_size_bytes', u.input_size_rank, 'result_size_bytes', u.result_size_rank,
        'otel_seen', u.otel_seen, 'hook_seen', u.hook_seen, 'heuristic_seen', u.heuristic_seen
    )
FROM unnest(
    $1::uuid[], $2::text[], $3::text[], $4::text[], $5::text[],
    $6::timestamptz[], $7::timestamptz[], $8::timestamptz[], $9::int[],
    $10::text[], $11::int[], $12::text[], $13::int[], $14::text[], $15::int[],
    $16::text[], $17::int[], $18::text[], $19::int[], $20::text[], $21::int[],
    $22::text[], $23::int[], $24::bool[], $25::int[],
    $26::int[], $27::int[], $28::int[], $29::int[], $30::int[], $31::int[],
    $32::int[], $33::int[], $34::int[], $35::int[]
) AS u(
    id, session_id, prompt_id, tool_use_id, tool_name,
    started_at, ended_at, decided_at, decided_at_rank,
    decision_val, decision_rank, decision_source_val, decision_source_rank, tool_source_val, tool_source_rank,
    permission_mode_val, permission_mode_rank, agent_id_val, agent_id_rank, file_path_val, file_path_rank,
    error_type_val, error_type_rank, success_val, success_rank,
    duration_val, duration_rank, input_size_val, input_size_rank, result_size_val, result_size_rank,
    event_count_delta, otel_seen, hook_seen, heuristic_seen
)
ORDER BY u.id
ON CONFLICT (id) DO UPDATE SET
    tool_use_id = COALESCE(tool_calls.tool_use_id, EXCLUDED.tool_use_id),
    tool_name = CASE WHEN tool_calls.tool_name = '' THEN EXCLUDED.tool_name ELSE tool_calls.tool_name END,
    prompt_id = COALESCE(tool_calls.prompt_id, EXCLUDED.prompt_id),
    started_at = LEAST(tool_calls.started_at, EXCLUDED.started_at),
    ended_at = CASE WHEN tool_calls.ended_at IS NULL THEN EXCLUDED.ended_at
                    WHEN EXCLUDED.ended_at IS NULL THEN tool_calls.ended_at
                    ELSE GREATEST(tool_calls.ended_at, EXCLUDED.ended_at) END,
    decided_at = CASE WHEN (EXCLUDED.field_ranks->>'decided_at')::int >= COALESCE((tool_calls.field_ranks->>'decided_at')::int, -1)
                      THEN COALESCE(EXCLUDED.decided_at, tool_calls.decided_at)
                      ELSE tool_calls.decided_at END,
    decision = CASE WHEN (EXCLUDED.field_ranks->>'decision')::int >= COALESCE((tool_calls.field_ranks->>'decision')::int, -1) AND EXCLUDED.decision IS NOT NULL THEN EXCLUDED.decision ELSE tool_calls.decision END,
    decision_source = CASE WHEN (EXCLUDED.field_ranks->>'decision_source')::int >= COALESCE((tool_calls.field_ranks->>'decision_source')::int, -1) AND EXCLUDED.decision_source IS NOT NULL THEN EXCLUDED.decision_source ELSE tool_calls.decision_source END,
    tool_source = CASE WHEN (EXCLUDED.field_ranks->>'tool_source')::int >= COALESCE((tool_calls.field_ranks->>'tool_source')::int, -1) AND EXCLUDED.tool_source IS NOT NULL THEN EXCLUDED.tool_source ELSE tool_calls.tool_source END,
    permission_mode = CASE WHEN (EXCLUDED.field_ranks->>'permission_mode')::int >= COALESCE((tool_calls.field_ranks->>'permission_mode')::int, -1) AND EXCLUDED.permission_mode IS NOT NULL THEN EXCLUDED.permission_mode ELSE tool_calls.permission_mode END,
    agent_id = CASE WHEN (EXCLUDED.field_ranks->>'agent_id')::int >= COALESCE((tool_calls.field_ranks->>'agent_id')::int, -1) AND EXCLUDED.agent_id IS NOT NULL THEN EXCLUDED.agent_id ELSE tool_calls.agent_id END,
    file_path = CASE WHEN (EXCLUDED.field_ranks->>'file_path')::int >= COALESCE((tool_calls.field_ranks->>'file_path')::int, -1) AND EXCLUDED.file_path IS NOT NULL THEN EXCLUDED.file_path ELSE tool_calls.file_path END,
    error_type = CASE WHEN (EXCLUDED.field_ranks->>'error_type')::int >= COALESCE((tool_calls.field_ranks->>'error_type')::int, -1) AND EXCLUDED.error_type IS NOT NULL THEN EXCLUDED.error_type ELSE tool_calls.error_type END,
    success = CASE WHEN (EXCLUDED.field_ranks->>'success')::int >= COALESCE((tool_calls.field_ranks->>'success')::int, -1) AND EXCLUDED.success IS NOT NULL THEN EXCLUDED.success ELSE tool_calls.success END,
    duration_ms = CASE WHEN (EXCLUDED.field_ranks->>'duration_ms')::int >= COALESCE((tool_calls.field_ranks->>'duration_ms')::int, -1) AND EXCLUDED.duration_ms IS NOT NULL THEN EXCLUDED.duration_ms ELSE tool_calls.duration_ms END,
    input_size_bytes = CASE WHEN (EXCLUDED.field_ranks->>'input_size_bytes')::int >= COALESCE((tool_calls.field_ranks->>'input_size_bytes')::int, -1) AND EXCLUDED.input_size_bytes IS NOT NULL THEN EXCLUDED.input_size_bytes ELSE tool_calls.input_size_bytes END,
    result_size_bytes = CASE WHEN (EXCLUDED.field_ranks->>'result_size_bytes')::int >= COALESCE((tool_calls.field_ranks->>'result_size_bytes')::int, -1) AND EXCLUDED.result_size_bytes IS NOT NULL THEN EXCLUDED.result_size_bytes ELSE tool_calls.result_size_bytes END,
    event_count = tool_calls.event_count + EXCLUDED.event_count,
    field_ranks = tool_calls.field_ranks || jsonb_build_object(
        'decision', GREATEST(COALESCE((tool_calls.field_ranks->>'decision')::int, -1), (EXCLUDED.field_ranks->>'decision')::int),
        'decision_source', GREATEST(COALESCE((tool_calls.field_ranks->>'decision_source')::int, -1), (EXCLUDED.field_ranks->>'decision_source')::int),
        'tool_source', GREATEST(COALESCE((tool_calls.field_ranks->>'tool_source')::int, -1), (EXCLUDED.field_ranks->>'tool_source')::int),
        'decided_at', GREATEST(COALESCE((tool_calls.field_ranks->>'decided_at')::int, -1), (EXCLUDED.field_ranks->>'decided_at')::int),
        'permission_mode', GREATEST(COALESCE((tool_calls.field_ranks->>'permission_mode')::int, -1), (EXCLUDED.field_ranks->>'permission_mode')::int),
        'agent_id', GREATEST(COALESCE((tool_calls.field_ranks->>'agent_id')::int, -1), (EXCLUDED.field_ranks->>'agent_id')::int),
        'file_path', GREATEST(COALESCE((tool_calls.field_ranks->>'file_path')::int, -1), (EXCLUDED.field_ranks->>'file_path')::int),
        'error_type', GREATEST(COALESCE((tool_calls.field_ranks->>'error_type')::int, -1), (EXCLUDED.field_ranks->>'error_type')::int),
        'success', GREATEST(COALESCE((tool_calls.field_ranks->>'success')::int, -1), (EXCLUDED.field_ranks->>'success')::int),
        'duration_ms', GREATEST(COALESCE((tool_calls.field_ranks->>'duration_ms')::int, -1), (EXCLUDED.field_ranks->>'duration_ms')::int),
        'input_size_bytes', GREATEST(COALESCE((tool_calls.field_ranks->>'input_size_bytes')::int, -1), (EXCLUDED.field_ranks->>'input_size_bytes')::int),
        'result_size_bytes', GREATEST(COALESCE((tool_calls.field_ranks->>'result_size_bytes')::int, -1), (EXCLUDED.field_ranks->>'result_size_bytes')::int),
        'otel_seen', GREATEST(COALESCE((tool_calls.field_ranks->>'otel_seen')::int, 0), (EXCLUDED.field_ranks->>'otel_seen')::int),
        'hook_seen', GREATEST(COALESCE((tool_calls.field_ranks->>'hook_seen')::int, 0), (EXCLUDED.field_ranks->>'hook_seen')::int),
        'heuristic_seen', GREATEST(COALESCE((tool_calls.field_ranks->>'heuristic_seen')::int, 0), (EXCLUDED.field_ranks->>'heuristic_seen')::int)
    ),
    correlation = CASE WHEN COALESCE(tool_calls.tool_use_id, EXCLUDED.tool_use_id) IS NOT NULL THEN
        CASE WHEN GREATEST(COALESCE((tool_calls.field_ranks->>'otel_seen')::int,0),(EXCLUDED.field_ranks->>'otel_seen')::int) = 1
                  AND GREATEST(COALESCE((tool_calls.field_ranks->>'hook_seen')::int,0),(EXCLUDED.field_ranks->>'hook_seen')::int) = 1 THEN 'exact'
             WHEN GREATEST(COALESCE((tool_calls.field_ranks->>'otel_seen')::int,0),(EXCLUDED.field_ranks->>'otel_seen')::int) = 1
                  AND GREATEST(COALESCE((tool_calls.field_ranks->>'heuristic_seen')::int,0),(EXCLUDED.field_ranks->>'heuristic_seen')::int) = 1 THEN 'heuristic'
             WHEN GREATEST(COALESCE((tool_calls.field_ranks->>'otel_seen')::int,0),(EXCLUDED.field_ranks->>'otel_seen')::int) = 1 THEN 'otel_only'
             ELSE 'hook_only' END
    ELSE 'hook_only' END
`
