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
)

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

	ingestOpts := []ingest.Option{ingest.WithLogger(logger)}
	if o.registerer != nil {
		ingestOpts = append(ingestOpts, ingest.WithRegisterer(o.registerer))
	}
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
		addrReady:  make(chan struct{}),
	}, nil
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
