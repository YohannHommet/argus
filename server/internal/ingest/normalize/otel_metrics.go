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

// histogramSamples implements the ticket's "store sum/count as two samples"
// rule: <name>_sum (only when OTLP's optional Sum field is set — lead note 5:
// "Histogram sum is optional in OTLP; handle its absence without producing a
// bogus 0") and <name>_count (always — Count is not optional in the OTLP
// message, and a legitimately-zero count is a real value, not an absence, so
// it is never suppressed).
//
// Lead note 2's collision question — "how do the two rows' series_hash and
// dedup_key differ so they cannot collide under the (ts, series_hash,
// dedup_key) primary key" — is answered by treating "<name>_sum" and
// "<name>_count" as two distinct metric names, full stop: seriesHash and
// model.DedupKeyMetric both hash over (name, attrs), so two different names
// with identical attrs and ts necessarily produce two different series
// hashes and two different dedup keys without any bespoke suffixing logic
// here — the same mechanism that already distinguishes any other two
// same-timestamp, same-attrs, different-name metrics.
func (n *Normalizer) histogramSamples(name, vendor, temporality string, dp *metricspb.HistogramDataPoint, resourceAttrs map[string]any, ingestedAt time.Time) []model.MetricSample {
	var out []model.MetricSample

	if dp.Sum != nil {
		out = append(out, n.buildSample(name+"_sum", vendor, temporality, dp.GetAttributes(), resourceAttrs, dp.GetTimeUnixNano(), dp.GetSum(), ingestedAt))
	}
	out = append(out, n.buildSample(name+"_count", vendor, temporality, dp.GetAttributes(), resourceAttrs, dp.GetTimeUnixNano(), float64(dp.GetCount()), ingestedAt))

	return out
}

// buildSample assembles one model.MetricSample from a decoded data point's
// raw pieces, common to Sum, Gauge, and Histogram data points.
//
// series_hash participation (lead note 1): only the data point's own
// Attributes participate — never resource or instrumentation-scope
// attributes. Three reasons: (1) OTLP's own data model documents a
// NumberDataPoint/HistogramDataPoint's Attributes as "the set of key/value
// pairs that uniquely identify the timeseries from where this point
// belongs" — series identity is already a defined OTel concept, and it is
// exactly the data-point attributes, not the resource or scope; (2) resource
// attributes (service.version, host.arch, os.type, ...) commonly change
// across an agent's lifetime (an upgrade mid-session) or across export
// batches without the underlying timeseries changing at all — folding them
// into series_hash would fragment metric_series_state's cumulative-diff
// state (SPEC §1.8) across a resource-attribute change that has nothing to
// do with the counter it's tracking; (3) it keeps a clean, checkable
// invariant: Attrs (the stored jsonb column) and the bytes hashed into
// SeriesHash are exactly the same map, so series_hash is always
// independently reproducible from the stored row via
// sha256(name + sorted(attrs)) — no hidden extra inputs a debugger can't
// see. session.id, when Claude Code includes it, arrives as one such
// data-point attribute (OTEL_METRICS_INCLUDE_SESSION_ID) and therefore
// participates like any other attribute, with no special-casing needed.
//
// ts / clamping (lead note 6): TimeUnixNano is passed through
// model.ClampTimestamp exactly like FromOTLPLogs does, because
// metric_samples is monthly-partitioned with no DEFAULT partition (SPEC
// §2.2/§2.3) — an unclamped, badly-skewed point would be an insert error,
// not merely a data-quality flag, and dropping the point instead of storing
// it clamped would violate "never silently drop a point". Unlike
// model.Event, model.MetricSample (owned by ticket P2-01, not this ticket)
// has no ClockSkewed column, and metric_samples (SPEC §2.3) has no
// clock_skewed column either — so the skew *signal* ClampTimestamp computes
// is deliberately discarded here after being used to pick the timestamp;
// there is no column to carry it and adding one is a schema change outside
// this ticket's scope. This is a known, documented limitation, not an
// oversight: a metrics-only clock-skew data-quality view is a gap v1 accepts
// (the same events-vs-metrics asymmetry SPEC §1.8 already treats as
// acceptable for cost/token attribution).
//
// dedup_key (SPEC §1.7 rule 2) and series_hash are computed from the
// *clamped* timestamp, not the raw one: a skewed point is stored under
// ts=now, and its dedup key must match the row it is actually stored under
// (the same reasoning FromOTLPLogs's dedup key already applies implicitly by
// never taking ts as an input at all — DedupKeyMetric, unlike
// DedupKeyOTelLog, takes ts explicitly, so this function must choose which
// one, and consistency with the stored row wins).
// resourceAttrs is only ever consulted for the audit finding m12 session.id
// fallback below — never folded into attrs/series_hash, since series_hash's
// inputs "must stay exactly the stored attrs" (this ticket's caveat) and
// buildSample's own doc comment already documents resource attrs as
// deliberately excluded from series identity.
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
		// M5 audit note: this branch used to be reachable — attrs is built
		// exclusively from otlpAnyValueToGo's outputs, and a float64 holding
		// NaN or +/-Inf (OTLP permits a DoubleValue to carry either) made
		// encoding/json refuse to marshal it. otlpAnyValueToGo now sanitizes
		// every DoubleValue at decode time (otlpattrs.go's
		// sanitizeAttrFloat), replacing a non-finite value with its string
		// form before it ever reaches this map, so attrs always marshals
		// and this branch is provably unreachable. Kept as defense in depth
		// (mirroring FromOTLPLogs.buildEvent's identical fallback) rather
		// than removed, since — unlike the logs path — FromOTLPMetrics's
		// contract (this file's package doc comment) is to never turn a
		// value-level problem into an error at all.
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

// seriesHash implements SPEC §2.3's "sha256(name + sorted attrs) — series
// identity". encoding/json already marshals map[string]T with sorted keys at
// every nesting level (the same property model's canonicalJSON relies on),
// so json.Marshal(attrs) is sorted-attrs-as-bytes without a bespoke encoder.
//
// A marshal error (see buildSample's DedupKeyMetric comment for why this is
// now provably unreachable post-M5) falls back to hashing the name alone:
// still deterministic and still distinguishes this metric from every
// differently-named one, which is the best available substitute for "sorted
// attrs" when the attrs cannot be canonically rendered at all.
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
