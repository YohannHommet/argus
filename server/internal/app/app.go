// Package app wires config → store → httpapi (docs/SPEC.md §3.1) and owns
// the process lifecycle: constructing the storage layer, running migrations
// when configured, and running the HTTP server through the graceful
// shutdown sequence in SPEC §3.8. It is the only package allowed to know
// about every other layer at once; httpapi, store, and config each stay
// ignorant of it.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/YohannHommet/argus/server/internal/config"
	"github.com/YohannHommet/argus/server/internal/httpapi"
	"github.com/YohannHommet/argus/server/internal/ingest"
	"github.com/YohannHommet/argus/server/internal/ingest/hooks"
	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
	"github.com/YohannHommet/argus/server/internal/ingest/otlp"
	"github.com/YohannHommet/argus/server/internal/store/postgres"
	"github.com/YohannHommet/argus/server/internal/stream"
)

// statsBroadcastInterval is SPEC §5.1's fixed 2s `event: stats` cadence.
// Config's table (SPEC §3.7) is normative and complete and lists no key for
// it — same reasoning as jobs.go's partitionJobInterval and New's own
// ImportPrices call below ("adding an unlisted ARGUS_* key would be the
// larger deviation") — so this is a constant, not a config field.
const statsBroadcastInterval = 2 * time.Second

// Option configures an optional aspect of New's construction. The zero
// value (no options passed) is production behaviour in every case.
type Option func(*options)

type options struct {
	registerer prometheus.Registerer
}

// WithRegisterer overrides the Prometheus registerer the ingest pipeline's
// metrics register against (default: prometheus.DefaultRegisterer, via
// ingest.NewMetrics's own nil-means-default convention). Production never
// needs this; a test process that constructs more than one App (P2-13's
// end-to-end test, which starts a second App with a deliberately tiny
// ingest queue for its load-capacity check) does, since the default
// registry is a package-level global and registering the same metric names
// on it twice panics.
func WithRegisterer(reg prometheus.Registerer) Option {
	return func(o *options) { o.registerer = reg }
}

// App is a constructed, ready-to-serve Argus process: a live database pool
// wrapped in the postgres Store, and the readiness flag Serve's shutdown
// sequence flips.
type App struct {
	cfg    *config.Config
	logger *slog.Logger
	store  *postgres.Store
	ready  *httpapi.ReadyState

	partitions *PartitionJob    // started by Serve
	rollups    *RollupJob       // started by Serve (P3-05)
	retention  *RetentionJob    // started by Serve (P3-10)
	sweep      *SweepJob        // started by Serve (SPEC §2.4 abandoned-session sweep)
	ingest     *ingest.Pipeline // drained by Serve's shutdown sequence (drainIngest)
	hooks      *hooks.Mounter   // P2-11: POST /ingest/hook, wired into httpapi.Deps.HookMounter by Serve
	otlp       *otlp.Handler    // P2-10: POST /v1/{logs,metrics,traces}, wired into httpapi.Deps.OTLPMounter by Serve

	// hub is P5-03's live-stream broker (SPEC §5.3), wired into
	// httpapi.Deps.Stream by Serve and shut down FIRST in shutdown() — see
	// serve.go's shutdown ordering doc comment for why that order (not the
	// SPEC §3.8 step numbering's literal order) is load-bearing.
	hub *stream.Hub

	// publisher is the ingest Publisher seam's (pipeline.go:108-148) real
	// implementation: it fans persisted events into hub as they're flushed,
	// and separately runs its own debounced `session`-frame loop (Run,
	// started by Serve alongside the other scheduler-shaped jobs).
	publisher *ingest.HubPublisher

	// stats is the SPEC §5.1 2s `event: stats` broadcaster; Run started by
	// Serve alongside publisher.
	stats *stream.StatsBroadcaster

	server *http.Server // set by Serve

	// listenAddr/addrReady let a caller that started Serve on an ephemeral
	// port (cfg.HTTPAddr == "…:0", e.g. P2-13's end-to-end test) discover
	// the OS-assigned real address instead of guessing a free port ahead of
	// time and racing Serve's own bind — see Addr/Listening.
	listenAddr string
	addrReady  chan struct{}
}

// New connects the database pool, wraps it in the postgres Store, and runs
// migrations when ARGUS_AUTO_MIGRATE is true (SPEC §3.7, §3.8) — all before
// the HTTP listener exists, so a `serve` that fails to migrate never starts
// accepting traffic. It then ensures the retention-horizon-through-two-
// months-ahead partitions exist (SPEC §2.4's "startup fails loudly if the
// current month's partition cannot be created", plus the P3-12 backward
// reach to ARGUS_RETENTION_RAW_DAYS) for the same reason: an ingest request
// landing before the first hourly PartitionJob tick — whether newly
// arrived or an in-retention backfill crossing a month boundary — must
// never hit a missing partition. It does not start serving; call Serve for
// that.
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger, opts ...Option) (*App, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return nil, fmt.Errorf("app: connecting to database: %w", err)
	}

	// WithRollupSessionRemarkMax threads ARGUS_ROLLUP_SESSION_REMARK_MAX
	// (SPEC §2.4, §3.7) into the store without postgres importing
	// internal/config (depguard, SPEC §3.1) — see pool.go's Option doc.
	st := postgres.New(pool, postgres.WithRollupSessionRemarkMax(cfg.RollupSessionRemarkMax))

	if cfg.AutoMigrate {
		if err := st.Migrate(ctx); err != nil {
			pool.Close()
			return nil, fmt.Errorf("app: running migrations: %w", err)
		}
	}

	// Seed model_prices from the price table embedded in this binary
	// (db/prices/*.json), immediately after migrations and for the same
	// reason: without it the table is empty on every fresh deployment, and an
	// empty table means pricing.Estimate can never resolve a price, so
	// cost_estimated_usd and estimated_share are silently 0 forever — the
	// exact silent zero SPEC §4.1 exists to forbid, on the one number the UI
	// uses to flag that a cost is estimated rather than reported.
	//
	// `argusd prices import` (SPEC §3.8) stays as the operator-facing way to
	// re-import or update, but it cannot be the only way: nothing in
	// docker-compose or the quickstart runs it, and a `docker compose up`
	// deployment reported estimated_usd = 0 with a populated events table
	// until this call existed. The import is idempotent (ON CONFLICT with an
	// IS DISTINCT FROM guard, so a re-run touches no rows) and only writes
	// the repo-sourced rows, leaving operator-supplied ones alone.
	//
	// Not gated behind a new config key: SPEC §3.7's table is normative and
	// complete, and adding an unlisted ARGUS_* key would be the larger
	// deviation. A failure here is fatal for the same reason a failed
	// migration is — starting up with prices missing produces wrong numbers
	// rather than an obvious error.
	priceSummary, priceErr := st.ImportPrices(ctx)
	if priceErr != nil {
		pool.Close()
		return nil, fmt.Errorf("app: importing the embedded model price table: %w", priceErr)
	}
	logger.Info("model prices imported",
		"inserted", priceSummary.Inserted, "updated", priceSummary.Updated, "unchanged", priceSummary.Unchanged)

	now := time.Now()
	retention := time.Duration(cfg.RetentionRawDays) * 24 * time.Hour
	if err := st.EnsurePartitions(ctx, now.Add(-retention), now.Add(partitionJobHorizon)); err != nil {
		pool.Close()
		return nil, fmt.Errorf("app: ensuring startup partitions: %w", err)
	}

	// P3-05: the rollup job (SPEC §2.4). Its metrics follow the same
	// o.registerer plumbing as the ingest pipeline/hooks handler below, for
	// the identical reason (a test process constructing a second App must
	// not panic registering the same metric names on the default registry
	// twice).
	var rollupMetrics *RollupJobMetrics
	if o.registerer != nil {
		rollupMetrics = NewRollupJobMetrics(o.registerer)
	}
	rollupJob := NewRollupJob(st, logger, rollupMetrics, cfg.RollupInterval, cfg.RollupMaxBuckets)

	// P5-03: the hub must exist BEFORE the ingest pipeline, resolving the
	// ordering problem the Publisher seam otherwise has (the pipeline needs
	// a Publisher at construction time — New starts its workers immediately,
	// its own doc comment — but a HubPublisher needs a hub to publish into,
	// so the hub is built first and st, already constructed above, is what
	// HubPublisher's debounce loop reads session projections through). Its
	// own registerer follows the identical o.registerer plumbing as
	// rollupMetrics/ingestOpts, for the identical reason: a test process
	// constructing a second App must not panic registering
	// argus_stream_* twice on the default registry.
	hubOpts := []stream.Option{
		stream.WithBuffer(cfg.StreamBuffer),
		stream.WithMaxSubscribers(cfg.StreamMaxSubscribers),
		stream.WithLogger(logger),
	}
	if o.registerer != nil {
		hubOpts = append(hubOpts, stream.WithRegisterer(o.registerer))
	}
	hub := stream.New(hubOpts...)

	publisher := ingest.NewHubPublisher(hub, st, ingest.WithHubPublisherLogger(logger))

	ingestOpts := []ingest.Option{ingest.WithLogger(logger), ingest.WithPublisher(publisher)}
	if o.registerer != nil {
		ingestOpts = append(ingestOpts, ingest.WithRegisterer(o.registerer))
	}
	// The pipeline deliberately does NOT take New's ctx. Its context is
	// Background-derived and owned by Pipeline.Close (see Pipeline.ctx's doc
	// in internal/ingest/pipeline.go): Close cancels it only if the drain
	// deadline is exceeded, which is what unblocks a worker parked inside a
	// store call instead of leaking it. Tying the pipeline's lifetime to this
	// startup context would defeat the shutdown sequence outright — Serve's
	// shutdown() derives its HTTP and drain budgets from Background for the
	// same reason, precisely so a cancelled parent context cannot make the
	// drain discard queued batches with a healthy database (SPEC §3.8, audit
	// finding M4). contextcheck cannot see that ownership boundary; it began
	// flagging this call once the drain gained a per-attempt WithTimeout.
	//nolint:contextcheck // the pipeline's lifetime is owned by Close, not by New's caller — see above
	ing := ingest.New(st, ingestPipelineConfig(cfg), ingestOpts...)

	// P2-11: the hooks webhook (SPEC §3.5). hookNormalizer is built here
	// (not inside internal/ingest/hooks) because it needs
	// ARGUS_RETENTION_RAW_DAYS/ARGUS_INGEST_HOOK_ALLOW_MESSAGE_DISPLAY —
	// internal/ingest/hooks must not import internal/config (same
	// config-free-at-the-leaf convention ing's construction above follows).
	// ing satisfies hooks.Enqueuer structurally via EnqueueEvents; passing
	// it here rather than *ingest.Pipeline directly would gain nothing
	// since httpapi.RequireIngestToken already closes the httpapi seam.
	hookNormalizer := normalize.NewHookNormalizer(
		time.Now,
		time.Duration(cfg.RetentionRawDays)*24*time.Hour,
		cfg.IngestHookAllowMessageDisplay,
	)
	hookHandlerOpts := []hooks.Option{hooks.WithLogger(logger)}
	if o.registerer != nil {
		hookHandlerOpts = append(hookHandlerOpts, hooks.WithRegisterer(o.registerer))
	}
	hookHandler := hooks.NewHandler(ing, hookNormalizer, cfg.IngestMaxBodyBytes, hookHandlerOpts...)
	hookMounter := hooks.NewMounter(hookHandler, httpapi.RequireIngestToken(cfg.IngestToken))

	// P2-10: the OTLP/HTTP receiver (SPEC §3.4). otlpNormalizer is built
	// here for the same config-free-at-the-leaf reason as hookNormalizer
	// above (internal/ingest/otlp must not import internal/config); ing
	// satisfies otlp.Enqueuer structurally via EnqueueEvents/EnqueueMetrics.
	otlpNormalizer := normalize.NewNormalizer(time.Now, time.Duration(cfg.RetentionRawDays)*24*time.Hour)
	otlpHandler := otlp.New(ing, otlpNormalizer, cfg.IngestMaxBodyBytes, httpapi.RequireIngestToken(cfg.IngestToken), logger, o.registerer)

	// P5-03: the 2s stats broadcaster (SPEC §5.1). Its SnapshotFunc closure
	// is what lets internal/stream stay ignorant of *ingest.Pipeline/
	// *postgres.Store (depguard) while still combining pipeline metrics,
	// queue depth, active-session count, and BOTH drop counters into one
	// Snapshot — see newStatsSnapshotFunc's own doc comment for the
	// DroppedTotal-is-a-sum reasoning.
	statsBroadcaster := stream.NewStatsBroadcaster(hub, newStatsSnapshotFunc(ing, st.ActiveSessionCount), statsBroadcastInterval, logger)

	return &App{
		cfg:        cfg,
		logger:     logger,
		store:      st,
		ready:      httpapi.NewReadyState(),
		partitions: NewPartitionJob(st, logger, cfg.RetentionRawDays),
		rollups:    rollupJob,
		retention:  NewRetentionJob(st, logger, cfg.RetentionRawDays, cfg.DedupWindow, cfg.RetentionSessionDays, cfg.RetentionHour),
		sweep:      NewSweepJob(st, logger, cfg.SweepInterval, cfg.SessionIdleTimeout),
		ingest:     ing,
		hooks:      hookMounter,
		otlp:       otlpHandler,
		hub:        hub,
		publisher:  publisher,
		stats:      statsBroadcaster,
		addrReady:  make(chan struct{}),
	}, nil
}

// newStatsSnapshotFunc closes over the ingest pipeline and the store's
// active-session count — internal/app is the only package allowed to know
// about both at once (package doc comment), which is exactly why
// internal/stream's SnapshotFunc/Snapshot types exist: StatsBroadcaster
// itself never imports internal/ingest or internal/store/postgres (SPEC §3.1
// depguard).
//
// activeSessions is taken as a function rather than the *postgres.Store it
// comes from so this composition is unit-testable without a live database.
// That matters more than it looks: the DroppedTotal decision documented below
// has no observable consequence anywhere else, so without a test able to call
// this function directly, a future edit could silently change the meaning of
// an operator-facing metric with the whole suite still green.
func newStatsSnapshotFunc(ing *ingest.Pipeline, activeSessions func(context.Context) (int64, error)) stream.SnapshotFunc {
	return func(ctx context.Context) (stream.Snapshot, error) {
		active, err := activeSessions(ctx)
		if err != nil {
			return stream.Snapshot{}, fmt.Errorf("app: stats snapshot: active session count: %w", err)
		}
		lagSum, lagCount := ing.Metrics().LagObservations()
		return stream.Snapshot{
			QueueDepth:     ing.QueueDepth(),
			EventsTotal:    ing.Metrics().EventsTotal(),
			LagSum:         lagSum,
			LagCount:       lagCount,
			ActiveSessions: int(active),
			// DroppedTotal is ingest drops ONLY — events that never reached
			// storage at all (queue-full shedding, a permanent write error, a
			// drain-deadline timeout; see Metrics.Dropped's own doc comment).
			// It deliberately does NOT include hub.DroppedTotal(), even though
			// that is also real loss, because the two are not the same kind of
			// fact and this field is the one an operator alerts on:
			//
			//   - an ingest drop is permanent. No reconnect can recover it,
			//     because nothing was ever stored to replay.
			//   - a hub drop means the event IS stored and this subscriber's
			//     own SSE buffer merely fell behind. SPEC §5.1 already gives
			//     that its own dedicated channel — `event: lag`, "{dropped: N}
			//     when a subscriber's buffer overflowed" — which the client
			//     answers by refetching, and it is per-subscriber, which the
			//     process-wide hub counter is not.
			//
			// Summing them would make a self-healing display-layer condition
			// indistinguishable from permanent data loss in the single number
			// the data-quality screen's dropped tile reports (deviation D-28
			// names StreamStatsFrame.dropped_total as that tile's future
			// backing field). Fleet-wide hub-drop health remains available to
			// an operator as argus_stream_dropped_total on /metrics.
			DroppedTotal: int64(ing.Metrics().DroppedCount()),
		}, nil
	}
}

// ingestPipelineConfig maps the ARGUS_INGEST_* config keys onto the ingest
// pipeline's own config struct. It exists as a named function purely so the
// mapping is directly testable: every field here is a config key that
// reaches production only through this one assignment, and an omitted field
// silently falls back to the pipeline's internal default rather than
// failing anything. That is the exact shape of the two integration defects
// Phase 3 shipped (docs/review/phase-3-deviations.md) — two components each
// individually correct and individually tested, with nothing covering the
// seam between them.
func ingestPipelineConfig(cfg *config.Config) ingest.PipelineConfig {
	return ingest.PipelineConfig{
		QueueCap:       cfg.IngestQueue,
		Workers:        cfg.IngestWorkers,
		BatchSize:      cfg.IngestBatchSize,
		FlushInterval:  cfg.IngestFlush,
		RetryConflict:  cfg.IngestRetryConflict,
		RetryTransient: cfg.IngestRetryTransient,
		WriteTimeout:   cfg.IngestWriteTimeout,
	}
}

// Addr returns the address Serve actually bound to, resolving an
// ephemeral ":0" port to the OS-assigned one. Only meaningful after
// Listening() has been closed; before that it is "".
func (a *App) Addr() string { return a.listenAddr }

// Listening is closed the moment Serve has bound its listener (Addr is
// valid from then on) or immediately if binding failed (Addr stays ""). A
// caller that starts Serve in a goroutine with an ephemeral port waits on
// this instead of guessing a free port ahead of time and racing Serve's own
// bind.
func (a *App) Listening() <-chan struct{} { return a.addrReady }
