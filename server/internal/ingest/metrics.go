package ingest

import (
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// SPEC §3.6: self-metrics prefix ("argus_ingest_*").
const (
	metricsNamespace = "argus"
	metricsSubsystem = "ingest"
)

// Metrics implements SPEC §3.6 self-observability (exported fields for testutil, not scraping).
type Metrics struct {
	// QueueDepth is a per-lane gauge ("event"|"metric") of buffered batches.
	QueueDepth *prometheus.GaugeVec

	// BatchSize is shared by both lanes (same threshold, unlabeled).
	BatchSize prometheus.Histogram

	// Events counts persisted items by source (label "source" from model.Source set).
	// Metric samples use model.SourceOTelMetric (no Source field on MetricSample).
	Events *prometheus.CounterVec

	// Dropped counts items never reaching storage, by source (SPEC §3.4: argus_ingest_dropped_total).
	Dropped *prometheus.CounterVec

	// Deduped counts BatchResult.Deduped across every successful write —
	// the ingest_dedup ledger doing its job, not a failure.
	Deduped prometheus.Counter

	// TooOld counts items outside retention (SPEC §1.7 rule 3), distinct from Dropped.
	TooOld prometheus.Counter

	// WriteDuration times WriteBatch/WriteMetrics calls including retries.
	WriteDuration prometheus.Histogram

	// Retries counts retried attempts by class ("conflict"|"transient"), per-retry not per-batch.
	Retries *prometheus.CounterVec

	// WriteFailed counts dropped batches by class ("conflict"|"transient"|"permanent"), per SPEC §3.6.
	WriteFailed *prometheus.CounterVec

	// Lag observes ingested_at-ts per-event (SPEC §3.6: per-event gives honest distribution).
	Lag prometheus.Histogram
}

// NewMetrics registers the metric set. Tests must pass a fresh registry via WithRegisterer.
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

// EventsTotal sums events across all sources (SPEC §5.1: one fleet-wide rate).
func (m *Metrics) EventsTotal() float64 { return sumCounterVec(m.Events) }

// DroppedCount sums dropped items across all sources.
func (m *Metrics) DroppedCount() float64 { return sumCounterVec(m.Dropped) }

// sumCounterVec sums all label combinations. Collect closes synchronously, no reader wait.
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

// LagObservations reads the histogram's sum/count for mean-lag-over-window calculation.
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
