// Package postgres — upsert_session.go builds the `sessions` stub-on-
// reference upsert (SPEC §1.7 rule 1, §1.6, §2.1): one bulk, unnest-driven
// statement per WriteBatch call that folds in every candidate event's
// contribution to its session row, with SPEC §1.5.3's field_ranks
// precedence mechanism for the columns two sources can disagree about.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/YohannHommet/argus/server/internal/model"
)

// Source ranks for the field_ranks precedence mechanism (SPEC §1.5.3):
// "Ranks: otel_log=30, hook=20, otel_metric=10, sim = the rank of the source
// it imitates." sim's imitated source isn't yet expressed on model.Event (no
// ticket has landed that attribute), so it falls back to the hook rank here
// — documented as a P2-06 assumption for the simulator ticket to revisit,
// not a silent guess.
const (
	rankOTelMetric = 10
	rankHook       = 20
	rankOTelLog    = 30
	rankNone       = -1 // sentinel: this event contributed no candidate for the field
)

func sourceRank(s model.Source) int {
	switch s {
	case model.SourceOTelLog:
		return rankOTelLog
	case model.SourceHook:
		return rankHook
	case model.SourceOTelMetric:
		return rankOTelMetric
	case model.SourceSim:
		return rankHook
	default:
		return rankHook
	}
}

// rankedValue is a (value, rank, ts) candidate for one field_ranks-governed
// column, kept while folding a session's events so the batch-local winner
// (highest rank, later ts on a tie) can be computed before ever touching the
// database, per SPEC §1.5.3: "equal rank ⇒ later ts wins".
type rankedValue struct {
	val  string
	rank int
	ts   time.Time
	set  bool
}

func (r *rankedValue) offer(val string, rank int, ts time.Time) {
	if val == "" {
		return
	}
	if !r.set || rank > r.rank || (rank == r.rank && ts.After(r.ts)) {
		*r = rankedValue{val: val, rank: rank, ts: ts, set: true}
	}
}

// sessionAgg accumulates one session's contribution from the batch's
// candidate events (SPEC §1.6 projections table: "session.* events + first/
// last event seen + aggregates of llm.request").
type sessionAgg struct {
	id         string
	vendor     string
	firstSeen  time.Time
	lastEvent  time.Time
	eventCount int64
	sawStart   bool
	sawEnd     bool
	startedAt  *time.Time
	endedAt    *time.Time

	cwd, project, startType, endReason, permissionMode                      rankedValue
	appVersion, entrypoint, terminalType, userEmail, userAccountUUID, orgID rankedValue

	inputTokens, outputTokens, cacheRead, cacheCreate int64
	costUSD, costEstimatedUSD                         float64
	costByQuerySource                                 map[string]float64
	models                                            map[string]struct{}
}

func newSessionAgg(id string) *sessionAgg {
	return &sessionAgg{id: id, costByQuerySource: map[string]float64{}, models: map[string]struct{}{}}
}

// attrStr reads a string attribute out of an event's raw payload (SPEC
// §1.3's "attrs is the full flattened source payload"). Deliberately
// forgiving: a missing or wrong-typed key yields "", never an error (SPEC
// §1.5.2: "a missing field yields NULL, never an error").
func attrStr(attrs map[string]any, key string) string {
	v, ok := attrs[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// foldSessionEvents groups persisted candidate events by session_id and
// folds each into a sessionAgg, in (ts, dedup_key) order (the same order the
// caller already sorted candidates into for the lock-ordering invariant), so
// rankedValue.offer's tie-break ("equal rank ⇒ later ts wins") sees events
// in a stable, deterministic order.
func foldSessionEvents(candidates []model.Event) map[string]*sessionAgg {
	out := map[string]*sessionAgg{}
	for _, e := range candidates {
		agg, ok := out[e.SessionID]
		if !ok {
			agg = newSessionAgg(e.SessionID)
			agg.firstSeen, agg.lastEvent = e.TS, e.TS
			out[e.SessionID] = agg
		}
		agg.foldEvent(e)
	}
	return out
}

func (a *sessionAgg) foldEvent(e model.Event) {
	if e.TS.Before(a.firstSeen) {
		a.firstSeen = e.TS
	}
	if e.TS.After(a.lastEvent) {
		a.lastEvent = e.TS
	}
	a.eventCount++
	if e.Vendor != "" {
		a.vendor = e.Vendor
	}

	rank := sourceRank(e.Source)

	// permission_mode is not in SPEC §1.5.3's precedence table (unlike
	// cwd/start_type/etc., which are single-source by construction, see
	// below), so it uses the generic cross-source rank comparison.
	if e.PermissionMode != nil {
		a.permissionMode.offer(*e.PermissionMode, rank, e.TS)
	}

	switch e.Kind { //nolint:exhaustive // only the kinds SPEC §1.6/§1.5.3 assign a sessions-projection role to are handled; every other Kind contributes only the common event_count/first_seen/last_event folded above.
	case model.KindSessionStart:
		a.sawStart = true
		if a.startedAt == nil || e.TS.Before(*a.startedAt) {
			ts := e.TS
			a.startedAt = &ts
		}
		// SPEC §1.5.3: "sessions.cwd, project | hook (SessionStart.cwd,
		// CwdChanged) ... sessions.start_type ... | hook". These columns are
		// deliberately extracted from hook-sourced events only (never from
		// an otel_log candidate, however it's spelled in attrs): OTel's only
		// cwd-adjacent signal is workspace.host_paths, a list SPEC §1.5.3
		// explicitly rejects as "less direct", so there is nothing for it
		// to compete with here — the winner is fixed, not just usually-higher-rank.
		if e.Source == model.SourceHook {
			if cwd := attrStr(e.Attrs, "cwd"); cwd != "" {
				a.cwd.offer(cwd, rankHook, e.TS)
				a.project.offer(path.Base(cwd), rankHook, e.TS)
			}
			a.startType.offer(attrStr(e.Attrs, "source"), rankHook, e.TS)
		}
	case model.KindSessionEnd:
		a.sawEnd = true
		if a.endedAt == nil || e.TS.After(*a.endedAt) {
			ts := e.TS
			a.endedAt = &ts
		}
		if e.Source == model.SourceHook {
			a.endReason.offer(attrStr(e.Attrs, "reason"), rankHook, e.TS)
		}
	case model.KindWorkspaceCWDChanged:
		if e.Source == model.SourceHook {
			if cwd := attrStr(e.Attrs, "cwd"); cwd != "" {
				a.cwd.offer(cwd, rankHook, e.TS)
				a.project.offer(path.Base(cwd), rankHook, e.TS)
			}
		}
	case model.KindLLMRequest:
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
		cost := 0.0
		if e.CostUSD != nil {
			cost = *e.CostUSD
		}
		if e.CostSource != nil && *e.CostSource == "estimated" {
			a.costEstimatedUSD += cost
		} else if e.CostUSD != nil {
			a.costUSD += cost
		}
		// SPEC §1.9: "sessions.cost_by_query_source jsonb — a map from the
		// raw observed query_source value ('' for absent) to summed cost."
		qs := ""
		if e.QuerySource != nil {
			qs = *e.QuerySource
		}
		a.costByQuerySource[qs] += cost
		if e.Model != nil && *e.Model != "" {
			a.models[*e.Model] = struct{}{}
		}
	}

	// SPEC §1.5.3: app_version/entrypoint/terminal_type/user_*/org_id are
	// otel_log-only ("hooks don't carry them"), with app_version falling
	// back to the resource service.version attribute.
	if e.Source == model.SourceOTelLog {
		if v := attrStr(e.Attrs, "app.version"); v != "" {
			a.appVersion.offer(v, rankOTelLog, e.TS)
		} else if v := attrStr(e.Attrs, "resource.service.version"); v != "" {
			a.appVersion.offer(v, rankOTelLog, e.TS)
		}
		if v := attrStr(e.Attrs, "entrypoint"); v != "" {
			a.entrypoint.offer(v, rankOTelLog, e.TS)
		}
		if v := attrStr(e.Attrs, "terminal.type"); v != "" {
			a.terminalType.offer(v, rankOTelLog, e.TS)
		}
		if v := attrStr(e.Attrs, "user.email"); v != "" {
			a.userEmail.offer(v, rankOTelLog, e.TS)
		}
		if v := attrStr(e.Attrs, "user.account_uuid"); v != "" {
			a.userAccountUUID.offer(v, rankOTelLog, e.TS)
		}
		if v := attrStr(e.Attrs, "organization.id"); v != "" {
			a.orgID.offer(v, rankOTelLog, e.TS)
		}
	}
}

// sessionUpsertResult is one row of upsertSessions's RETURNING clause: the
// post-merge state needed for the SPEC §2.4 project-change re-mark and the
// trailing turn_count correction.
type sessionUpsertResult struct {
	ID                       string
	CWD, Project             string
	OldCWD, OldProject       string
	FirstSeenAt, LastEventAt time.Time
}

// upsertSessions runs the SPEC §1.6/§1.7/§1.5.3 sessions upsert for every
// session touched by candidates, sorted by id ascending (the lock-ordering
// invariant, SPEC §1.6). It is the second statement WriteBatch issues,
// right after the ingest_dedup gate.
func upsertSessions(ctx context.Context, tx pgx.Tx, aggs map[string]*sessionAgg) ([]sessionUpsertResult, error) {
	if len(aggs) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(aggs))
	for id := range aggs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	n := len(ids)
	vendors := make([]string, n)
	firstSeen := make([]time.Time, n)
	lastEvent := make([]time.Time, n)
	eventCount := make([]int64, n)
	startedAt := make([]*time.Time, n)
	endedAt := make([]*time.Time, n)
	sawEnd := make([]bool, n)
	sawStart := make([]bool, n)

	cwdVal, projectVal, startTypeVal, endReasonVal, permVal := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	cwdRank, projectRank, startTypeRank, endReasonRank, permRank := make([]int, n), make([]int, n), make([]int, n), make([]int, n), make([]int, n)
	appVal, entryVal, termVal, emailVal, uuidVal, orgVal := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	appRank, entryRank, termRank, emailRank, uuidRank, orgRank := make([]int, n), make([]int, n), make([]int, n), make([]int, n), make([]int, n), make([]int, n)

	inputTok, outputTok, cacheRead, cacheCreate := make([]int64, n), make([]int64, n), make([]int64, n), make([]int64, n)
	costUSD, costEstUSD := make([]float64, n), make([]float64, n)
	cqsJSON := make([]string, n)
	modelsJSON := make([]string, n)

	for i, id := range ids {
		a := aggs[id]
		vendors[i] = a.vendor
		if vendors[i] == "" {
			vendors[i] = "unknown"
		}
		firstSeen[i], lastEvent[i] = a.firstSeen, a.lastEvent
		eventCount[i] = a.eventCount
		startedAt[i], endedAt[i] = a.startedAt, a.endedAt
		sawEnd[i], sawStart[i] = a.sawEnd, a.sawStart

		cwdVal[i], cwdRank[i] = a.cwd.val, orNone(a.cwd)
		projectVal[i], projectRank[i] = a.project.val, orNone(a.project)
		startTypeVal[i], startTypeRank[i] = a.startType.val, orNone(a.startType)
		endReasonVal[i], endReasonRank[i] = a.endReason.val, orNone(a.endReason)
		permVal[i], permRank[i] = a.permissionMode.val, orNone(a.permissionMode)
		appVal[i], appRank[i] = a.appVersion.val, orNone(a.appVersion)
		entryVal[i], entryRank[i] = a.entrypoint.val, orNone(a.entrypoint)
		termVal[i], termRank[i] = a.terminalType.val, orNone(a.terminalType)
		emailVal[i], emailRank[i] = a.userEmail.val, orNone(a.userEmail)
		uuidVal[i], uuidRank[i] = a.userAccountUUID.val, orNone(a.userAccountUUID)
		orgVal[i], orgRank[i] = a.orgID.val, orNone(a.orgID)

		inputTok[i], outputTok[i], cacheRead[i], cacheCreate[i] = a.inputTokens, a.outputTokens, a.cacheRead, a.cacheCreate
		costUSD[i], costEstUSD[i] = a.costUSD, a.costEstimatedUSD

		cqsBytes, err := json.Marshal(a.costByQuerySource)
		if err != nil {
			return nil, fmt.Errorf("postgres: marshal cost_by_query_source: %w", err)
		}
		cqsJSON[i] = string(cqsBytes)

		modelList := make([]string, 0, len(a.models))
		for m := range a.models {
			modelList = append(modelList, m)
		}
		sort.Strings(modelList)
		modelBytes, err := json.Marshal(modelList)
		if err != nil {
			return nil, fmt.Errorf("postgres: marshal models: %w", err)
		}
		modelsJSON[i] = string(modelBytes)
	}

	rows, err := tx.Query(ctx, sessionUpsertSQL,
		ids, vendors, firstSeen, lastEvent, eventCount,
		startedAt, endedAt, sawEnd, sawStart,
		cwdVal, cwdRank, projectVal, projectRank, startTypeVal, startTypeRank, endReasonVal, endReasonRank, permVal, permRank,
		appVal, appRank, entryVal, entryRank, termVal, termRank, emailVal, emailRank, uuidVal, uuidRank, orgVal, orgRank,
		inputTok, outputTok, cacheRead, cacheCreate,
		costUSD, costEstUSD, cqsJSON, modelsJSON,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: upsert sessions: %w", err)
	}
	defer rows.Close()

	var results []sessionUpsertResult
	for rows.Next() {
		var r sessionUpsertResult
		var cwd, project, oldCWD, oldProject *string
		if err := rows.Scan(&r.ID, &cwd, &project, &oldCWD, &oldProject, &r.FirstSeenAt, &r.LastEventAt); err != nil {
			return nil, fmt.Errorf("postgres: scan session upsert: %w", err)
		}
		if cwd != nil {
			r.CWD = *cwd
		}
		if project != nil {
			r.Project = *project
		}
		if oldCWD != nil {
			r.OldCWD = *oldCWD
		}
		if oldProject != nil {
			r.OldProject = *oldProject
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: upsert sessions: %w", err)
	}
	return results, nil
}

func orNone(r rankedValue) int {
	if !r.set {
		return rankNone
	}
	return r.rank
}

// sessionUpsertSQL is the stub-on-reference + field_ranks upsert (SPEC
// §1.6, §1.7 rule 1, §1.5.3, §2.1). `old` captures each touched session's
// cwd/project *before* this statement's effect, using ordinary MVCC
// snapshot semantics of a CTE evaluated against the pre-statement table
// state, so the RETURNING clause can report both old and new values for the
// SPEC §2.4 project-change re-mark without a second round trip.
//
// field_ranks is stored as a flat jsonb object of {column: last-writing-
// rank}; a column only advances past its stored rank (never regresses),
// which is what "write overwrites only when its rank is >= the stored rank"
// (SPEC §1.5.3) reduces to when expressed as GREATEST(stored, incoming).
const sessionUpsertSQL = `
WITH old AS (
    SELECT id, cwd, project FROM sessions WHERE id = ANY($1::text[])
)
INSERT INTO sessions (
    id, vendor, first_seen_at, last_event_at, event_count,
    started_at, ended_at, status,
    cwd, project, start_type, end_reason, permission_mode,
    app_version, entrypoint, terminal_type, user_email, user_account_uuid, organization_id,
    input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
    cost_usd, cost_estimated_usd, cost_by_query_source, models, field_ranks, updated_at
)
SELECT
    u.id, u.vendor, u.first_seen, u.last_event, u.event_count_delta,
    u.started_at, u.ended_at,
    CASE WHEN u.saw_end THEN 'ended' WHEN u.saw_start THEN 'active' ELSE 'unknown' END,
    NULLIF(u.cwd_val, ''), NULLIF(u.project_val, ''), NULLIF(u.start_type_val, ''), NULLIF(u.end_reason_val, ''), NULLIF(u.permission_mode_val, ''),
    NULLIF(u.app_version_val, ''), NULLIF(u.entrypoint_val, ''), NULLIF(u.terminal_type_val, ''), NULLIF(u.user_email_val, ''), NULLIF(u.user_account_uuid_val, ''), NULLIF(u.organization_id_val, ''),
    u.input_tokens_delta, u.output_tokens_delta, u.cache_read_delta, u.cache_creation_delta,
    u.cost_usd_delta, u.cost_estimated_usd_delta,
    u.cost_by_query_source_delta,
    COALESCE((SELECT array_agg(DISTINCT m) FROM jsonb_array_elements_text(u.models_delta) m), '{}'::text[]),
    jsonb_build_object(
        'cwd', GREATEST(-1, u.cwd_rank), 'project', GREATEST(-1, u.project_rank),
        'start_type', GREATEST(-1, u.start_type_rank), 'end_reason', GREATEST(-1, u.end_reason_rank),
        'permission_mode', GREATEST(-1, u.permission_mode_rank), 'app_version', GREATEST(-1, u.app_version_rank),
        'entrypoint', GREATEST(-1, u.entrypoint_rank), 'terminal_type', GREATEST(-1, u.terminal_type_rank),
        'user_email', GREATEST(-1, u.user_email_rank), 'user_account_uuid', GREATEST(-1, u.user_account_uuid_rank),
        'organization_id', GREATEST(-1, u.organization_id_rank)
    ),
    now()
FROM unnest(
    $1::text[], $2::text[], $3::timestamptz[], $4::timestamptz[], $5::bigint[],
    $6::timestamptz[], $7::timestamptz[], $8::bool[], $9::bool[],
    $10::text[], $11::int[], $12::text[], $13::int[], $14::text[], $15::int[], $16::text[], $17::int[], $18::text[], $19::int[],
    $20::text[], $21::int[], $22::text[], $23::int[], $24::text[], $25::int[], $26::text[], $27::int[], $28::text[], $29::int[],
    $30::text[], $31::int[],
    $32::bigint[], $33::bigint[], $34::bigint[], $35::bigint[],
    $36::float8[], $37::float8[], $38::jsonb[], $39::jsonb[]
) AS u(
    id, vendor, first_seen, last_event, event_count_delta,
    started_at, ended_at, saw_end, saw_start,
    cwd_val, cwd_rank, project_val, project_rank, start_type_val, start_type_rank, end_reason_val, end_reason_rank, permission_mode_val, permission_mode_rank,
    app_version_val, app_version_rank, entrypoint_val, entrypoint_rank, terminal_type_val, terminal_type_rank, user_email_val, user_email_rank, user_account_uuid_val, user_account_uuid_rank, organization_id_val, organization_id_rank,
    input_tokens_delta, output_tokens_delta, cache_read_delta, cache_creation_delta,
    cost_usd_delta, cost_estimated_usd_delta, cost_by_query_source_delta, models_delta
)
ORDER BY u.id
ON CONFLICT (id) DO UPDATE SET
    vendor = EXCLUDED.vendor,
    first_seen_at = LEAST(sessions.first_seen_at, EXCLUDED.first_seen_at),
    last_event_at = GREATEST(sessions.last_event_at, EXCLUDED.last_event_at),
    event_count = sessions.event_count + EXCLUDED.event_count,
    started_at = CASE WHEN sessions.started_at IS NULL THEN EXCLUDED.started_at
                      WHEN EXCLUDED.started_at IS NULL THEN sessions.started_at
                      ELSE LEAST(sessions.started_at, EXCLUDED.started_at) END,
    ended_at = CASE WHEN sessions.ended_at IS NULL THEN EXCLUDED.ended_at
                    WHEN EXCLUDED.ended_at IS NULL THEN sessions.ended_at
                    ELSE GREATEST(sessions.ended_at, EXCLUDED.ended_at) END,
    status = CASE
        WHEN EXCLUDED.status = 'ended' THEN 'ended'
        WHEN sessions.status = 'ended' THEN 'ended'
        WHEN EXCLUDED.status = 'active' THEN 'active'
        WHEN sessions.status = 'abandoned' THEN 'abandoned'
        WHEN sessions.status = 'active' THEN 'active'
        ELSE 'unknown'
    END,
    cwd = CASE WHEN (EXCLUDED.field_ranks->>'cwd')::int >= COALESCE((sessions.field_ranks->>'cwd')::int, -1) AND EXCLUDED.cwd IS NOT NULL THEN EXCLUDED.cwd ELSE sessions.cwd END,
    project = CASE WHEN (EXCLUDED.field_ranks->>'project')::int >= COALESCE((sessions.field_ranks->>'project')::int, -1) AND EXCLUDED.project IS NOT NULL THEN EXCLUDED.project ELSE sessions.project END,
    start_type = CASE WHEN (EXCLUDED.field_ranks->>'start_type')::int >= COALESCE((sessions.field_ranks->>'start_type')::int, -1) AND EXCLUDED.start_type IS NOT NULL THEN EXCLUDED.start_type ELSE sessions.start_type END,
    end_reason = CASE WHEN (EXCLUDED.field_ranks->>'end_reason')::int >= COALESCE((sessions.field_ranks->>'end_reason')::int, -1) AND EXCLUDED.end_reason IS NOT NULL THEN EXCLUDED.end_reason ELSE sessions.end_reason END,
    permission_mode = CASE WHEN (EXCLUDED.field_ranks->>'permission_mode')::int >= COALESCE((sessions.field_ranks->>'permission_mode')::int, -1) AND EXCLUDED.permission_mode IS NOT NULL THEN EXCLUDED.permission_mode ELSE sessions.permission_mode END,
    app_version = CASE WHEN (EXCLUDED.field_ranks->>'app_version')::int >= COALESCE((sessions.field_ranks->>'app_version')::int, -1) AND EXCLUDED.app_version IS NOT NULL THEN EXCLUDED.app_version ELSE sessions.app_version END,
    entrypoint = CASE WHEN (EXCLUDED.field_ranks->>'entrypoint')::int >= COALESCE((sessions.field_ranks->>'entrypoint')::int, -1) AND EXCLUDED.entrypoint IS NOT NULL THEN EXCLUDED.entrypoint ELSE sessions.entrypoint END,
    terminal_type = CASE WHEN (EXCLUDED.field_ranks->>'terminal_type')::int >= COALESCE((sessions.field_ranks->>'terminal_type')::int, -1) AND EXCLUDED.terminal_type IS NOT NULL THEN EXCLUDED.terminal_type ELSE sessions.terminal_type END,
    user_email = CASE WHEN (EXCLUDED.field_ranks->>'user_email')::int >= COALESCE((sessions.field_ranks->>'user_email')::int, -1) AND EXCLUDED.user_email IS NOT NULL THEN EXCLUDED.user_email ELSE sessions.user_email END,
    user_account_uuid = CASE WHEN (EXCLUDED.field_ranks->>'user_account_uuid')::int >= COALESCE((sessions.field_ranks->>'user_account_uuid')::int, -1) AND EXCLUDED.user_account_uuid IS NOT NULL THEN EXCLUDED.user_account_uuid ELSE sessions.user_account_uuid END,
    organization_id = CASE WHEN (EXCLUDED.field_ranks->>'organization_id')::int >= COALESCE((sessions.field_ranks->>'organization_id')::int, -1) AND EXCLUDED.organization_id IS NOT NULL THEN EXCLUDED.organization_id ELSE sessions.organization_id END,
    input_tokens = sessions.input_tokens + EXCLUDED.input_tokens,
    output_tokens = sessions.output_tokens + EXCLUDED.output_tokens,
    cache_read_tokens = sessions.cache_read_tokens + EXCLUDED.cache_read_tokens,
    cache_creation_tokens = sessions.cache_creation_tokens + EXCLUDED.cache_creation_tokens,
    cost_usd = sessions.cost_usd + EXCLUDED.cost_usd,
    cost_estimated_usd = sessions.cost_estimated_usd + EXCLUDED.cost_estimated_usd,
    cost_by_query_source = COALESCE((
        SELECT jsonb_object_agg(key, total)
        FROM (
            SELECT key, SUM(value::numeric) AS total
            FROM (
                SELECT * FROM jsonb_each_text(sessions.cost_by_query_source)
                UNION ALL
                SELECT * FROM jsonb_each_text(EXCLUDED.cost_by_query_source)
            ) merged(key, value)
            GROUP BY key
        ) totals
    ), '{}'::jsonb),
    models = COALESCE((SELECT array_agg(DISTINCT x) FROM unnest(sessions.models || EXCLUDED.models) x), '{}'::text[]),
    field_ranks = sessions.field_ranks || jsonb_build_object(
        'cwd', GREATEST(COALESCE((sessions.field_ranks->>'cwd')::int, -1), (EXCLUDED.field_ranks->>'cwd')::int),
        'project', GREATEST(COALESCE((sessions.field_ranks->>'project')::int, -1), (EXCLUDED.field_ranks->>'project')::int),
        'start_type', GREATEST(COALESCE((sessions.field_ranks->>'start_type')::int, -1), (EXCLUDED.field_ranks->>'start_type')::int),
        'end_reason', GREATEST(COALESCE((sessions.field_ranks->>'end_reason')::int, -1), (EXCLUDED.field_ranks->>'end_reason')::int),
        'permission_mode', GREATEST(COALESCE((sessions.field_ranks->>'permission_mode')::int, -1), (EXCLUDED.field_ranks->>'permission_mode')::int),
        'app_version', GREATEST(COALESCE((sessions.field_ranks->>'app_version')::int, -1), (EXCLUDED.field_ranks->>'app_version')::int),
        'entrypoint', GREATEST(COALESCE((sessions.field_ranks->>'entrypoint')::int, -1), (EXCLUDED.field_ranks->>'entrypoint')::int),
        'terminal_type', GREATEST(COALESCE((sessions.field_ranks->>'terminal_type')::int, -1), (EXCLUDED.field_ranks->>'terminal_type')::int),
        'user_email', GREATEST(COALESCE((sessions.field_ranks->>'user_email')::int, -1), (EXCLUDED.field_ranks->>'user_email')::int),
        'user_account_uuid', GREATEST(COALESCE((sessions.field_ranks->>'user_account_uuid')::int, -1), (EXCLUDED.field_ranks->>'user_account_uuid')::int),
        'organization_id', GREATEST(COALESCE((sessions.field_ranks->>'organization_id')::int, -1), (EXCLUDED.field_ranks->>'organization_id')::int)
    ),
    updated_at = now()
RETURNING sessions.id, sessions.cwd, sessions.project,
    (SELECT old.cwd FROM old WHERE old.id = sessions.id),
    (SELECT old.project FROM old WHERE old.id = sessions.id),
    sessions.first_seen_at, sessions.last_event_at
`
