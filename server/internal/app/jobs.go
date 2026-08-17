package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/YohannHommet/argus/server/internal/store/postgres"
)

// partitionJobInterval is the SPEC §2.4 partition-manager job's cadence
// ("Hourly"). Not configurable: §3.7's config table is complete and
// normative and does not list a key for it, so P2-05 hardcodes it rather
// than growing the config surface unilaterally.
const partitionJobInterval = time.Hour

// partitionJobHorizon is how far ahead of "now" EnsurePartitions reaches on
// every tick: the current month plus the next two (SPEC §2.4).
const partitionJobHorizon = 2 * 30 * 24 * time.Hour // ~2 months; EnsurePartitions floors to whole months anyway

// PartitionJob is the SPEC §2.4 partition-manager job: on every tick it
// calls store.EnsurePartitions for the range from the retention horizon
// (ARGUS_RETENTION_RAW_DAYS) back through two months ahead, so ingest never
// hits a missing partition. The backward reach ("Backward creation",
// decided 2026-08-13, implemented by P3-12) exists so an in-retention
// backfill crossing a month boundary never lands on a missing partition and
// gets misclassified too_old (SPEC §1.7 rule 3) — that classification stays
// reserved for data genuinely older than the retention window; widening how
// far back partitions exist does not widen what counts as too_old. The job
// is scheduler-shaped (ticker + context) rather than wired directly into
// Serve's own loop, so that whichever ticket ends up owning the full jobs
// supervisor (rollups, sweep, retention alongside this one — P2-09 in this
// codebase's plan) can start it the same way it starts the others.
//
// Single-flight: a tick that is still running when the next one fires is
// skipped rather than overlapped, since EnsurePartitions is already
// idempotent and a skipped tick just means the next one (an hour later)
// catches up. Errors are logged, never fatal — a transient DB blip should
// not crash the process; only App.New's startup call (SPEC §2.4's "startup
// fails loudly") is fatal.
type PartitionJob struct {
	store     *postgres.Store
	logger    *slog.Logger
	interval  time.Duration
	horizon   time.Duration
	retention time.Duration
	now       func() time.Time

	running sync.Mutex // held for the duration of a tick; TryLock single-flights
}

// NewPartitionJob constructs a PartitionJob using the SPEC §2.4 defaults
// (hourly, current month + 2 ahead, backward to retentionRawDays). store
// must be non-nil; logger must be non-nil. retentionRawDays is
// ARGUS_RETENTION_RAW_DAYS (config.Config.RetentionRawDays), passed as a
// plain int rather than *config.Config so internal/app's caller (app.go)
// keeps sole ownership of the config dependency, matching the
// config-free-at-the-leaf convention the hook/OTLP normalizers already
// follow (New's doc comment).
func NewPartitionJob(store *postgres.Store, logger *slog.Logger, retentionRawDays int) *PartitionJob {
	return &PartitionJob{
		store:     store,
		logger:    logger,
		interval:  partitionJobInterval,
		horizon:   partitionJobHorizon,
		retention: time.Duration(retentionRawDays) * 24 * time.Hour,
		now:       time.Now,
	}
}

// Run ticks the job on j.interval until ctx is cancelled, running one tick
// immediately on entry so a long-lived process never waits a full interval
// before its first partition check. It returns when ctx is done; there is
// nothing to drain or flush, so no separate shutdown step is needed.
func (j *PartitionJob) Run(ctx context.Context) {
	j.tick(ctx)

	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.tick(ctx)
		}
	}
}

// tick runs one EnsurePartitions pass, single-flighted against overlapping
// ticks (see the PartitionJob doc comment). The range passed is
// [now-retention, now+horizon]; EnsurePartitions floors both ends to whole
// months and CREATE TABLE/INDEX IF NOT EXISTS makes re-covering
// already-existing months a no-op.
func (j *PartitionJob) tick(ctx context.Context) {
	if !j.running.TryLock() {
		j.logger.Warn("partition job: previous tick still running, skipping")
		return
	}
	defer j.running.Unlock()

	now := j.now()
	if err := j.store.EnsurePartitions(ctx, now.Add(-j.retention), now.Add(j.horizon)); err != nil {
		j.logger.Error("partition job: ensure partitions failed", "error", err)
	}
}

// SweepJob is the SPEC §2.4/§3.8 "Abandoned-session sweep": on every tick it
// calls store.SweepAbandoned(ctx, idle) (postgres/pool.go), which moves every
// session whose status is active|unknown, has no ended_at, and has been idle
// (no new event) longer than idle to status='abandoned', via the partial
// sessions_sweep_idx (SPEC §2.1, §1.7). Before this job existed,
// SweepAbandoned had zero non-test callers, so 'abandoned' was a status a
// session could reach from nothing (SPEC §1.7's terminal states) but never
// arrived at in a running server.
//
// Shaped identically to PartitionJob/RollupJob (ticker + context,
// single-flighted by a held sync.Mutex so an overlapping tick is skipped
// rather than queued): SweepAbandoned's UPDATE is naturally idempotent — a
// session a previous tick already moved to 'abandoned' no longer matches the
// WHERE status IN ('active','unknown') clause, so a skipped tick just means
// the next one (ARGUS_SWEEP_INTERVAL later) catches whatever went idle since.
// Errors are logged, never fatal, for the same reason as the other jobs: a
// transient DB blip should not crash the process.
type SweepJob struct {
	store    *postgres.Store
	logger   *slog.Logger
	interval time.Duration
	idle     time.Duration
	now      func() time.Time

	running sync.Mutex // held for the duration of a tick; TryLock single-flights
}

// NewSweepJob constructs a SweepJob. store and logger must be non-nil.
// interval is ARGUS_SWEEP_INTERVAL and idle is ARGUS_SESSION_IDLE_TIMEOUT
// (config.Config), passed as plain values rather than *config.Config for the
// same config-free-at-the-leaf reason NewPartitionJob's doc comment gives.
func NewSweepJob(store *postgres.Store, logger *slog.Logger, interval, idle time.Duration) *SweepJob {
	return &SweepJob{
		store:    store,
		logger:   logger,
		interval: interval,
		idle:     idle,
		now:      time.Now,
	}
}

// Run ticks the job on j.interval until ctx is cancelled, running one tick
// immediately on entry — same rationale as PartitionJob.Run/RollupJob.Run: a
// long-lived process should not wait a full interval before its first sweep.
func (j *SweepJob) Run(ctx context.Context) {
	j.tick(ctx)

	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.tick(ctx)
		}
	}
}

// tick runs one SweepAbandoned pass, single-flighted in-process against
// overlapping ticks (see the SweepJob doc comment). A failure is logged,
// never fatal — the next tick (j.interval later) retries automatically.
func (j *SweepJob) tick(ctx context.Context) {
	if !j.running.TryLock() {
		j.logger.Warn("sweep job: previous tick still running, skipping")
		return
	}
	defer j.running.Unlock()

	n, err := j.store.SweepAbandoned(ctx, j.idle)
	if err != nil {
		j.logger.Error("sweep job: sweep abandoned failed", "error", err)
		return
	}
	if n > 0 {
		j.logger.Info("sweep job: marked sessions abandoned", "count", n)
	}
}

// rollupMetricsNamespace/rollupMetricsSubsystem give every rollup-job
// metric the "argus_rollup_*" prefix, matching internal/ingest/metrics.go's
// "argus_ingest_*" convention for the pipeline's own self-observability
// surface (SPEC §3.6 names this job's counters the same way).
const (
	rollupMetricsNamespace = "argus"
	rollupMetricsSubsystem = "rollup"
)

// RollupJobMetrics is RollupJob's Prometheus self-observability surface
// (P3-05 ticket note: "buckets claimed/recomputed, duration, errors,
// skipped-because-locked"). Kept in this package rather than in
// internal/store/postgres: store.Maintenance.RunRollups's signature is
// fixed (the P3-05 seam, SPEC §3.3) and must not grow a registerer
// parameter, so the job wrapper that already owns "how often" and "single-
// flight this tick" (mirroring PartitionJob) is also the natural owner of
// "how do we observe it" — RunRollups's own store.RollupStats return value
// already carries everything these metrics need per call.
type RollupJobMetrics struct {
	BucketsClaimed    prometheus.Counter
	BucketsRecomputed prometheus.Counter
	Duration          prometheus.Histogram
	Errors            prometheus.Counter
	SkippedLocked     prometheus.Counter
}

// NewRollupJobMetrics registers RollupJobMetrics against reg, following
// ingest.NewMetrics's nil-means-default-registerer idiom exactly: a nil reg
// uses prometheus.DefaultRegisterer (production), and a test process that
// constructs a second App must pass a fresh prometheus.NewRegistry() (see
// WithRegisterer) since MustRegister panics on a duplicate metric name
// against the same registry.
func NewRollupJobMetrics(reg prometheus.Registerer) *RollupJobMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	m := &RollupJobMetrics{
		BucketsClaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: rollupMetricsNamespace, Subsystem: rollupMetricsSubsystem,
			Name: "buckets_claimed_total", Help: "rollup_dirty (bucket, source) pairs claimed across every tick.",
		}),
		BucketsRecomputed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: rollupMetricsNamespace, Subsystem: rollupMetricsSubsystem,
			Name: "buckets_recomputed_total", Help: "rollup_hourly buckets fully recomputed across every tick (claimed ∪ current/previous hour, both sources).",
		}),
		Duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: rollupMetricsNamespace, Subsystem: rollupMetricsSubsystem,
			Name: "duration_seconds", Help: "RunRollups call latency, including a lock-skip's near-zero fast path.",
			Buckets: prometheus.DefBuckets,
		}),
		Errors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: rollupMetricsNamespace, Subsystem: rollupMetricsSubsystem,
			Name: "errors_total", Help: "RunRollups calls that returned a non-nil error.",
		}),
		SkippedLocked: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: rollupMetricsNamespace, Subsystem: rollupMetricsSubsystem,
			Name: "skipped_locked_total", Help: "RunRollups calls that returned immediately because another pass already held the advisory lock (SPEC §2.4 single-flight).",
		}),
	}
	reg.MustRegister(m.BucketsClaimed, m.BucketsRecomputed, m.Duration, m.Errors, m.SkippedLocked)
	return m
}

// RollupJob is the SPEC §2.4 rollup job: on every tick it calls
// store.RunRollups(ctx, maxBuckets), which claims dirty rollup_dirty
// buckets plus the current/previous hour and fully recomputes rollup_hourly/
// rollup_daily for them inside one transaction (internal/store/postgres/
// rollups.go). Shaped identically to PartitionJob (ticker + context,
// single-flighted by a held sync.Mutex so an overlapping tick is skipped
// rather than queued) since both are scheduler-owned maintenance passes
// with the same lifecycle, but RunRollups also single-flights itself at the
// Postgres level via a transaction-scoped advisory lock (rollups.go's
// rollupLockKey) — the two single-flights guard different things: this
// mutex prevents two ticks from the *same* process overlapping (cheap,
// no round trip); the advisory lock prevents two *processes* (e.g. two
// argusd replicas) from racing each other's dirty-bucket claim.
type RollupJob struct {
	store      *postgres.Store
	logger     *slog.Logger
	metrics    *RollupJobMetrics
	interval   time.Duration
	maxBuckets int
	now        func() time.Time

	running sync.Mutex // held for the duration of a tick; TryLock single-flights
}

// NewRollupJob constructs a RollupJob. store and logger must be non-nil; a
// nil metrics uses NewRollupJobMetrics(nil) (production default), matching
// New's o.registerer plumbing for the ingest pipeline/hooks handler.
// interval is ARGUS_ROLLUP_INTERVAL and maxBuckets is
// ARGUS_ROLLUP_MAX_BUCKETS (config.Config), passed as plain values rather
// than *config.Config for the same config-free-at-the-leaf reason
// NewPartitionJob's doc comment gives.
func NewRollupJob(store *postgres.Store, logger *slog.Logger, metrics *RollupJobMetrics, interval time.Duration, maxBuckets int) *RollupJob {
	if metrics == nil {
		metrics = NewRollupJobMetrics(nil)
	}
	return &RollupJob{
		store:      store,
		logger:     logger,
		metrics:    metrics,
		interval:   interval,
		maxBuckets: maxBuckets,
		now:        time.Now,
	}
}

// Run ticks the job on j.interval until ctx is cancelled, running one tick
// immediately on entry — same rationale as PartitionJob.Run: a long-lived
// process should not wait a full interval before its first rollup pass.
func (j *RollupJob) Run(ctx context.Context) {
	j.tick(ctx)

	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.tick(ctx)
		}
	}
}

// tick runs one RunRollups pass, single-flighted in-process against
// overlapping ticks (see the RollupJob doc comment's two-single-flights
// note). Errors are logged, never fatal — a transient DB blip should not
// crash the process; the next tick (j.interval later) retries automatically
// since a failed pass leaves rollup_dirty untouched (rollups.go's whole-
// transaction design).
func (j *RollupJob) tick(ctx context.Context) {
	if !j.running.TryLock() {
		j.logger.Warn("rollup job: previous tick still running, skipping")
		return
	}
	defer j.running.Unlock()

	start := j.now()
	stats, err := j.store.RunRollups(ctx, j.maxBuckets)
	j.metrics.Duration.Observe(j.now().Sub(start).Seconds())

	if err != nil {
		j.metrics.Errors.Inc()
		j.logger.Error("rollup job: run rollups failed", "error", err)
		return
	}

	if stats.BucketsClaimed == 0 && stats.BucketsRecomputed == 0 {
		// RunRollups reports zero of everything in exactly two cases: a
		// concurrent pass already held the advisory lock (SPEC §2.4's
		// single-flight, "returns immediately with zero buckets claimed and
		// no error"), or — vanishingly rarely, since the current/previous
		// hour are always recomputed — genuinely nothing to do. Counting
		// both as "skipped" would undercount real no-op ticks as lock
		// contention, but a true zero-recompute tick without a concurrent
		// rollup pass running is not a state this job can reach in
		// practice (current-hour recomputation always includes at least the
		// bucket check itself), so this metric is an accurate proxy for
		// "another pass held the lock" without RunRollups needing to widen
		// store.RollupStats to say so explicitly.
		j.metrics.SkippedLocked.Inc()
		return
	}

	j.metrics.BucketsClaimed.Add(float64(stats.BucketsClaimed))
	j.metrics.BucketsRecomputed.Add(float64(stats.BucketsRecomputed))
}

// RetentionJob is the SPEC §2.4 "Retention job": daily at ARGUS_RETENTION_HOUR
// (local time), it drops fully-expired events/metric_samples partitions
// (store.ApplyRetention, coarse — SPEC §2.2), prunes ingest_dedup rows older
// than ARGUS_DEDUP_WINDOW (store.PruneDedup), and — only when
// ARGUS_RETENTION_SESSION_DAYS > 0 (default 0 = never, m11 fix) — deletes
// sessions whose last_event_at is older than that horizon
// (store.DeleteExpiredSessions, cascading to turns/tool_calls/subagents),
// in that order. Unlike
// PartitionJob/RollupJob it does not tick on a fixed interval: Run computes
// the next occurrence of the configured local hour and sleeps until then,
// so "daily at 04:00" is honoured exactly rather than approximated by a
// short-interval poll. The `--precise` batched-delete mode (SPEC §2.2/§2.4)
// is deliberately NOT part of this automatic daily pass — it is an
// operator-invoked `argusd retention --precise` action (postgres.Store.
// ApplyRetentionPrecise, a Store-only method outside store.Maintenance),
// since SPEC's config table (§3.7) has no key that would make it a job
// default and a boundary-partition batched DELETE is heavier than the
// coarse drop this job runs unattended every day.
type RetentionJob struct {
	store            *postgres.Store
	logger           *slog.Logger
	retention        time.Duration // ARGUS_RETENTION_RAW_DAYS as a duration
	dedupWindow      time.Duration // ARGUS_DEDUP_WINDOW
	sessionRetention time.Duration // ARGUS_RETENTION_SESSION_DAYS as a duration; 0 = never (m11 fix)
	hour             int           // ARGUS_RETENTION_HOUR, local wall-clock hour
	now              func() time.Time

	running sync.Mutex // held for the duration of a tick; TryLock single-flights
}

// NewRetentionJob constructs a RetentionJob. store and logger must be
// non-nil. retentionRawDays is ARGUS_RETENTION_RAW_DAYS, dedupWindow is
// ARGUS_DEDUP_WINDOW, sessionRetentionDays is ARGUS_RETENTION_SESSION_DAYS
// (0 = never delete sessions, SPEC §2.4/§3.7's documented meaning), and hour
// is ARGUS_RETENTION_HOUR (config.Config), passed as plain values rather
// than *config.Config for the same config-free-at-the-leaf reason
// NewPartitionJob's doc comment gives.
func NewRetentionJob(store *postgres.Store, logger *slog.Logger, retentionRawDays int, dedupWindow time.Duration, sessionRetentionDays, hour int) *RetentionJob {
	return &RetentionJob{
		store:            store,
		logger:           logger,
		retention:        time.Duration(retentionRawDays) * 24 * time.Hour,
		dedupWindow:      dedupWindow,
		sessionRetention: time.Duration(sessionRetentionDays) * 24 * time.Hour,
		hour:             hour,
		now:              time.Now,
	}
}

// nextRun returns the next local wall-clock occurrence of j.hour:00:00 that
// is strictly after `after` — today's occurrence if `after` is still before
// it, otherwise tomorrow's.
func (j *RetentionJob) nextRun(after time.Time) time.Time {
	loc := after.Location()
	next := time.Date(after.Year(), after.Month(), after.Day(), j.hour, 0, 0, 0, loc)
	if !next.After(after) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// Run sleeps until the next ARGUS_RETENTION_HOUR occurrence, runs one tick,
// and repeats, until ctx is cancelled. Unlike PartitionJob.Run/RollupJob.Run
// it does NOT run a tick immediately on entry: SPEC §2.4 names an exact
// daily time, and dropping partitions/pruning the dedup ledger the instant
// any argusd process starts (including a developer's `serve` restarted
// mid-afternoon) would be a surprising side effect a maintenance job run
// once a day should not have.
func (j *RetentionJob) Run(ctx context.Context) {
	for {
		now := j.now()
		wait := j.nextRun(now).Sub(now)

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			j.tick(ctx)
		}
	}
}

// tick runs one retention pass, single-flighted in-process against
// overlapping ticks (see PartitionJob/RollupJob's identical rationale).
// ApplyRetention runs before PruneDedup, matching SPEC §2.4's "Retention
// job" bullet order; a failure in either is logged, never fatal — a
// transient DB blip should not crash the process, and the next scheduled
// run (24h later) retries.
func (j *RetentionJob) tick(ctx context.Context) {
	if !j.running.TryLock() {
		j.logger.Warn("retention job: previous tick still running, skipping")
		return
	}
	defer j.running.Unlock()

	now := j.now()

	dropped, err := j.store.ApplyRetention(ctx, now.Add(-j.retention), false)
	if err != nil {
		j.logger.Error("retention job: apply retention failed", "error", err)
	} else if len(dropped) > 0 {
		j.logger.Info("retention job: dropped expired partitions", "count", len(dropped), "partitions", dropped)
	}

	pruned, err := j.store.PruneDedup(ctx, now.Add(-j.dedupWindow))
	if err != nil {
		j.logger.Error("retention job: prune dedup failed", "error", err)
	} else if pruned > 0 {
		j.logger.Info("retention job: pruned ingest_dedup rows", "count", pruned)
	}

	// ARGUS_RETENTION_SESSION_DAYS defaults to 0, meaning "never" (SPEC
	// §3.7) — sessionRetention is the zero duration in that case, and this
	// step is skipped entirely rather than calling DeleteExpiredSessions
	// with a cutoff of "now", which would delete every session immediately
	// (m11 fix: this key used to be parsed, validated, and documented but
	// read by no code at all).
	if j.sessionRetention > 0 {
		deleted, err := j.store.DeleteExpiredSessions(ctx, now.Add(-j.sessionRetention))
		if err != nil {
			j.logger.Error("retention job: delete expired sessions failed", "error", err)
		} else if deleted > 0 {
			j.logger.Info("retention job: deleted expired sessions", "count", deleted)
		}
	}
}
