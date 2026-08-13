package normalize

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
)

// loadMetricsFixture mirrors loadFixture (otel_logs_test.go) for
// testdata/metrics/<name>.json, decoding into the real OTLP type
// FromOTLPMetrics decodes in production.
func loadMetricsFixture(t *testing.T, name string) *metricspb.MetricsData {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "metrics", name)) //nolint:gosec // test-only: name is always a literal from this file's own test table, never external input
	require.NoError(t, err, "reading fixture %s", name)

	var data metricspb.MetricsData
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	require.NoError(t, opts.Unmarshal(b, &data), "unmarshaling fixture %s", name)
	return &data
}

// normalizeOneMetric loads a single-data-point fixture, runs
// FromOTLPMetrics, and asserts it produced exactly one sample and no
// rejections — the common shape for a single Sum/Gauge data point fixture.
func normalizeOneMetric(t *testing.T, fixture string) model.MetricSample {
	t.Helper()
	data := loadMetricsFixture(t, fixture)
	n := newTestNormalizer()
	samples, rejections := n.FromOTLPMetrics(data)
	require.Empty(t, rejections, "fixture %s produced unexpected rejections", fixture)
	require.Len(t, samples, 1, "fixture %s did not produce exactly one sample", fixture)
	return samples[0]
}

// TestFromOTLPMetrics_SevenKnownMetrics is the ticket's core AC: "cases for
// each of the 7 claude_code.* metrics with their documented attribute sets".
func TestFromOTLPMetrics_SevenKnownMetrics(t *testing.T) {
	t.Parallel()

	t.Run("session.count", func(t *testing.T) {
		t.Parallel()
		s := normalizeOneMetric(t, "session_count.json")
		require.Equal(t, "claude_code.session.count", s.Name)
		require.Equal(t, "claude_code", s.Vendor)
		require.Equal(t, temporalityDelta, s.Temporality)
		require.InDelta(t, float64(1), s.Value, 1e-9)
		require.Equal(t, strp("11111111-1111-4111-8111-111111111111"), s.SessionID)
		require.Equal(t, "fresh", s.Attrs["start_type"])
		require.NotEmpty(t, s.SeriesHash)
		require.NotEmpty(t, s.DedupKey)
	})

	t.Run("lines_of_code.count", func(t *testing.T) {
		t.Parallel()
		data := loadMetricsFixture(t, "lines_of_code_count.json")
		n := newTestNormalizer()
		samples, rejections := n.FromOTLPMetrics(data)
		require.Empty(t, rejections)
		require.Len(t, samples, 3)

		for _, s := range samples {
			require.Equal(t, "claude_code.lines_of_code.count", s.Name)
			require.Equal(t, temporalityDelta, s.Temporality)
			require.Equal(t, strp("22222222-2222-4222-8222-222222222222"), s.SessionID)
		}
		require.Equal(t, "added", samples[0].Attrs["type"])
		require.InDelta(t, float64(42), samples[0].Value, 1e-9)
		require.Equal(t, "added", samples[1].Attrs["type"])
		require.InDelta(t, float64(42), samples[1].Value, 1e-9)
		require.Equal(t, "removed", samples[2].Attrs["type"])
		require.InDelta(t, float64(7), samples[2].Value, 1e-9)

		// series_hash stable across attribute map iteration/wire order
		// (samples[0] and samples[1] carry identical attrs, reversed key
		// order in the fixture).
		require.Equal(t, samples[0].SeriesHash, samples[1].SeriesHash,
			"same name+attrs (different KeyValue slice order) must hash identically")

		// series_hash differs when one attribute value changes (type:
		// added -> removed).
		require.NotEqual(t, samples[0].SeriesHash, samples[2].SeriesHash,
			"a changed attribute value must change series_hash")

		// dedup_key must also differ across the three distinct data points
		// (same ts, same name, different attrs => different canonical_attrs
		// hash input per SPEC §1.7 rule 2).
		require.NotEqual(t, samples[0].DedupKey, samples[2].DedupKey)
		require.Equal(t, samples[0].DedupKey, samples[1].DedupKey,
			"identical (name, ts, attrs) must dedup-key identically regardless of wire attribute order")
	})

	t.Run("commit.count (no session.id)", func(t *testing.T) {
		t.Parallel()
		s := normalizeOneMetric(t, "commit_count_no_session.json")
		require.Equal(t, "claude_code.commit.count", s.Name)
		require.Equal(t, temporalityDelta, s.Temporality)
		require.Nil(t, s.SessionID, "AC: a metric with no session.id is accepted with session_id = NULL")
		require.InDelta(t, float64(1), s.Value, 1e-9)
	})

	t.Run("pull_request.count", func(t *testing.T) {
		t.Parallel()
		s := normalizeOneMetric(t, "pull_request_count.json")
		require.Equal(t, "claude_code.pull_request.count", s.Name)
		require.Equal(t, strp("33333333-3333-4333-8333-333333333333"), s.SessionID)
		require.Equal(t, temporalityDelta, s.Temporality)
	})

	t.Run("cost.usage (cumulative)", func(t *testing.T) {
		t.Parallel()
		s := normalizeOneMetric(t, "cost_usage_cumulative.json")
		require.Equal(t, "claude_code.cost.usage", s.Name)
		require.Equal(t, temporalityCumulative, s.Temporality, "AC: cumulative vs delta labelled correctly")
		require.InDelta(t, 0.4523, s.Value, 1e-9)
		require.Equal(t, "claude-opus-4-6", s.Attrs["model"])
		require.Equal(t, "sdk", s.Attrs["query_source"])
		require.Nil(t, s.Delta, "delta is filled by the Phase 3 rollup job, never by the normalizer")
	})

	t.Run("token.usage", func(t *testing.T) {
		t.Parallel()
		s := normalizeOneMetric(t, "token_usage.json")
		require.Equal(t, "claude_code.token.usage", s.Name)
		require.Equal(t, temporalityDelta, s.Temporality)
		require.InDelta(t, float64(1024), s.Value, 1e-9)
		require.Equal(t, "input", s.Attrs["type"])
	})

	t.Run("code_edit_tool.decision", func(t *testing.T) {
		t.Parallel()
		s := normalizeOneMetric(t, "code_edit_tool_decision.json")
		require.Equal(t, "claude_code.code_edit_tool.decision", s.Name)
		require.Equal(t, "Edit", s.Attrs["tool_name"])
		require.Equal(t, "accept", s.Attrs["decision"])
		require.Equal(t, "config", s.Attrs["source"])
	})

	t.Run("active_time.total", func(t *testing.T) {
		t.Parallel()
		s := normalizeOneMetric(t, "active_time_total.json")
		require.Equal(t, "claude_code.active_time.total", s.Name)
		require.Equal(t, temporalityDelta, s.Temporality)
		require.InDelta(t, 12.5, s.Value, 1e-9, "AsDouble value")
		require.Equal(t, "user", s.Attrs["type"])
	})
}

// TestFromOTLPMetrics_StoreAnything covers SPEC §1.8's "unknown metric name
// is stored, never rejected, never rolled up" — a Go-level rollup allow-list
// does not exist in this package at all (verified by these two cases using
// completely made-up metric names and still succeeding).
func TestFromOTLPMetrics_StoreAnything(t *testing.T) {
	t.Parallel()

	t.Run("unknown Gauge metric", func(t *testing.T) {
		t.Parallel()
		s := normalizeOneMetric(t, "unknown_metric_gauge.json")
		require.Equal(t, "claude_code.some_future_metric.gauge", s.Name)
		require.Equal(t, temporalityGauge, s.Temporality)
		require.InDelta(t, 3.14, s.Value, 1e-9)
		require.Equal(t, "sprocket", s.Attrs["widget"])
	})

	t.Run("unknown Histogram metric yields _sum/_count pair", func(t *testing.T) {
		t.Parallel()
		data := loadMetricsFixture(t, "unknown_metric_histogram.json")
		n := newTestNormalizer()
		samples, rejections := n.FromOTLPMetrics(data)
		require.Empty(t, rejections)
		require.Len(t, samples, 2, "AC: a histogram yields the _sum/_count pair")

		byName := map[string]model.MetricSample{}
		for _, s := range samples {
			byName[s.Name] = s
		}

		sumSample, ok := byName["claude_code.some_future_metric.duration_sum"]
		require.True(t, ok)
		require.InDelta(t, 123.45, sumSample.Value, 1e-9)

		countSample, ok := byName["claude_code.some_future_metric.duration_count"]
		require.True(t, ok)
		require.InDelta(t, float64(5), countSample.Value, 1e-9)

		// Lead note 2: the two rows must not collide under (ts, series_hash,
		// dedup_key) — verified here by the two names hashing differently.
		require.NotEqual(t, sumSample.SeriesHash, countSample.SeriesHash)
		require.NotEqual(t, sumSample.DedupKey, countSample.DedupKey)
	})

	t.Run("Histogram data point with no sum yields only _count, never a bogus 0 _sum", func(t *testing.T) {
		t.Parallel()
		data := loadMetricsFixture(t, "unknown_metric_histogram_no_sum.json")
		n := newTestNormalizer()
		samples, rejections := n.FromOTLPMetrics(data)
		require.Empty(t, rejections)
		require.Len(t, samples, 1, "no _sum sample when OTLP's optional sum field is absent")
		require.Equal(t, "claude_code.some_future_metric.duration_nosum_count", samples[0].Name)
		require.InDelta(t, float64(3), samples[0].Value, 1e-9)
	})
}

// TestFromOTLPMetrics_Temporality covers lead note 3's UNSPECIFIED mapping.
func TestFromOTLPMetrics_Temporality(t *testing.T) {
	t.Parallel()

	s := normalizeOneMetric(t, "unspecified_temporality.json")
	require.Equal(t, temporalityUnspecified, s.Temporality,
		"AGGREGATION_TEMPORALITY_UNSPECIFIED must map to an honest, distinct value, never silently forced to delta or cumulative")
}

// TestFromOTLPMetrics_Rejections covers this normalizer's two rejection
// cases (package doc comment on FromOTLPMetrics): an unsupported metric
// aggregation type, and a structurally invalid NumberDataPoint. Both must
// leave the rest of the batch intact.
func TestFromOTLPMetrics_Rejections(t *testing.T) {
	t.Parallel()

	t.Run("unsupported Summary aggregation type", func(t *testing.T) {
		t.Parallel()
		data := loadMetricsFixture(t, "unsupported_summary_type.json")
		n := newTestNormalizer()
		samples, rejections := n.FromOTLPMetrics(data)
		require.Empty(t, samples)
		require.Len(t, rejections, 1)
		require.Contains(t, rejections[0].Reason, "unsupported metric aggregation type")
		require.Equal(t, "claude_code.some_future_metric.summary", rejections[0].Record["metric.name"])
	})

	t.Run("mixed batch: rejection never discards the rest of the batch", func(t *testing.T) {
		t.Parallel()
		data := loadMetricsFixture(t, "mixed_batch_with_unsupported.json")
		n := newTestNormalizer()
		samples, rejections := n.FromOTLPMetrics(data)
		require.Len(t, rejections, 1)
		require.Len(t, samples, 1)
		require.Equal(t, "claude_code.commit.count", samples[0].Name)
	})

	t.Run("NumberDataPoint missing both as_double and as_int", func(t *testing.T) {
		t.Parallel()
		data := loadMetricsFixture(t, "number_datapoint_missing_value.json")
		n := newTestNormalizer()
		samples, rejections := n.FromOTLPMetrics(data)
		require.Empty(t, samples)
		require.Len(t, rejections, 1)
		require.Contains(t, rejections[0].Reason, "neither as_double nor as_int")
		require.Equal(t, "claude_code.session.count", rejections[0].Record["metric.name"])
	})
}

// TestFromOTLPMetrics_NilData mirrors FromOTLPLogs's nil-input contract:
// never panics, returns empty slices.
func TestFromOTLPMetrics_NilData(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer()
	samples, rejections := n.FromOTLPMetrics(nil)
	require.Empty(t, samples)
	require.Empty(t, rejections)
}

// TestFromOTLPMetrics_UnmarshalableAttrs exercises buildSample's and
// seriesHash's fallback branches: a DoubleValue attribute holding NaN is
// legal on the wire (OTLP places no constraint on a double attribute value)
// but encoding/json refuses to marshal NaN/Inf, so both
// model.DedupKeyMetric and seriesHash must fall back to their documented
// deterministic, name-scoped substitutes rather than erroring or panicking.
func TestFromOTLPMetrics_UnmarshalableAttrs(t *testing.T) {
	t.Parallel()

	data := &metricspb.MetricsData{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "claude_code.unmarshalable_attrs",
					Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
						DataPoints: []*metricspb.NumberDataPoint{{
							TimeUnixNano: uint64(fixedNow.UnixNano()),
							Attributes: []*commonpb.KeyValue{{
								Key:   "weird",
								Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: math.NaN()}},
							}},
							Value: &metricspb.NumberDataPoint_AsInt{AsInt: 1},
						}},
					}},
				}},
			}},
		}},
	}

	n := newTestNormalizer()
	samples, rejections := n.FromOTLPMetrics(data)
	require.Empty(t, rejections)
	require.Len(t, samples, 1)
	require.Equal(t, "metric:unhashable:claude_code.unmarshalable_attrs", samples[0].DedupKey)
	require.NotEmpty(t, samples[0].SeriesHash)
}

// TestFromOTLPMetrics_Clamping asserts a badly-skewed metric point still
// lands under the clamped "now" timestamp (SPEC §1.2/§2.3: metric_samples
// has no DEFAULT partition, so an unclamped point would be an insert error)
// rather than being silently dropped — the property lead note 6 requires.
func TestFromOTLPMetrics_Clamping(t *testing.T) {
	t.Parallel()

	ancientNano := uint64(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	data := &metricspb.MetricsData{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "claude_code.session.count",
					Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
						AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
						IsMonotonic:            true,
						DataPoints: []*metricspb.NumberDataPoint{{
							TimeUnixNano: ancientNano,
							Value:        &metricspb.NumberDataPoint_AsInt{AsInt: 1},
						}},
					}},
				}},
			}},
		}},
	}

	n := newTestNormalizer()
	samples, rejections := n.FromOTLPMetrics(data)
	require.Empty(t, rejections, "a clock-skewed point is clamped, not rejected")
	require.Len(t, samples, 1)
	require.True(t, samples[0].TS.Equal(fixedNow), "an out-of-window point is clamped to now so it always addresses an existing partition")
}
