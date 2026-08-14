// Package postgres — read_quality.go implements store.Reader's Facets,
// DataQuality, UnknownKinds, and HookLatency (SPEC §3.3, §4.2, §4.3, P3-08).
//
// Facets and DataQuality deliberately never read `events`: every value they
// report is derivable from sessions/tool_calls/turns/subagents/
// metric_samples, which SPEC §2.5's EXPLAIN guard does not police at all
// (its `strings.Contains(plan, "events")` check cannot trip on a plan that
// never names that relation). DataQuality's four booleans in particular are
// "has Argus ever received X" questions with no natural time bound — unlike
// UnknownKinds/HookLatency below, which SPEC §2.5 explicitly allows onto
// `events` only because both are bounded to the requested window — so
// answering them from `events` at all would mean an unbounded scan the
// guard's own spirit forbids even where its regex would not catch it. The
// promoted-column reasoning behind each of DataQuality's four checks:
//
//   - LogsExporterSeen: `api_request`/`llm.request` is the only event kind
//     with promoted token/cost columns (SPEC §1.5.1), and it exists solely
//     on the OTel *logs* pipeline — no hook event carries it (SPEC §1.5.2's
//     mapping table has no equivalent). turns.api_request_count (SPEC
//     §2.1) is incremented only by that kind, so any turn with a nonzero
//     count proves at least one OTel log event was ingested.
//   - MetricsExporterSeen: metric_samples (SPEC §2.3) is written exclusively
//     by Writer.WriteMetrics, itself fed only by the OTLP metrics receiver
//     — a non-empty table proves the metrics exporter was ever configured.
//   - HooksSeen: tool_calls.correlation (SPEC §1.6) is 'exact' or
//     'hook_only' only when a hook-sourced tool.pre/tool.decision/
//     tool.result event contributed to that call's correlation;
//     tool_calls.agent_id is documented "hook-sourced only" (SPEC §2.3).
//     subagents (SPEC §2.3) is populated exclusively by the hook-only
//     SubagentStart/SubagentStop events (SPEC §1.5.2; no OTel log
//     equivalent exists in §1.5.1) — a second, independent witness.
//   - ToolDetailsSeen: tool_calls.file_path is populated from
//     `attrs.tool_parameters.file_path` (SPEC §1.5.1's tool_result row),
//     which requires `OTEL_LOG_TOOL_DETAILS=1` (or a FileChanged hook, SPEC
//     line "file-touch view"). A non-null file_path proves tool_parameters
//     detail was actually captured at least once.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
	"github.com/YohannHommet/argus/server/internal/store/postgres/gen"
)

// maxUnknownKindGroups caps how many distinct (event_name, source) groups
// Reader.UnknownKinds returns. openapi.yaml exposes no `limit` parameter on
// GET /api/v1/quality/unknown-kinds (only `since`), so this is Argus's own
// safety cap against an unbounded GROUP BY result, not a wire-visible
// default.
const maxUnknownKindGroups = 500

// Facets implements store.Reader (SPEC §4.2): distinct raw values ever seen
// per filterable dimension, read from sessions/tool_calls only (see this
// file's package doc for why never events).
func (s *Store) Facets(ctx context.Context) (model.Facets, error) {
	q := gen.New(s.pool)

	projects, err := q.FacetProjects(ctx)
	if err != nil {
		return model.Facets{}, fmt.Errorf("postgres: facets: projects: %w", err)
	}
	vendors, err := q.FacetVendors(ctx)
	if err != nil {
		return model.Facets{}, fmt.Errorf("postgres: facets: vendors: %w", err)
	}
	models, err := q.FacetModels(ctx)
	if err != nil {
		return model.Facets{}, fmt.Errorf("postgres: facets: models: %w", err)
	}
	tools, err := q.FacetTools(ctx)
	if err != nil {
		return model.Facets{}, fmt.Errorf("postgres: facets: tools: %w", err)
	}
	decisionSources, err := q.FacetDecisionSources(ctx)
	if err != nil {
		return model.Facets{}, fmt.Errorf("postgres: facets: decision sources: %w", err)
	}
	querySources, err := q.FacetQuerySources(ctx)
	if err != nil {
		return model.Facets{}, fmt.Errorf("postgres: facets: query sources: %w", err)
	}

	return model.Facets{
		Projects:        projects,
		Models:          models,
		Vendors:         vendors,
		Tools:           tools,
		DecisionSources: decisionSources,
		QuerySources:    querySources,
	}, nil
}

// DataQuality implements store.Reader (SPEC §4.2's meta data_quality
// block); see this file's package doc for the reasoning behind each flag.
func (s *Store) DataQuality(ctx context.Context) (model.DataQuality, error) {
	row, err := gen.New(s.pool).DataQualitySnapshot(ctx)
	if err != nil {
		return model.DataQuality{}, fmt.Errorf("postgres: data quality: %w", err)
	}
	return model.DataQuality{
		LogsExporterSeen:    row.LogsExporterSeen,
		MetricsExporterSeen: row.MetricsExporterSeen,
		// HooksSeen: sqlc infers the OR-of-two-EXISTS expression as
		// nullable (pgtype.Bool) even though Postgres never actually
		// returns SQL NULL for it; a non-valid scan is treated as false,
		// the safe reading for an "ever seen" flag.
		HooksSeen:       row.HooksSeen.Valid && row.HooksSeen.Bool,
		ToolDetailsSeen: row.ToolDetailsSeen,
	}, nil
}

// UnknownKinds implements store.Reader (SPEC §4.3's getQualityUnknownKinds
// shape): unmapped event_name groups within [since, now), one raw attrs
// sample per group — the one query besides HookLatency's that SPEC §2.5
// permits onto `events`, bounded by `ts >= since` (the events (kind, ts
// DESC) index).
func (s *Store) UnknownKinds(ctx context.Context, since time.Time, limit int) ([]model.UnknownKindGroup, error) {
	if limit <= 0 || limit > maxUnknownKindGroups {
		limit = maxUnknownKindGroups
	}

	rows, err := gen.New(s.pool).UnknownKindGroups(ctx, gen.UnknownKindGroupsParams{
		Since: pgTimestamptz(since), RowLimit: int32(limit), // limit is clamped to [1, maxUnknownKindGroups] above, well within int32
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: unknown kinds: %w", err)
	}

	out := make([]model.UnknownKindGroup, len(rows))
	for i, r := range rows {
		var sample map[string]any
		if len(r.Sample) > 0 {
			if unmarshalErr := json.Unmarshal(r.Sample, &sample); unmarshalErr != nil {
				return nil, fmt.Errorf("postgres: unknown kinds: decode sample: %w", unmarshalErr)
			}
		}
		out[i] = model.UnknownKindGroup{
			EventName: r.EventName,
			Source:    model.Source(r.Source),
			Count:     r.Count,
			FirstSeen: r.FirstSeen.Time,
			LastSeen:  r.LastSeen.Time,
			Sample:    sample,
		}
	}
	return out, nil
}

// roundToInt64 rounds a nullable float64 percentile to int64, treating a
// NULL (a hook_event group whose every row lacked a duration_ms) as 0 —
// unlike read_analytics.go's roundToInt64Ptr, HookLatencyRow's p50/p95/p99
// fields are plain int64 (SPEC §4.3's worked example has none of them
// null), so there is no pointer to preserve absence with.
func roundToInt64(f *float64) int64 {
	if f == nil {
		return 0
	}
	return int64(math.Round(*f))
}

// HookLatency implements store.Reader (SPEC §4.3's getQualityHookLatency
// shape): executions/percentiles/errors/cancelled per hook_event, from
// hook.execution_end events bounded to the requested window — the second
// (and only other) query SPEC §2.5 permits onto `events`. Hand-written pgx,
// not sqlc, for the same percentile_cont NOT-NULL mis-inference reason
// read_analytics.go's decisionWaitPercentiles and read_sessions.go's
// sessionHookLatency document; hook_event is never promoted to its own
// column (SPEC §1.5.1 promotes only duration_ms/success from
// hook.execution_end), so it is read out of attrs, matching
// sessionHookLatency's own convention.
func (s *Store) HookLatency(ctx context.Context, f store.AnalyticsFilter) (model.HookLatency, error) {
	from, to := resolveWindow(f, time.Now())

	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(attrs->>'hook_event', '') AS hook_event,
		       count(*)::bigint AS executions,
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms) AS p50_ms,
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) AS p95_ms,
		       percentile_cont(0.99) WITHIN GROUP (ORDER BY duration_ms) AS p99_ms,
		       count(*) FILTER (WHERE success = false)::bigint AS errors,
		       count(*) FILTER (WHERE COALESCE((attrs->>'num_cancelled')::int, 0) > 0)::bigint AS cancelled
		FROM events
		WHERE kind = 'hook.execution_end' AND ts >= $1 AND ts < $2
		GROUP BY 1
		ORDER BY 1`, from, to)
	if err != nil {
		return model.HookLatency{}, fmt.Errorf("postgres: hook latency: %w", err)
	}
	defer rows.Close()

	out := []model.HookLatencyRow{}
	for rows.Next() {
		var (
			hookEvent                string
			executions, errs, cancel int64
			p50, p95, p99            *float64
		)
		if scanErr := rows.Scan(&hookEvent, &executions, &p50, &p95, &p99, &errs, &cancel); scanErr != nil {
			return model.HookLatency{}, fmt.Errorf("postgres: hook latency: scan: %w", scanErr)
		}
		out = append(out, model.HookLatencyRow{
			HookEvent:  hookEvent,
			Executions: executions,
			P50MS:      roundToInt64(p50),
			P95MS:      roundToInt64(p95),
			P99MS:      roundToInt64(p99),
			Errors:     errs,
			Cancelled:  cancel,
		})
	}
	if err := rows.Err(); err != nil {
		return model.HookLatency{}, fmt.Errorf("postgres: hook latency: %w", err)
	}

	return model.HookLatency{Rows: out}, nil
}
