package normalize

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/YohannHommet/argus/server/internal/model"
)

// Closed vocabulary for metric_samples.temporality (SPEC §2.3).
// OTel values mapped onto this (not rejected), so constants don't violate SPEC §0.
const (
	temporalityDelta      = "delta"
	temporalityCumulative = "cumulative"
	temporalityGauge      = "gauge"

	// temporalityUnspecified maps OTLP UNSPECIFIED (lead note 3: map to something
	// honest, not drop). Silently forcing to "delta"/"cumulative" would lie to
	// Phase 3's rollup job about whether points should be diffed; "unspecified"
	// preserves that the exporter did not say.
	temporalityUnspecified = "unspecified"
)

// metricSeriesHashSeparator joins metric name and attrs JSON before hashing.
// SPEC §2.3 says "sha256(name + sorted attrs)" without specifying join. Separator
// prevents collisions (e.g. "foo{" metric + attrs starting "{..." from colliding).
// Argus-internal implementation detail; series_hash consumed only internally.
const metricSeriesHashSeparator = "|"

// FromOTLPMetrics implements P2-04 (SPEC §1.8, §2.3, §1.7 rule 2) end-to-end:
// walk ResourceMetrics/ScopeMetrics/Metric/data point, support Sum/Gauge/Histogram,
// compute series identity and dedup_key, apply "store raw everything" (unrecognized
// names stored like documented ones, never fed to rollup per SPEC §1.8).
// Never errors (SPEC §0: no value rejected). Missing session.id ≠ rejection
// (SPEC §1.8: NULL accepted). Only rejects structurally undecodable: unsupported
// aggregation type (Exponential/Summary/empty) or NumberDataPoint with no value.
func (n *Normalizer) FromOTLPMetrics(data *metricspb.MetricsData) ([]model.MetricSample, []Rejection) {
	var samples []model.MetricSample
	var rejections []Rejection

	if data == nil {
		return samples, rejections
	}

	nowFn := n.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	ingestedAt := nowFn()

	for _, rm := range data.GetResourceMetrics() {
		resourceAttrs := otlpAttrsToMap(rm.GetResource().GetAttributes())
		vendor := resolveVendor(resourceAttrs) // shared with FromOTLPLogs, otel_logs.go

		for _, sm := range rm.GetScopeMetrics() {
			for _, metric := range sm.GetMetrics() {
				name := metric.GetName()

				switch {
				case metric.GetSum() != nil:
					sum := metric.GetSum()
					temporality := mapTemporality(sum.GetAggregationTemporality())
					for _, dp := range sum.GetDataPoints() {
						sample, ok := n.numberSample(name, vendor, temporality, dp, resourceAttrs, ingestedAt)
						if !ok {
							rejections = append(rejections, numberDataPointRejection(name, vendor, dp))
							continue
						}
						samples = append(samples, sample)
					}

				case metric.GetGauge() != nil:
					for _, dp := range metric.GetGauge().GetDataPoints() {
						sample, ok := n.numberSample(name, vendor, temporalityGauge, dp, resourceAttrs, ingestedAt)
						if !ok {
							rejections = append(rejections, numberDataPointRejection(name, vendor, dp))
							continue
						}
						samples = append(samples, sample)
					}

				case metric.GetHistogram() != nil:
					hist := metric.GetHistogram()
					temporality := mapTemporality(hist.GetAggregationTemporality())
					for _, dp := range hist.GetDataPoints() {
						samples = append(samples, n.histogramSamples(name, vendor, temporality, dp, resourceAttrs, ingestedAt)...)
					}

				default:
					// ExponentialHistogram/Summary/empty oneof (ticket scope: Sum/Gauge/Histogram).
					rejections = append(rejections, Rejection{
						Reason: "unsupported metric aggregation type (only Sum, Gauge, and Histogram are decoded)",
						Record: map[string]any{
							"metric.name":     name,
							"resource.vendor": vendor,
						},
						// audit finding m14: Count is point count, not 1 (ExponentialHistogram w/50 points
						// reports 50, not 1, or rejectedDataPoints undercounts).
						Count: unsupportedMetricDataPointCount(metric),
					})
				}
			}
		}
	}

	return samples, rejections
}

// mapTemporality implements lead note 3's temporality mapping: OTLP's
// AggregationTemporality enum onto Argus's own delta|cumulative|gauge(
// |unspecified) text vocabulary. Gauge metrics never carry an
// AggregationTemporality at all (the Gauge message has no such field —
// callers pass temporalityGauge directly rather than through this
// function).
func mapTemporality(t metricspb.AggregationTemporality) string {
	switch t {
	case metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA:
		return temporalityDelta
	case metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE:
		return temporalityCumulative
	case metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_UNSPECIFIED:
		return temporalityUnspecified
	default:
		// Any future AggregationTemporality value this pinned protobuf
		// version does not know about — same honest fallback as UNSPECIFIED
		// rather than a guess.
		return temporalityUnspecified
	}
}

// numberSample builds one model.MetricSample from a Sum or Gauge
// NumberDataPoint, or reports ok=false when the point's value oneof is
// unset (SPEC lead note 5: "Handle both [AsDouble/AsInt]", and the doc
// package comment's rejection policy above).
func (n *Normalizer) numberSample(name, vendor, temporality string, dp *metricspb.NumberDataPoint, resourceAttrs map[string]any, ingestedAt time.Time) (sample model.MetricSample, ok bool) {
	var value float64
	switch v := dp.GetValue().(type) {
	case *metricspb.NumberDataPoint_AsDouble:
		value = v.AsDouble
	case *metricspb.NumberDataPoint_AsInt:
		value = float64(v.AsInt)
	default:
		return model.MetricSample{}, false
	}

	return n.buildSample(name, vendor, temporality, dp.GetAttributes(), resourceAttrs, dp.GetTimeUnixNano(), value, ingestedAt), true
}

// histogramSamples emits two samples per histogram datapoint: <name>_sum
// (if OTLP Sum is set) and <name>_count (always). Distinct metric names
// prevent key collision (lead note 2).
func (n *Normalizer) histogramSamples(name, vendor, temporality string, dp *metricspb.HistogramDataPoint, resourceAttrs map[string]any, ingestedAt time.Time) []model.MetricSample {
	var out []model.MetricSample

	if dp.Sum != nil {
		out = append(out, n.buildSample(name+"_sum", vendor, temporality, dp.GetAttributes(), resourceAttrs, dp.GetTimeUnixNano(), dp.GetSum(), ingestedAt))
	}
	out = append(out, n.buildSample(name+"_count", vendor, temporality, dp.GetAttributes(), resourceAttrs, dp.GetTimeUnixNano(), float64(dp.GetCount()), ingestedAt))

	return out
}

// buildSample assembles one model.MetricSample from a decoded data point's
// raw pieces (Sum, Gauge, Histogram). series_hash uses data-point attributes only
// (not resource/scope — SPEC §2.3, lead note 1). Timestamp clamped (no ClockSkewed
// column per SPEC §2.3). SessionID falls back to resourceAttrs per m12 (never merged
// into attrs/series_hash).
func (n *Normalizer) buildSample(name, vendor string, temporality string, kvs []*commonpb.KeyValue, resourceAttrs map[string]any, timeUnixNano uint64, value float64, ingestedAt time.Time) model.MetricSample {
	attrs := otlpAttrsToMap(kvs)

	rawTS := time.Unix(0, int64(timeUnixNano)).UTC()                        //nolint:gosec // uint64 wire timestamp never approaches int64 overflow within any plausible event time (same justification as otel_logs.go's resolveTimestamp)
	clampedTS, _ := model.ClampTimestamp(rawTS, ingestedAt, n.RetentionRaw) // skew signal deliberately discarded — see buildSample's doc comment

	// audit finding m12: a session.id present only on the resource (never
	// merged into a metric data point's own attrs, unlike the logs path)
	// is still worth attributing the sample to — read it here, from the
	// separate resourceAttrs map, so it never touches attrs/series_hash.
	sessionID := String(attrs, "session.id")
	if sessionID == nil || *sessionID == "" {
		sessionID = String(resourceAttrs, "session.id")
	}

	dedupKey, err := model.DedupKeyMetric(name, clampedTS, attrs)
	if err != nil {
		// (M5) unreachable post-sanitization; fallback key for defense in depth.
		dedupKey = "metric:unhashable:" + name
	}

	return model.MetricSample{
		TS:          clampedTS,
		IngestedAt:  ingestedAt,
		Name:        name,
		Vendor:      vendor,
		SessionID:   sessionID,
		Value:       value,
		Temporality: temporality,
		SeriesHash:  seriesHash(name, attrs),
		Attrs:       attrs,
		DedupKey:    dedupKey,
	}
}

// seriesHash implements SPEC §2.3's sha256(name + sorted attrs) series
// identity. Marshal error (unreachable post-M5) falls back to name alone.
func seriesHash(name string, attrs map[string]any) []byte {
	canon, err := json.Marshal(attrs)
	if err != nil {
		canon = []byte("null")
	}
	sum := sha256.Sum256([]byte(name + metricSeriesHashSeparator + string(canon)))
	return sum[:]
}

// numberDataPointRejection builds the Rejection for an invalid
// NumberDataPoint (SPEC lead note 5 / this file's rejection policy):
// Record carries enough of the point's own identity (name, attrs,
// timestamp) to debug from the API without re-decoding the original wire
// payload, matching Rejection.Record's documented purpose (rejection.go).
func numberDataPointRejection(name, vendor string, dp *metricspb.NumberDataPoint) Rejection {
	return Rejection{
		Reason: "number data point has neither as_double nor as_int set (invalid per OTLP's own data model)",
		Record: map[string]any{
			"metric.name":     name,
			"resource.vendor": vendor,
			"time_unix_nano":  dp.GetTimeUnixNano(),
			"attrs":           otlpAttrsToMap(dp.GetAttributes()),
		},
		Count: 1, // one NumberDataPoint
	}
}

// unsupportedMetricDataPointCount is audit finding m14's fix: how many data
// points the "unsupported aggregation type" Rejection above actually
// discards. ExponentialHistogram and Summary both carry a DataPoints slice
// (like Sum/Gauge/Histogram do); an empty oneof (no aggregation type set at
// all) has none to count.
func unsupportedMetricDataPointCount(metric *metricspb.Metric) int {
	if eh := metric.GetExponentialHistogram(); eh != nil {
		return len(eh.GetDataPoints())
	}
	if s := metric.GetSummary(); s != nil {
		return len(s.GetDataPoints())
	}
	return 0
}
