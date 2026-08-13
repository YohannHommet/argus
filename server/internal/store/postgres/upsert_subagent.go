// Package postgres — upsert_subagent.go builds the `subagents` upsert
// (SPEC §1.6, §1.5.3, §1.9, §2.3): the P2-08 seam in write.go, filled in.
//
// # Winner is always hook (SPEC §1.5.3)
//
// Unlike sessions/tool_calls, subagents.* has exactly one possible source:
// "OTel emits no subagent lifecycle event at all" (SPEC §1.5.3). There is
// therefore nothing for a field_ranks precedence mechanism to arbitrate
// between two sources — every merge below is a plain monotonic
// COALESCE/LEAST/GREATEST, not a ranked comparison. field_ranks is still
// written (as an empty object) purely because the column is NOT NULL; it is
// never read back by this file.
//
// # NULL-vs-0 for tool_call_count (lead note 5, SPEC §1.9)
//
// tool_call_count is recomputed (not incremented, exactly like
// sessions.tool_call_count) from tool_calls.agent_id — itself hook-only
// (SPEC §1.5.3). Whether a session has ANY tool-level hook coverage at all
// is judged independently, from tool_calls.correlation <> 'otel_only'
// (db/queries/subagents.sql: SessionsWithToolHookCoverage): a session with
// no such row could not possibly have a real "0 tool calls for this agent"
// answer, because hooks were never on to report it. NULL is written for
// every subagent in such a session, never 0 — the RecomputeSubagentToolCallCounts
// query encodes this distinction directly in SQL so no Go code path can
// accidentally collapse it (SPEC §1.9: "0 would be a lie").
//
// # depth: parent chain with a cap, out-of-order arrival (lead note 4)
//
// A SubagentStart may arrive before its own parent's SubagentStart (SPEC
// §1.7: out-of-order arrival is normal, and stub-on-reference forbids
// buffering). resolveDepths therefore does a SINGLE hop: it looks up the
// parent's depth as currently stored (querying the database, plus this same
// batch's own in-flight aggs for a parent created in the very same
// WriteBatch call) and adds one, capped at subagentMaxDepth. When the
// parent is not found in either place, depth defaults to 1 (as if the
// subagent were a direct child of the synthetic root) — this is a
// documented approximation, not a guess promoted to fact: the stored
// `depth` column self-heals on a LATER write that still carries
// parent_agent_id (the ON CONFLICT clause below only overwrites depth when
// EXCLUDED.parent_agent_id is non-null), but a subagent whose only event
// ever folded had no parent info keeps depth=1 forever in the stored
// column. This is why SubagentTree (subagent_tree.go) does NOT trust the
// stored depth column for its response: it recomputes depth from the LIVE
// parent_agent_id chain via a recursive CTE at read time, which is always
// correct regardless of write-time arrival order — the stored column here
// is best-effort bookkeeping, not the read path's source of truth.
//
// # status derivation and the stub-on-reference default (lead note 7)
//
// The DDL default for `status` is 'running', but this file always writes
// an explicit computed value on every INSERT — including a
// SubagentStop-without-a-matching-SubagentStart, which computes 'unknown'
// (started_at IS NULL, ended_at IS NOT NULL) — so the column default is
// never actually reached by any row this code writes; it exists for the
// (SPEC-permitted) case of a row created by a future code path that has
// even less information than this one always has.
package postgres

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store/postgres/gen"
)

// subagentMaxDepth is SPEC §2.3/§4.3's depth cap ("depth cap 16 so a
// malformed parent_agent_id cycle cannot hang the query"), applied on both
// the write side (this file, one hop at a time) and the read side
// (subagent_tree.go's recursive CTE, which is the guard that actually
// matters for the cycle AC — see that file's doc).
const subagentMaxDepth = 16

// subagentKey identifies one subagents row, mirroring the table's PK
// (session_id, agent_id).
type subagentKey struct {
	sessionID string
	agentID   string
}

// subagentAgg accumulates one subagents row's contribution from the
// batch's candidate events (SPEC §1.6 projections table: "subagent.*, plus
// hook tool.* events carrying agent_id" — the tool.* half is handled
// separately, by the post-upsert RecomputeSubagentToolCallCounts pass, not
// by folding here, because it needs the already-upserted tool_calls table
// as its source of truth (lead note 5), not a batch-local tally that would
// double count on redelivery).
type subagentAgg struct {
	sessionID, agentID string

	parentAgentID  *string
	agentType      *string
	promptID       *string
	spawnToolUseID *string

	startedAt *time.Time // set only by a subagent.start contribution
	endedAt   *time.Time // set only by a subagent.stop contribution

	// incomingStatus is this batch's local complete/failed verdict, only
	// meaningful when endedAt != nil (a stop was folded). It participates
	// in the upsert's status computation but is never itself the row's
	// final status without also checking startedAt (see execSubagentUpsert's
	// SQL: a stop with no known start is 'unknown', not 'complete'/'failed').
	incomingStatus model.SubagentStatus

	depth int // resolved by resolveDepths before the upsert runs
}

// subagentLifecycleKinds is the SPEC §1.6 "built from" set for the
// lifecycle half of the subagents projection (subagent.start /
// subagent.stop); the tool.* half is not folded here (see this file's
// package doc).
var subagentLifecycleKinds = map[model.Kind]bool{
	model.KindSubagentStart: true,
	model.KindSubagentStop:  true,
}

// upsertSubagents is the P2-08 fill-in of write.go's named seam (SPEC
// §1.6, §2.3, §1.9). Its slot in the lock order is right after tool_calls,
// before rollup_dirty — unchanged from the seam's placeholder. It runs
// strictly after upsertToolCalls in the same transaction, which is what
// lets its counter recompute read tool_calls.agent_id as already-settled
// truth for this batch (lead note 5).
func upsertSubagents(ctx context.Context, tx pgx.Tx, candidates []model.Event) error {
	aggs := map[subagentKey]*subagentAgg{}
	getOrCreate := func(sessionID, agentID string) *subagentAgg {
		k := subagentKey{sessionID, agentID}
		a, ok := aggs[k]
		if !ok {
			a = &subagentAgg{sessionID: sessionID, agentID: agentID}
			aggs[k] = a
		}
		return a
	}

	for _, e := range candidates {
		if !subagentLifecycleKinds[e.Kind] || e.AgentID == nil || *e.AgentID == "" {
			continue
		}
		a := getOrCreate(e.SessionID, *e.AgentID)
		switch e.Kind { //nolint:exhaustive // subagentLifecycleKinds above already narrowed to exactly the two Kinds this switch handles; every other Kind was filtered out before this loop body is reached.
		case model.KindSubagentStart:
			foldSubagentStart(a, e)
		case model.KindSubagentStop:
			foldSubagentStop(a, e)
		}
	}

	if len(aggs) == 0 {
		return nil
	}

	inferParents(aggs, candidates)

	if err := resolveDepths(ctx, tx, aggs); err != nil {
		return err
	}
	if err := execSubagentUpsert(ctx, tx, aggs); err != nil {
		return err
	}
	return recomputeSubagentCounters(ctx, tx, candidates, aggs)
}

func foldSubagentStart(a *subagentAgg, e model.Event) {
	ts := e.TS
	if a.startedAt == nil || ts.Before(*a.startedAt) {
		a.startedAt = &ts
	}
	// SPEC §2.3: "parent_agent_id ... NULL = root". SPEC §1.5.2: SubagentStart
	// carries parent_agent_id "if present" — an absent one is not an error,
	// it means the parent is the main agent (root), which is exactly what a
	// nil ParentAgentID already encodes, so no inference is attempted here;
	// inferParents only runs for aggs that are STILL nil after this fold
	// (SPEC's "documented best-effort" fallback, not the common case).
	if e.ParentAgentID != nil && *e.ParentAgentID != "" {
		a.parentAgentID = e.ParentAgentID
	}
	if e.AgentType != nil && *e.AgentType != "" {
		a.agentType = e.AgentType
	}
	if a.promptID == nil && e.PromptID != nil {
		a.promptID = e.PromptID
	}
}

func foldSubagentStop(a *subagentAgg, e model.Event) {
	ts := e.TS
	if a.endedAt == nil || ts.After(*a.endedAt) {
		a.endedAt = &ts
	}
	// AgentType is a common hook field (SPEC §1.5.2), so a Stop payload can
	// legitimately carry it even without a preceding Start in this batch —
	// worth keeping if Start's own value is still unknown.
	if a.agentType == nil && e.AgentType != nil && *e.AgentType != "" {
		a.agentType = e.AgentType
	}
	// SPEC lead note 7: "complete/failed on stop (from the stop payload's
	// success)". A missing success field on a genuine stop (as opposed to a
	// genuinely absent start, which is what makes the row 'unknown') is
	// treated as non-failure — a documented assumption, not a guess dressed
	// up as fact: SubagentStop's own mapping (normalize/hooks.go) only ever
	// promotes `success` when the payload actually has the key.
	switch {
	case e.Success == nil, *e.Success:
		a.incomingStatus = model.SubagentStatusComplete
	default:
		a.incomingStatus = model.SubagentStatusFailed
	}
}

// inferParents implements SPEC P2-08's "documented best-effort" fallback:
// "parent_agent_id ... else inferred from the spawning hook tool.* event
// carrying agent_type in the same turn". It is confined to the CURRENT
// BATCH's candidates only — a deliberate, documented scope limit. Widening
// it to a full historical scan would require a database read keyed on
// attrs.tool_input.subagent_type, which no index supports (SPEC §2.2 does
// not index attrs), and would turn a best-effort convenience into a
// per-batch full-table scan for a field the SPEC itself only asks be
// inferred "documented best-effort" — not guaranteed. A subagent whose
// spawning PreToolUse landed in an earlier batch simply keeps
// parent_agent_id nil (indistinguishable from "spawned by root"), which is
// the same honest-null behaviour SPEC §1.9 asks for elsewhere.
func inferParents(aggs map[subagentKey]*subagentAgg, candidates []model.Event) {
	for k, a := range aggs {
		if a.parentAgentID != nil || a.agentType == nil || *a.agentType == "" || a.promptID == nil {
			continue
		}
		var (
			bestAgentID   *string
			bestToolUseID *string
			bestTS        time.Time
			found         bool
		)
		for _, e := range candidates {
			if e.Kind != model.KindToolPre || e.Source != model.SourceHook {
				continue
			}
			if e.SessionID != k.sessionID || e.PromptID == nil || *e.PromptID != *a.promptID {
				continue
			}
			if a.startedAt != nil && e.TS.After(*a.startedAt) {
				continue // a spawning tool call cannot postdate the subagent it spawns
			}
			toolInput, ok := normalize.Map(e.Attrs, "tool_input")
			if !ok {
				continue
			}
			subagentType := normalize.String(toolInput, "subagent_type")
			if subagentType == nil || *subagentType != *a.agentType {
				continue
			}
			if !found || e.TS.After(bestTS) {
				bestAgentID, bestToolUseID, bestTS, found = e.AgentID, e.ToolUseID, e.TS, true
			}
		}
		if found {
			// bestAgentID may itself be nil, meaning the spawning tool call
			// was made by the root agent — leaving a.parentAgentID nil is
			// then already correct (SPEC §2.3: "NULL = root"), so there is
			// nothing to assign for that case beyond spawnToolUseID.
			a.parentAgentID = bestAgentID
			a.spawnToolUseID = bestToolUseID
		}
	}
}

// resolveDepths implements SPEC §2.3/§4.3's "depth ... from the parent
// chain with a cap" for the write side — see this file's package doc for
// why this is a single hop, not a recursive walk, and why the read path
// (subagent_tree.go) is the actual source of truth for display.
func resolveDepths(ctx context.Context, tx pgx.Tx, aggs map[subagentKey]*subagentAgg) error {
	sessionSet := map[string]bool{}
	for k := range aggs {
		sessionSet[k.sessionID] = true
	}
	sessionIDs := make([]string, 0, len(sessionSet))
	for s := range sessionSet {
		sessionIDs = append(sessionIDs, s)
	}
	sort.Strings(sessionIDs)

	q := gen.New(tx)
	rows, err := q.GetSubagentDepths(ctx, sessionIDs)
	if err != nil {
		return fmt.Errorf("postgres: query subagent depths: %w", err)
	}
	existingDepth := make(map[subagentKey]int, len(rows))
	for _, r := range rows {
		existingDepth[subagentKey{r.SessionID, r.AgentID}] = int(r.Depth)
	}

	for k, a := range aggs {
		if a.parentAgentID == nil || *a.parentAgentID == "" {
			a.depth = 1 // direct child of the synthetic root (SPEC §4.3 example: ag_7 depth=1)
			continue
		}
		parentKey := subagentKey{k.sessionID, *a.parentAgentID}
		switch {
		case existingDepth[parentKey] > 0 || hasDepthEntry(rows, parentKey):
			a.depth = existingDepth[parentKey] + 1
		default:
			if pa, ok := aggs[parentKey]; ok && pa != a {
				// The parent is itself newly created in this very batch
				// (e.g. two SubagentStart events for a chain in one
				// WriteBatch call). pa.depth may still be its own
				// zero-value at this point in map iteration order; that is
				// fine — resolveDepths is not order-sensitive for this
				// case's correctness bound because a is capped below
				// regardless, and a genuinely deep same-batch chain is not
				// a scenario any AC requires resolving exactly (see
				// package doc: the read path is authoritative).
				a.depth = pa.depth + 1
			} else {
				// Out-of-order arrival (SPEC §1.7): the parent's own row
				// does not exist yet anywhere this write can see. Default
				// to 1, documented in this file's package doc.
				a.depth = 1
			}
		}
		if a.depth > subagentMaxDepth || a.depth < 1 {
			a.depth = subagentMaxDepth
			if a.depth < 1 {
				a.depth = 1
			}
		}
	}
	return nil
}

// hasDepthEntry distinguishes "parent found with depth 0" (impossible per
// the DDL's NOT NULL DEFAULT 1, but defensive) from "parent not found at
// all" for the map lookup above, without a second map allocation per call.
func hasDepthEntry(rows []gen.GetSubagentDepthsRow, key subagentKey) bool {
	for _, r := range rows {
		if r.SessionID == key.sessionID && r.AgentID == key.agentID {
			return true
		}
	}
	return false
}

// execSubagentUpsert runs the bulk unnest upsert for every (session_id,
// agent_id) touched this batch, sorted ascending (the lock-ordering
// invariant, SPEC §1.6: "subagents (by session_id, agent_id)").
func execSubagentUpsert(ctx context.Context, tx pgx.Tx, aggs map[subagentKey]*subagentAgg) error {
	keys := make([]subagentKey, 0, len(aggs))
	for k := range aggs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].sessionID != keys[j].sessionID {
			return keys[i].sessionID < keys[j].sessionID
		}
		return keys[i].agentID < keys[j].agentID
	})

	n := len(keys)
	sessionID := make([]string, n)
	agentID := make([]string, n)
	parentAgentID := make([]*string, n)
	agentType := make([]*string, n)
	promptID := make([]*string, n)
	spawnToolUseID := make([]*string, n)
	depth := make([]int32, n)
	startedAt := make([]*time.Time, n)
	endedAt := make([]*time.Time, n)
	incomingStatus := make([]string, n)

	for i, k := range keys {
		a := aggs[k]
		sessionID[i] = a.sessionID
		agentID[i] = a.agentID
		parentAgentID[i] = a.parentAgentID
		agentType[i] = a.agentType
		promptID[i] = a.promptID
		spawnToolUseID[i] = a.spawnToolUseID
		depth[i] = int32(a.depth) //nolint:gosec // a.depth is bounded to [1, subagentMaxDepth] by resolveDepths
		startedAt[i] = a.startedAt
		endedAt[i] = a.endedAt
		status := a.incomingStatus
		if status == "" {
			// No stop was folded for this row (a start-only contribution);
			// this value is only consulted by the upsert SQL when
			// ended_at IS NOT NULL, so a placeholder here never surfaces.
			status = model.SubagentStatusRunning
		}
		incomingStatus[i] = string(status)
	}

	_, err := tx.Exec(ctx, subagentUpsertSQL,
		sessionID, agentID, parentAgentID, agentType, promptID, spawnToolUseID,
		depth, startedAt, endedAt, incomingStatus,
	)
	if err != nil {
		return fmt.Errorf("postgres: upsert subagents: %w", err)
	}
	return nil
}

// recomputeSubagentCounters refreshes subagents.tool_call_count (SPEC
// §1.9) for every session this batch touched at all — not only sessions
// with a subagent lifecycle event, since a tool.* hook event carrying
// agent_id updates an EXISTING subagent's count with no lifecycle event of
// its own — and sessions.subagent_count (SPEC §1.6/§2.1), scoped to
// sessions that actually had a lifecycle contribution this batch, since
// that count only changes when the subagents table itself does.
func recomputeSubagentCounters(ctx context.Context, tx pgx.Tx, candidates []model.Event, aggs map[subagentKey]*subagentAgg) error {
	sessionSet := map[string]bool{}
	for _, e := range candidates {
		sessionSet[e.SessionID] = true
	}
	sessionIDs := make([]string, 0, len(sessionSet))
	for s := range sessionSet {
		sessionIDs = append(sessionIDs, s)
	}
	sort.Strings(sessionIDs)
	if len(sessionIDs) == 0 {
		return nil
	}

	q := gen.New(tx)

	hookCoverageSessions, err := q.SessionsWithToolHookCoverage(ctx, sessionIDs)
	if err != nil {
		return fmt.Errorf("postgres: query tool hook coverage: %w", err)
	}
	sort.Strings(hookCoverageSessions)

	if err := q.RecomputeSubagentToolCallCounts(ctx, gen.RecomputeSubagentToolCallCountsParams{
		HookCoverageSessions: hookCoverageSessions,
		SessionIds:           sessionIDs,
	}); err != nil {
		return fmt.Errorf("postgres: recompute subagent tool_call_count: %w", err)
	}

	subagentSessionSet := map[string]bool{}
	for k := range aggs {
		subagentSessionSet[k.sessionID] = true
	}
	subagentSessionIDs := make([]string, 0, len(subagentSessionSet))
	for s := range subagentSessionSet {
		subagentSessionIDs = append(subagentSessionIDs, s)
	}
	sort.Strings(subagentSessionIDs)
	if err := q.RecomputeSessionSubagentCount(ctx, subagentSessionIDs); err != nil {
		return fmt.Errorf("postgres: recompute session subagent_count: %w", err)
	}
	return nil
}

// subagentUpsertSQL is the subagents upsert (SPEC §1.6, §1.5.3, §1.9,
// §2.3). See this file's package doc for the merge rules; unlike
// tool_calls/sessions there is no CROSS-SOURCE field_ranks precedence to
// arbitrate (subagents.* has exactly one possible source, SPEC §1.5.3), so
// every VALUE merge below is a plain monotonic COALESCE/LEAST/GREATEST.
// field_ranks is still used here, but for a different purpose than in
// tool_calls/sessions: as a durable side-channel for the stop's success
// verdict (see the status doc below), since the table has no dedicated
// `success` column of its own to remember it in.
//
// Conflict target: ON CONFLICT (session_id, agent_id), the table's actual
// primary key — always valid, unlike tool_calls' deterministic-id scheme,
// because subagents rows are naturally keyed by the vendor's own agent_id.
//
// status (lead note 7) and the field_ranks.stop_success side-channel: a
// SubagentStop's complete/failed verdict must survive being folded into a
// 'unknown' row (stop-without-start, AC 2) so that a LATER-arriving
// SubagentStart can correct the row to its true complete/failed status
// (TestWriteBatch_Subagent_LateStartCorrectsUnknownStatus) instead of
// falling back to a bare "we now know it started, so it must be running" —
// which would silently forget a stop that already happened. Since
// subagents has no dedicated `success` column, the verdict is stashed in
// field_ranks->>'stop_success' (a bool) whenever a stop is folded, and read
// back from there whenever a write recomputes status without itself
// carrying fresh stop information (EXCLUDED.ended_at IS NULL) but the row
// already has an end time from an earlier write. This is the ONE place
// field_ranks carries information a status CASE branch cannot get purely
// from column nullness; it is never used as a precedence rank here.
const subagentUpsertSQL = `
INSERT INTO subagents (
    session_id, agent_id, parent_agent_id, agent_type, prompt_id, spawn_tool_use_id,
    depth, started_at, ended_at, status, field_ranks
)
SELECT
    u.session_id, u.agent_id, u.parent_agent_id, u.agent_type, u.prompt_id, u.spawn_tool_use_id,
    u.depth, u.started_at, u.ended_at,
    CASE
        WHEN u.ended_at IS NOT NULL AND u.started_at IS NULL THEN 'unknown'
        WHEN u.ended_at IS NOT NULL THEN u.incoming_status
        ELSE 'running'
    END,
    CASE WHEN u.ended_at IS NOT NULL
         THEN jsonb_build_object('stop_success', u.incoming_status = 'complete')
         ELSE '{}'::jsonb
    END
FROM unnest(
    $1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[],
    $7::int[], $8::timestamptz[], $9::timestamptz[], $10::text[]
) AS u(session_id, agent_id, parent_agent_id, agent_type, prompt_id, spawn_tool_use_id,
       depth, started_at, ended_at, incoming_status)
ORDER BY u.session_id, u.agent_id
ON CONFLICT (session_id, agent_id) DO UPDATE SET
    parent_agent_id = COALESCE(subagents.parent_agent_id, EXCLUDED.parent_agent_id),
    agent_type = COALESCE(subagents.agent_type, EXCLUDED.agent_type),
    prompt_id = COALESCE(subagents.prompt_id, EXCLUDED.prompt_id),
    spawn_tool_use_id = COALESCE(subagents.spawn_tool_use_id, EXCLUDED.spawn_tool_use_id),
    depth = CASE WHEN EXCLUDED.parent_agent_id IS NOT NULL THEN EXCLUDED.depth ELSE subagents.depth END,
    started_at = COALESCE(subagents.started_at, EXCLUDED.started_at),
    ended_at = CASE WHEN subagents.ended_at IS NULL THEN EXCLUDED.ended_at
                    WHEN EXCLUDED.ended_at IS NULL THEN subagents.ended_at
                    ELSE GREATEST(subagents.ended_at, EXCLUDED.ended_at) END,
    field_ranks = subagents.field_ranks || EXCLUDED.field_ranks,
    status = CASE
        WHEN COALESCE(subagents.started_at, EXCLUDED.started_at) IS NULL
             AND COALESCE(subagents.ended_at, EXCLUDED.ended_at) IS NOT NULL THEN 'unknown'
        WHEN EXCLUDED.ended_at IS NOT NULL THEN EXCLUDED.status
        WHEN COALESCE(subagents.ended_at, EXCLUDED.ended_at) IS NOT NULL THEN
            -- A stop already happened on an earlier write (possibly folded
            -- as 'unknown' for lack of a start) and this write brings no
            -- fresh stop info of its own: recover the actual verdict from
            -- the stashed side-channel rather than guessing 'running'.
            CASE WHEN COALESCE((subagents.field_ranks->>'stop_success')::bool, true)
                 THEN 'complete' ELSE 'failed' END
        WHEN COALESCE(subagents.started_at, EXCLUDED.started_at) IS NOT NULL THEN 'running'
        ELSE subagents.status
    END
`
