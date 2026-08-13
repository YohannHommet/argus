package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

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
