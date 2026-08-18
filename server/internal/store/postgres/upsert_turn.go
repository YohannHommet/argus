// Package postgres — upsert_turn.go builds the `turns` stub-on-reference
// upsert (SPEC §1.7 rule 1, §1.6, §2.1): any candidate event carrying a
// prompt_id creates or touches a turn row, but cost/tokens are aggregated
// only from llm.request events (SPEC §1.5.3: "turns.* cost/tokens always
// aggregated from llm.request events only, never from hooks").
//
// turns.tool_call_count, tool_reject_count, and error_count are left at
// their DEFAULT 0 here — deliberately: SPEC's lead decision for this ticket
// reserves those counters for P2-07 (tool_calls) and P2-08 (subagents),
// which own the events those counts are derived from. Maintaining them here
// would be a half-implementation this ticket was explicitly told not to do.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/pricing"
)

// turnKey identifies one turns row (SPEC §2.1 PRIMARY KEY (session_id, prompt_id)).
type turnKey struct {
	SessionID, PromptID string
}

// turnAgg accumulates one turn's contribution from the batch's candidate
// events.
type turnAgg struct {
	key                                               turnKey
	firstSeen, lastEvent                              time.Time
	startedAt, endedAt                                *time.Time
	apiRequestCount                                   int
	inputTokens, outputTokens, cacheRead, cacheCreate int64
	costUSD, costEstimatedUSD                         float64
	models                                            map[string]struct{}
	sawEndSuccess                                     *bool
}

func newTurnAgg(key turnKey) *turnAgg {
	return &turnAgg{key: key, models: map[string]struct{}{}}
}

// foldTurnEvents groups persisted candidate events carrying a non-nil
// PromptID by (session_id, prompt_id) — stub-on-reference (SPEC §1.7 rule
// 1): "the same for turns when prompt_id is present." prices is WriteBatch's
// SPEC §2.4 price table for this transaction — nil when no candidate needs
// it (see write.go's doc on why loading it is conditional).
func foldTurnEvents(candidates []model.Event, prices []pricing.Price) map[turnKey]*turnAgg {
	out := map[turnKey]*turnAgg{}
	for _, e := range candidates {
		if e.PromptID == nil || *e.PromptID == "" {
			continue
		}
		key := turnKey{SessionID: e.SessionID, PromptID: *e.PromptID}
		agg, ok := out[key]
		if !ok {
			agg = newTurnAgg(key)
			agg.firstSeen, agg.lastEvent = e.TS, e.TS
			out[key] = agg
		}
		agg.foldEvent(e, prices)
	}
	return out
}

func (a *turnAgg) foldEvent(e model.Event, prices []pricing.Price) {
	if e.TS.Before(a.firstSeen) {
		a.firstSeen = e.TS
	}
	if e.TS.After(a.lastEvent) {
		a.lastEvent = e.TS
	}

	switch e.Kind { //nolint:exhaustive // only turn.start/turn.end/llm.request feed the turns projection (SPEC §1.6, §1.5.3); every other Kind contributes only the common first_seen/last_event folded above.
	case model.KindTurnStart:
		if a.startedAt == nil || e.TS.Before(*a.startedAt) {
			ts := e.TS
			a.startedAt = &ts
		}
	case model.KindTurnEnd:
		if a.endedAt == nil || e.TS.After(*a.endedAt) {
			ts := e.TS
			a.endedAt = &ts
		}
		if e.Success != nil {
			ok := *e.Success
			a.sawEndSuccess = &ok
		}
	case model.KindLLMRequest:
		a.apiRequestCount++
		if e.InputTokens != nil {
			a.inputTokens += *e.InputTokens
		}
		if e.OutputTokens != nil {
			a.outputTokens += *e.OutputTokens
		}
		if e.CacheReadTokens != nil {
			a.cacheRead += *e.CacheReadTokens
		}
		if e.CacheCreationTokens != nil {
			a.cacheCreate += *e.CacheCreationTokens
		}
		// D-30 (docs/review/phase-4-gauntlet.md, owner-ratified 2026-08-18):
		// branch on e.CostUSD, not e.CostSource — see upsert_session.go's
		// identical fold for the full reasoning (this is its exact twin,
		// per this file's package doc).
		switch {
		case e.CostUSD != nil:
			a.costUSD += *e.CostUSD
		case e.Model != nil && *e.Model != "":
			tokens := costTokens{}
			if e.InputTokens != nil {
				tokens.input = *e.InputTokens
			}
			if e.OutputTokens != nil {
				tokens.output = *e.OutputTokens
			}
			if e.CacheReadTokens != nil {
				tokens.cacheRead = *e.CacheReadTokens
			}
			if e.CacheCreationTokens != nil {
				tokens.cacheWrite = *e.CacheCreationTokens
			}
			if usd, ok := estimateCost(prices, *e.Model, tokens, e.TS); ok {
				a.costEstimatedUSD += usd
			}
		}
		if e.Model != nil && *e.Model != "" {
			a.models[*e.Model] = struct{}{}
		}
	}
}

// upsertTurns runs the turns upsert for every (session_id, prompt_id) key
// touched by candidates, sorted ascending (the lock-ordering invariant, SPEC
// §1.6). It is the third statement WriteBatch issues, after sessions.
func upsertTurns(ctx context.Context, tx pgx.Tx, aggs map[turnKey]*turnAgg) error {
	if len(aggs) == 0 {
		return nil
	}
	keys := make([]turnKey, 0, len(aggs))
	for k := range aggs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].SessionID != keys[j].SessionID {
			return keys[i].SessionID < keys[j].SessionID
		}
		return keys[i].PromptID < keys[j].PromptID
	})

	n := len(keys)
	sessionIDs := make([]string, n)
	promptIDs := make([]string, n)
	firstSeen := make([]time.Time, n)
	lastEvent := make([]time.Time, n)
	startedAt := make([]*time.Time, n)
	endedAt := make([]*time.Time, n)
	status := make([]string, n)
	apiReqCount := make([]int, n)
	inputTok, outputTok, cacheRead, cacheCreate := make([]int64, n), make([]int64, n), make([]int64, n), make([]int64, n)
	costUSD, costEstUSD := make([]float64, n), make([]float64, n)
	modelsJSON := make([]string, n)

	for i, k := range keys {
		a := aggs[k]
		sessionIDs[i], promptIDs[i] = k.SessionID, k.PromptID
		firstSeen[i], lastEvent[i] = a.firstSeen, a.lastEvent
		startedAt[i], endedAt[i] = a.startedAt, a.endedAt
		switch {
		case a.sawEndSuccess != nil && *a.sawEndSuccess:
			status[i] = "complete"
		case a.sawEndSuccess != nil && !*a.sawEndSuccess:
			status[i] = "failed"
		default:
			status[i] = "open"
		}
		apiReqCount[i] = a.apiRequestCount
		inputTok[i], outputTok[i], cacheRead[i], cacheCreate[i] = a.inputTokens, a.outputTokens, a.cacheRead, a.cacheCreate
		costUSD[i], costEstUSD[i] = a.costUSD, a.costEstimatedUSD

		modelList := make([]string, 0, len(a.models))
		for m := range a.models {
			modelList = append(modelList, m)
		}
		sort.Strings(modelList)
		b, err := json.Marshal(modelList)
		if err != nil {
			return fmt.Errorf("postgres: marshal turn models: %w", err)
		}
		modelsJSON[i] = string(b)
	}

	if _, err := tx.Exec(ctx, turnUpsertSQL,
		sessionIDs, promptIDs, startedAt, endedAt, firstSeen, lastEvent, status,
		apiReqCount, inputTok, outputTok, cacheRead, cacheCreate, costUSD, costEstUSD, modelsJSON,
	); err != nil {
		return fmt.Errorf("postgres: upsert turns: %w", err)
	}

	if err := reindexTurns(ctx, tx, sessionIDs); err != nil {
		return err
	}
	return nil
}

// turnUpsertSQL is the stub-on-reference turns upsert. status only ever
// advances open -> complete/failed here (a turn.end is authoritative and
// final in v1; nothing reopens a completed turn).
const turnUpsertSQL = `
INSERT INTO turns (
    session_id, prompt_id, started_at, ended_at, first_seen_at, last_event_at,
    status, api_request_count, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
    cost_usd, cost_estimated_usd, models
)
SELECT
    u.session_id, u.prompt_id, u.started_at, u.ended_at, u.first_seen, u.last_event,
    u.status, u.api_request_count, u.input_tokens_delta, u.output_tokens_delta, u.cache_read_delta, u.cache_creation_delta,
    u.cost_usd_delta, u.cost_estimated_usd_delta,
    COALESCE((SELECT array_agg(DISTINCT m) FROM jsonb_array_elements_text(u.models_delta) m), '{}'::text[])
FROM unnest(
    $1::text[], $2::text[], $3::timestamptz[], $4::timestamptz[], $5::timestamptz[], $6::timestamptz[], $7::text[],
    $8::int[], $9::bigint[], $10::bigint[], $11::bigint[], $12::bigint[], $13::float8[], $14::float8[], $15::jsonb[]
) AS u(
    session_id, prompt_id, started_at, ended_at, first_seen, last_event, status,
    api_request_count, input_tokens_delta, output_tokens_delta, cache_read_delta, cache_creation_delta,
    cost_usd_delta, cost_estimated_usd_delta, models_delta
)
ORDER BY u.session_id, u.prompt_id
ON CONFLICT (session_id, prompt_id) DO UPDATE SET
    first_seen_at = LEAST(turns.first_seen_at, EXCLUDED.first_seen_at),
    last_event_at = GREATEST(turns.last_event_at, EXCLUDED.last_event_at),
    started_at = CASE WHEN turns.started_at IS NULL THEN EXCLUDED.started_at
                      WHEN EXCLUDED.started_at IS NULL THEN turns.started_at
                      ELSE LEAST(turns.started_at, EXCLUDED.started_at) END,
    ended_at = CASE WHEN turns.ended_at IS NULL THEN EXCLUDED.ended_at
                    WHEN EXCLUDED.ended_at IS NULL THEN turns.ended_at
                    ELSE GREATEST(turns.ended_at, EXCLUDED.ended_at) END,
    status = CASE WHEN turns.status = 'open' THEN EXCLUDED.status ELSE turns.status END,
    api_request_count = turns.api_request_count + EXCLUDED.api_request_count,
    input_tokens = turns.input_tokens + EXCLUDED.input_tokens,
    output_tokens = turns.output_tokens + EXCLUDED.output_tokens,
    cache_read_tokens = turns.cache_read_tokens + EXCLUDED.cache_read_tokens,
    cache_creation_tokens = turns.cache_creation_tokens + EXCLUDED.cache_creation_tokens,
    cost_usd = turns.cost_usd + EXCLUDED.cost_usd,
    cost_estimated_usd = turns.cost_estimated_usd + EXCLUDED.cost_estimated_usd,
    models = COALESCE((SELECT array_agg(DISTINCT x) FROM unnest(turns.models || EXCLUDED.models) x), '{}'::text[])
`

// reindexTurns recomputes turn_index (SPEC §1.6's "turn_index assignment")
// for every turn of every session touched by this batch: a 0-based ordinal
// in first_seen_at order, tie-broken by prompt_id for a total order. Run as
// a trailing statement in the turns "slot": it re-touches rows this same
// transaction already holds the lock on from the upsert above, so it adds
// no new cross-transaction lock-acquisition ordering (the lock-ordering
// invariant, SPEC §1.6, is about the *first* time two concurrent
// transactions could contend on a row, not about how many statements one
// transaction issues against rows it already owns).
func reindexTurns(ctx context.Context, tx pgx.Tx, sessionIDs []string) error {
	_, err := tx.Exec(ctx, `
		UPDATE turns SET turn_index = ranked.rn - 1
		FROM (
		    SELECT session_id, prompt_id,
		           row_number() OVER (PARTITION BY session_id ORDER BY first_seen_at, prompt_id) AS rn
		    FROM turns
		    WHERE session_id = ANY($1::text[])
		) ranked
		WHERE turns.session_id = ranked.session_id AND turns.prompt_id = ranked.prompt_id
		  AND turns.turn_index IS DISTINCT FROM ranked.rn - 1`,
		dedupStrings(sessionIDs))
	if err != nil {
		return fmt.Errorf("postgres: reindex turns: %w", err)
	}
	return nil
}

func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
