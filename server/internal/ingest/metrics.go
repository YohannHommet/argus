package ingest

import (
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// metricsNamespace/metricsSubsystem give every ingest metric the
// "argus_ingest_*" prefix SPEC §3.6's self-metrics list names explicitly.
const (
	metricsNamespace = "argus"
	metricsSubsystem = "ingest"
)

// Metrics is the complete SPEC §3.6 self-observability surface for the
// pipeline: "queue depth gauge, batch-size histogram, per-source event
// counters, dedup counter, write-duration histogram, retry counters by
// class, argus_ingest_lag_seconds, dropped, too_old". Fields are exported so
// callers (tests, and a future /metrics-adjacent admin view) can read them
// directly with prometheus/client_golang/prometheus/testutil rather than
// scraping HTTP.
type Metrics struct {
	// QueueDepth is a per-lane gauge (label "lane": "event"|"metric") of how
	// many batches are currently buffered, read straight off len(channel) —
	// cheap enough to update on every enqueue/dequeue without a separate
	// polling goroutine.
	QueueDepth *prometheus.GaugeVec

	// BatchSize is shared by both lanes: the SPEC diagram's "accumulate
	// until size or flush" threshold applies identically to events and
	// metric samples, and a single unlabeled histogram avoids adding a
	// label whose only two values would otherwise be a lane discriminator
	// nobody queries by.
	BatchSize prometheus.Histogram

	// Events counts persisted items by source (label "source", values from
	// the closed model.Source set — never a vendor-supplied string, SPEC
	// §0). Metric samples are counted under model.SourceOTelMetric since
	// model.MetricSample carries no Source field of its own.
	Events *prometheus.CounterVec

	// Dropped counts items that never made it to storage — queue-full
	// shedding, a permanent write error, or a drain-deadline timeout — by
	// the same "source" label as Events, so "how much of source X's
	// traffic did we lose" is a single query. This is the metric SPEC §3.4
	// names as argus_ingest_dropped_total{source="hook"}.
	Dropped *prometheus.CounterVec

	// Deduped counts BatchResult.Deduped across every successful write —
	// the ingest_dedup ledger doing its job, not a failure.
	Deduped prometheus.Counter

	// TooOld counts BatchResult.TooOld — SPEC §1.7 rule 3's
	// argus_ingest_too_old_total, distinct from Dropped because "landed
	// outside retention" and "never reached storage at all" are different
	// operational conditions worth alerting on differently.
	TooOld prometheus.Counter

	// WriteDuration times each store.WriteBatch/WriteMetrics call,
	// successful or not (the timer starts before the retry loop and stops
	// when it returns), so p99 write latency reflects what a real batch
	// actually costs including any conflict backoff.
	WriteDuration prometheus.Histogram

	// Retries counts each retried attempt by class ("conflict"|"transient")
	// — incremented once per retry, not once per batch, so it reads as
	// "how much contention/instability is happening" rather than "how many
	// batches were affected".
	Retries *prometheus.CounterVec

	// WriteFailed counts batches that were ultimately dropped by the write
	// path, by class ("conflict"|"transient"|"permanent") — SPEC §3.6 names
	// argus_ingest_write_failed_total{class="permanent"} explicitly; the
	// other two classes reuse the same metric for the (rarer) case where a
	// conflict or transient error survives its whole retry budget.
	WriteFailed *prometheus.CounterVec

	// Lag observes ingested_at-ts (SPEC §3.6) per persisted event/sample,
	// not per batch: it is a single unlabeled histogram (no per-event
	// label, so no cardinality concern, SPEC §0), and per-event observation
	// gives an honest distribution — a per-batch average would hide a
	// single straggler inside an otherwise-fast batch.
	Lag prometheus.Histogram
}

// NewMetrics registers the full metric set against reg. A nil reg uses
// prometheus.DefaultRegisterer (the production default); tests that
// construct more than one Pipeline in the same process must pass a fresh
// prometheus.NewRegistry() per instance via WithRegisterer, since the
// default registry is a package-level global and registering the same
// metric name on it twice panics.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	m := &Metrics{
		QueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: metricsNamespace, Subsystem: metricsSubsystem,
			Name: "queue_depth", Help: "Batches currently buffered in the ingest queue, by lane.",
		}, []string{"lane"}),
		BatchSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: metricsNamespace, Subsystem: metricsSubsystem,
			Name: "batch_size", Help: "Number of events/samples written per store call.",
			Buckets: []float64{1, 5, 25, 100, 250, 500, 1000, 2500},
		}),
		Events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsNamespace, Subsystem: metricsSubsystem,
			Name: "events_total", Help: "Events/samples successfully persisted, by source.",
		}, []string{"source"}),
		Dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsNamespace, Subsystem: metricsSubsystem,
			Name: "dropped_total", Help: "Events/samples that never reached storage, by source.",
		}, []string{"source"}),
		Deduped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metricsNamespace, Subsystem: metricsSubsystem,
			Name: "deduped_total", Help: "Events/samples suppressed by the ingest_dedup ledger.",
		}),
		TooOld: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metricsNamespace, Subsystem: metricsSubsystem,
			Name: "too_old_total", Help: "Events/samples rejected for having no partition to land in (SPEC §1.7 rule 3).",
		}),
		WriteDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: metricsNamespace, Subsystem: metricsSubsystem,
			Name: "write_duration_seconds", Help: "store.WriteBatch/WriteMetrics call latency, including retries.",
			Buckets: prometheus.DefBuckets,
		}),
		Retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsNamespace, Subsystem: metricsSubsystem,
			Name: "retry_total", Help: "Retried write attempts, by class.",
		}, []string{"class"}),
		WriteFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsNamespace, Subsystem: metricsSubsystem,
			Name: "write_failed_total", Help: "Batches dropped by the write path, by class.",
		}, []string{"class"}),
		Lag: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: metricsNamespace, Subsystem: metricsSubsystem,
			Name: "lag_seconds", Help: "ingested_at - ts for each persisted event/sample.",
			Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60},
		}),
	}

	reg.MustRegister(
		m.QueueDepth, m.BatchSize, m.Events, m.Dropped, m.Deduped, m.TooOld,
		m.WriteDuration, m.Retries, m.WriteFailed, m.Lag,
	)
	return m
}

// EventsTotal sums argus_ingest_events_total across every "source" label
// value (Events' own doc comment: persisted items by source) into the
// single cumulative number internal/app's stats-broadcaster Snapshot needs
// (SPEC §5.1's events_per_sec is one fleet-wide rate, not a per-source
// breakdown). Read via dto.Metric.Write — the same technique
// prometheus/client_golang/prometheus/testutil uses internally — because a
// *prometheus.CounterVec has no cheap synchronous "sum every label" method
// of its own; see pipeline_test.go's metricValue doc comment for why this
// package writes against client_model directly rather than importing
// testutil (a transitive dependency this module's go.mod does not
// otherwise pull in).
func (m *Metrics) EventsTotal() float64 { return sumCounterVec(m.Events) }

// DroppedCount is EventsTotal's counterpart for argus_ingest_dropped_total
// (Dropped's own doc comment: items that never reached storage at all —
// queue-full shedding, a permanent write error, a drain-deadline timeout),
// summed the same way and for the same one-cumulative-number reason.
func (m *Metrics) DroppedCount() float64 { return sumCounterVec(m.Dropped) }

// sumCounterVec drains every label combination of v into pb and sums their
// current values. The channel is closed after one synchronous Collect call
// completes (no concurrent reader needed): every CounterVec this package
// registers has a label cardinality of two or three ("source" values), well
// under the buffer below, so Collect never blocks waiting for a reader that
// isn't there yet.
func sumCounterVec(v *prometheus.CounterVec) float64 {
	ch := make(chan prometheus.Metric, 8)
	v.Collect(ch)
	close(ch)
	var total float64
	for metric := range ch {
		var pb dto.Metric
		_ = metric.Write(&pb)
		total += pb.GetCounter().GetValue()
	}
	return total
}

// LagObservations reads argus_ingest_lag_seconds' cumulative sum/count
// straight off the histogram (Lag's own doc comment), so internal/app's
// stats broadcaster can compute a mean-lag-over-a-window
// (Snapshot.LagSum/LagCount, stream/stats.go) without importing
// prometheus/client_golang/prometheus/testutil itself — same reasoning as
// EventsTotal/DroppedCount above. A histogram collects as exactly one
// metric regardless of label cardinality (this one is unlabeled), so a
// zero-value return only happens if Collect produced nothing at all, which
// cannot happen for a *prometheus.Histogram registered by NewMetrics.
func (m *Metrics) LagObservations() (sum float64, count uint64) {
	ch := make(chan prometheus.Metric, 1)
	m.Lag.Collect(ch)
	close(ch)
	for metric := range ch {
		var pb dto.Metric
		_ = metric.Write(&pb)
		h := pb.GetHistogram()
		return h.GetSampleSum(), h.GetSampleCount()
	}
	return 0, 0
}
