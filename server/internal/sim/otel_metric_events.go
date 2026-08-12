package sim

import (
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// newSumMetric builds one OTLP Sum metric with a single delta data point,
// attrs prefixed with session.id — every metric fixture in
// internal/ingest/normalize/testdata/metrics/*.json carries a session.id
// data-point attribute (SPEC §1.8: OTEL_METRICS_INCLUDE_SESSION_ID), so
// every builder below takes sessionID and this function is the one place
// that attribute is attached.
//
// Claude Code's own metrics are cumulative counters exported at
// OTEL_METRIC_EXPORT_INTERVAL (telemetry-surfaces.md's "7 metrics" table),
// but the committed fixtures (cost_usage_cumulative.json,
// active_time_total.json) show both AGGREGATION_TEMPORALITY_CUMULATIVE and
// AGGREGATION_TEMPORALITY_DELTA in the wild with is_monotonic=true; this
// generator reports delta (one increment per 60s simulated export window)
// rather than tracking a running cumulative total per session, since a
// delta series round-trips through FromOTLPMetrics's Sum/delta path
// identically to a cumulative one for a single-point export. Temporality is
// Argus's own vocabulary (otel_metrics.go's mapTemporality comment), not a
// vendor-supplied value, so this choice is a generator modeling decision,
// not a fidelity-rule attribute fabrication.
func newSumMetric(name, sessionID string, seconds uint64, value float64, isInt bool, attrs ...*commonpb.KeyValue) *metricspb.Metric {
	allAttrs := append([]*commonpb.KeyValue{kvString("session.id", sessionID)}, attrs...)
	dp := &metricspb.NumberDataPoint{
		Attributes:   allAttrs,
		TimeUnixNano: seconds * 1e9,
	}
	if isInt {
		dp.Value = &metricspb.NumberDataPoint_AsInt{AsInt: int64(value)}
	} else {
		dp.Value = &metricspb.NumberDataPoint_AsDouble{AsDouble: value}
	}
	return &metricspb.Metric{
		Name: name,
		Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
			DataPoints:             []*metricspb.NumberDataPoint{dp},
			AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
			IsMonotonic:            true,
		}},
	}
}

// buildSessionCountMetric implements SPEC §7.1 item 1: "a
// claude_code.session.count metric point" with `start_type`
// (testdata/metrics/session_count.json; telemetry-surfaces.md line 26).
func buildSessionCountMetric(sessionID string, seconds uint64, startType string) *metricspb.Metric {
	return newSumMetric("claude_code.session.count", sessionID, seconds, 1, true,
		kvString("start_type", startType),
	)
}

// buildCostUsageMetric implements SPEC §7.1 item 4's `cost.usage`
// (testdata/metrics/cost_usage_cumulative.json: model, query_source;
// telemetry-surfaces.md line 29). querySource=="" is the "absent" draw from
// the same mixed distribution api_request uses (querysource.go), never the
// literal empty string.
func buildCostUsageMetric(sessionID string, seconds uint64, model, querySource string, usd float64) *metricspb.Metric {
	attrs := []*commonpb.KeyValue{kvString("model", model)}
	if querySource != "" {
		attrs = append(attrs, kvString("query_source", querySource))
	}
	return newSumMetric("claude_code.cost.usage", sessionID, seconds, usd, false, attrs...)
}

// buildTokenUsageMetric implements SPEC §7.1 item 4's `token.usage`
// (testdata/metrics/token_usage.json: type, model; telemetry-surfaces.md
// line 30: "type ∈ input|output|cacheRead|cacheCreation").
func buildTokenUsageMetric(sessionID string, seconds uint64, model, tokenType string, count int64) *metricspb.Metric {
	return newSumMetric("claude_code.token.usage", sessionID, seconds, float64(count), true,
		kvString("type", tokenType),
		kvString("model", model),
	)
}

// buildLinesOfCodeMetric implements `lines_of_code.count`
// (testdata/metrics/lines_of_code_count.json; telemetry-surfaces.md line
// 27: "type ∈ added|removed, model").
func buildLinesOfCodeMetric(sessionID string, seconds uint64, model, changeType string, count int64) *metricspb.Metric {
	return newSumMetric("claude_code.lines_of_code.count", sessionID, seconds, float64(count), true,
		kvString("type", changeType),
		kvString("model", model),
	)
}

// buildActiveTimeMetric implements `active_time.total`
// (testdata/metrics/active_time_total.json: type; telemetry-surfaces.md
// line 32: "type ∈ user|cli").
func buildActiveTimeMetric(sessionID string, seconds uint64, activeType string, durationSeconds float64) *metricspb.Metric {
	return newSumMetric("claude_code.active_time.total", sessionID, seconds, durationSeconds, false,
		kvString("type", activeType),
	)
}

// buildCodeEditToolDecisionMetric implements `code_edit_tool.decision`
// (testdata/metrics/code_edit_tool_decision.json; telemetry-surfaces.md
// line 31: "tool_name ∈ Edit|Write|NotebookEdit, decision ∈ accept|reject,
// source ∈ config|hook|user_permanent|user_temporary|user_abort|
// user_reject, language").
func buildCodeEditToolDecisionMetric(sessionID string, seconds uint64, toolName, decision, source, language string) *metricspb.Metric {
	return newSumMetric("claude_code.code_edit_tool.decision", sessionID, seconds, 1, true,
		kvString("tool_name", toolName),
		kvString("decision", decision),
		kvString("source", source),
		kvString("language", language),
	)
}

// buildCommitCountMetric / buildPullRequestCountMetric implement SPEC
// §7.1's "occasionally commit.count / pull_request.count"
// (testdata/metrics/pull_request_count.json, commit_count_no_session.json;
// telemetry-surfaces.md line 28). Neither documents an attribute beyond
// session.id, so these carry no extra attrs.
func buildCommitCountMetric(sessionID string, seconds uint64) *metricspb.Metric {
	return newSumMetric("claude_code.commit.count", sessionID, seconds, 1, true)
}

func buildPullRequestCountMetric(sessionID string, seconds uint64) *metricspb.Metric {
	return newSumMetric("claude_code.pull_request.count", sessionID, seconds, 1, true)
}
