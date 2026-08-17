// Command argusd is Argus's server binary: flag parsing and subcommand
// dispatch only (docs/SPEC.md §3.1). Each subcommand owns its own flag.FlagSet
// (SPEC §3.8: flat flag package, no cobra).
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/YohannHommet/argus/server/internal/app"
	"github.com/YohannHommet/argus/server/internal/config"
	"github.com/YohannHommet/argus/server/internal/sim"
	"github.com/YohannHommet/argus/server/internal/store/postgres"
	"github.com/YohannHommet/argus/server/internal/telemetry"
)

const usage = `Usage: argusd <command> [flags]

Commands:
  serve                 Start the HTTP server, ingest pipeline, and background jobs
  migrate                Run database migrations
  sim                    Run the traffic simulator
  retention               Run the retention job once
  rebuild-projections    Rebuild rollup projections from raw events
  prices                  Manage the model price table
  config                  Print or document the effective configuration
  version                 Print version, commit, and Go runtime info
  healthcheck             Check /healthz or /readyz on the configured HTTP listener (--endpoint, exit 0/1)
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	cmd, rest := args[0], args[1:]

	switch cmd {
	case "version":
		return runVersion(rest)
	case "config":
		return runConfig(rest)
	case "healthcheck":
		return runHealthcheck(rest)
	case "migrate":
		return runMigrate(rest)
	case "serve":
		return runServe(rest)
	case "sim":
		return sim.RunCLI(rest, os.Stdout, os.Stderr)
	case "prices":
		return runPrices(rest)
	case "retention":
		return runRetention(rest)
	case "rebuild-projections":
		return runRebuildProjections(rest)
	default:
		fmt.Fprintf(os.Stderr, "argusd: unknown command %q\n\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
}

func runVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmt.Printf("argusd %s (commit %s, %s)\n", telemetry.Version, telemetry.Commit, telemetry.GoVersion())
	return 0
}

func runConfig(args []string) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to an optional YAML config file")
	fs.Bool("print", false, "print the effective config, with secrets redacted (default action)")
	markdownFlag := fs.Bool("markdown", false, "print the SPEC §3.7 reference table as markdown")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *markdownFlag {
		fmt.Print(config.Markdown())
		return 0
	}

	// --print is also the default action for `argusd config` with no flags,
	// since dumping the effective config is the common case.
	cfg, warnings, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: %v\n", err)
		return 1
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "argusd: warning: %s\n", w)
	}
	fmt.Print(cfg.Print())
	return 0
}

// runMigrate implements `argusd migrate [up|status]` (SPEC §3.8). `up` is
// the default action, matching `up` (default), `status`, `down-to N`;
// `down-to` is not wired yet — P1-04's scope is Migrate/MigrateStatus, and
// nothing in Phase 1 needs a rollback path.
func runMigrate(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to an optional YAML config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	action := "up"
	if rest := fs.Args(); len(rest) > 0 {
		action = rest[0]
	}

	cfg, _, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: %v\n", err)
		return 1
	}

	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: migrate: %v\n", err)
		return 1
	}
	defer pool.Close()

	store := postgres.New(pool)

	switch action {
	case "up":
		if err := store.Migrate(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "argusd: migrate: %v\n", err)
			return 1
		}
		fmt.Println("argusd: migrate: up to date")
		return 0
	case "status":
		statuses, err := store.MigrateStatus(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "argusd: migrate: %v\n", err)
			return 1
		}
		for _, s := range statuses {
			state := "pending"
			if s.Applied {
				state = "applied"
			}
			fmt.Printf("%d\t%s\n", s.Version, state)
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "argusd: migrate: unknown action %q (want up or status)\n", action)
		return 2
	}
}

// runPrices implements `argusd prices import` (SPEC §3.8, P3-04): reads
// the embedded/seeded server/db/prices/*.json price table and upserts it
// into model_prices, printing an inserted/updated/unchanged summary.
// ImportPrices is idempotent (ON CONFLICT (model, effective_from) DO
// UPDATE, guarded so a byte-identical re-import touches no rows), which is
// what lets a re-run of this command be a safe, repeatable operation
// rather than a one-shot seed.
func runPrices(args []string) int {
	fs := flag.NewFlagSet("prices", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to an optional YAML config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	action := "import"
	if rest := fs.Args(); len(rest) > 0 {
		action = rest[0]
	}
	if action != "import" {
		fmt.Fprintf(os.Stderr, "argusd: prices: unknown action %q (want import)\n", action)
		return 2
	}

	cfg, _, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: %v\n", err)
		return 1
	}

	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: prices: %v\n", err)
		return 1
	}
	defer pool.Close()

	summary, err := postgres.New(pool).ImportPrices(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: prices: import: %v\n", err)
		return 1
	}

	fmt.Printf("argusd: prices import: %d inserted, %d updated, %d unchanged\n",
		summary.Inserted, summary.Updated, summary.Unchanged)
	return 0
}

// runRetention implements `argusd retention` (SPEC §2.4, §3.8, P3-10): runs
// the retention job once (rather than waiting for the daily scheduled tick,
// SPEC §3.8: "retention — Run the retention job once"). --dry-run lists the
// partitions that would be dropped and changes nothing at all — not even
// ingest_dedup pruning, since SPEC's dry-run contract ("lists it and changes
// nothing") is about the whole retention pass, not just the partition drop.
// --precise additionally runs the SPEC §2.2/§2.4 batched-delete mode against
// the boundary partition after the coarse drop; it is a manual, operator-
// invoked mode, not part of the automatic daily RetentionJob (see that
// type's doc comment in internal/app/jobs.go for why).
func runRetention(args []string) int {
	fs := flag.NewFlagSet("retention", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to an optional YAML config file")
	dryRun := fs.Bool("dry-run", false, "list partitions that would be dropped, changing nothing")
	precise := fs.Bool("precise", false, "additionally batch-delete expired rows from the boundary partition (SPEC §2.2)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, _, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: %v\n", err)
		return 1
	}

	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: retention: %v\n", err)
		return 1
	}
	defer pool.Close()

	st := postgres.New(pool)
	now := time.Now()
	rawCutoff := now.Add(-time.Duration(cfg.RetentionRawDays) * 24 * time.Hour)

	dropped, err := st.ApplyRetention(ctx, rawCutoff, *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: retention: apply retention: %v\n", err)
		return 1
	}
	verb := "dropped"
	if *dryRun {
		verb = "would drop"
	}
	fmt.Printf("argusd: retention: %s %d partition(s)\n", verb, len(dropped))
	for _, name := range dropped {
		fmt.Printf("  %s\n", name)
	}

	if *dryRun {
		return 0
	}

	if *precise {
		n, preciseErr := st.ApplyRetentionPrecise(ctx, rawCutoff)
		if preciseErr != nil {
			fmt.Fprintf(os.Stderr, "argusd: retention: precise delete: %v\n", preciseErr)
			return 1
		}
		fmt.Printf("argusd: retention: precise mode deleted %d row(s) from the boundary partition\n", n)
	}

	pruned, err := st.PruneDedup(ctx, now.Add(-cfg.DedupWindow))
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: retention: prune dedup: %v\n", err)
		return 1
	}
	fmt.Printf("argusd: retention: pruned %d ingest_dedup row(s)\n", pruned)
	return 0
}

// runRebuildProjections implements `argusd rebuild-projections [--from-ts …]
// [--force]` (SPEC §1.6, §3.8, P3-10). --from-ts (RFC 3339) selects every
// session with at least one event at or after it, and rebuilds each of those
// sessions from its own true start — NOT literally "replay events from this
// point forward" — because a session that straddles --from-ts (started
// before it, still active at/after it) can only be reconstructed correctly
// from its full history; see postgres/rebuild.go's package doc (M12) for why.
// --from-ts defaults to the zero time (rebuild every session, unscoped —
// this file's original, always-safe full-rebuild behaviour). --force
// overrides the M12 safety refusal that fires when --from-ts predates the
// oldest surviving events partition (raw events before that point may
// already be gone, SPEC §2.4 retention, so the "full session history" this
// rebuilds from may itself be incomplete).
//
// Resuming an interrupted rebuild: RebuildProjectionsForce recomputes the
// affected-session set from --from-ts on every call (job_state's watermark
// only stores (ts, seq), not the original --from-ts — see the package doc)
// — so an operator resuming a scoped (--from-ts != "") rebuild MUST
// re-supply the exact same --from-ts used to start it, or the resumed call
// computes a different session set against an already-partially-rebuilt
// database and produces inconsistent results.
func runRebuildProjections(args []string) int {
	fs := flag.NewFlagSet("rebuild-projections", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to an optional YAML config file")
	fromTSFlag := fs.String("from-ts", "", "RFC 3339 timestamp; rebuilds every session active at/after it, in full, from that session's own start (default: every session — a full rebuild). Re-supply the SAME value to resume an interrupted rebuild.")
	force := fs.Bool("force", false, "proceed even if --from-ts predates the oldest surviving events partition (some raw history may already be gone)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var fromTS time.Time
	if *fromTSFlag != "" {
		parsed, err := time.Parse(time.RFC3339, *fromTSFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "argusd: rebuild-projections: invalid --from-ts: %v\n", err)
			return 2
		}
		fromTS = parsed
	}

	cfg, _, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: %v\n", err)
		return 1
	}

	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: rebuild-projections: %v\n", err)
		return 1
	}
	defer pool.Close()

	report, err := postgres.New(pool).RebuildProjectionsForce(ctx, fromTS, *force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: rebuild-projections: %v\n", err)
		return 1
	}
	fmt.Printf("argusd: rebuild-projections: destroyed and rebuilt %d session(s), %d turn(s), %d tool_call(s), %d subagent(s)\n",
		report.Sessions, report.Turns, report.ToolCalls, report.Subagents)
	fmt.Println("argusd: rebuild-projections: complete")
	return 0
}

// runServe implements `argusd serve` (SPEC §3.8): load config, build the
// App (connect + optionally migrate), then run the HTTP server until
// SIGINT/SIGTERM triggers the graceful-shutdown sequence.
func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to an optional YAML config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, warnings, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: %v\n", err)
		return 1
	}

	logger, err := telemetry.NewLogger(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: %v\n", err)
		return 1
	}
	for _, w := range warnings {
		logger.Warn(w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg, logger)
	if err != nil {
		logger.Error("argusd: serve: startup failed", "error", err)
		return 1
	}

	if err := a.Serve(ctx); err != nil {
		logger.Error("argusd: serve: shutdown error", "error", err)
		return 1
	}
	return 0
}

// runHealthcheck implements `argusd healthcheck [--endpoint=<healthz|readyz>]`
// (m35, m36, m37; the compose healthcheck contract, SPEC §3.8). --endpoint
// defaults to "healthz" (liveness only, ops.go's healthzHandler: no DB, no
// migrations, no queue check) so existing invocations keep their current
// meaning; "readyz" hits the full SPEC §3.8 readiness contract instead — the
// compose-side half of m37 (gating `condition: service_healthy` dependents,
// e.g. `sim`, on real readiness) is a separate ticket's deploy/docker-
// compose.yml change, wired against exactly this flag name.
func runHealthcheck(args []string) int {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to an optional YAML config file")
	timeout := fs.Duration("timeout", 2*time.Second, "request timeout")
	endpoint := fs.String("endpoint", "healthz", "which endpoint to probe: healthz (liveness) or readyz (full SPEC §3.8 readiness)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	switch *endpoint {
	case "healthz", "readyz":
	default:
		fmt.Fprintf(os.Stderr, "argusd: healthcheck: invalid --endpoint %q (want healthz or readyz)\n", *endpoint)
		return 2
	}

	// m35: resolve ONLY ARGUS_HTTP_ADDR, not the full config.Load, which
	// errors when ARGUS_DATABASE_URL is unset (config.go's `required:"true"`
	// tag) even though a healthcheck never touches the database. A
	// YAML-configured deployment with no DSN visible to this short-lived
	// process would otherwise report permanently unhealthy while the server
	// itself is fine. internal/config/config.go is owned by another ticket
	// and offers no way to skip just that validation, so this resolves the
	// one field it needs itself, using the same koanf precedence order
	// (defaults -> optional YAML file -> ARGUS_-prefixed env, env wins)
	// config.Load already implements.
	addr, err := healthcheckHTTPAddr(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: healthcheck: %v\n", err)
		return 1
	}

	url := healthURL(addr, *endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: healthcheck: %v\n", err)
		return 1
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "argusd: healthcheck: %s returned %d\n", url, resp.StatusCode)
		return 1
	}
	return 0
}

// defaultHTTPAddrForHealthcheck must track config.go's Config.HTTPAddr
// struct tag (`default:":8080"`) — duplicated here rather than imported
// because healthcheckHTTPAddr exists specifically to bypass config.Load
// (m35), so it cannot pull the default from a resolved Config either.
const defaultHTTPAddrForHealthcheck = ":8080"

// healthcheckHTTPAddr resolves ARGUS_HTTP_ADDR the same way config.Load
// merges it (defaults -> optional YAML file at configPath -> ARGUS_-prefixed
// environment, env wins) but skips every other field and config.Load's
// required-key validation entirely (m35: internal/config/config.go is owned
// by another ticket and has no validation-skipping entry point, so this
// duplicates just enough of Load's merge order — using the same koanf
// building blocks config.go already depends on — for the one field the
// healthcheck subcommand needs).
func healthcheckHTTPAddr(configPath string) (string, error) {
	const key = "http_addr"

	k := koanf.New(".")
	if err := k.Load(confmap.Provider(map[string]any{key: defaultHTTPAddrForHealthcheck}, "."), nil); err != nil {
		return "", fmt.Errorf("loading default %s: %w", key, err)
	}
	if configPath != "" {
		if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
			return "", fmt.Errorf("loading %s: %w", configPath, err)
		}
	}
	if err := k.Load(env.Provider(".", env.Opt{
		Prefix: "ARGUS_",
		TransformFunc: func(k, v string) (string, any) {
			return strings.ToLower(strings.TrimPrefix(k, "ARGUS_")), v
		},
	}), nil); err != nil {
		return "", fmt.Errorf("loading environment: %w", err)
	}
	return k.String(key), nil
}

// healthURL turns an ARGUS_HTTP_ADDR listen address (e.g. ":8080",
// "0.0.0.0:8080", "[::]:8080", "localhost:8080") into a loopback URL for the
// healthcheck subcommand to hit, since the address itself may not be
// dialable (":8080" binds all interfaces but isn't a valid dial target).
//
// m36: the pre-fix version split on the FIRST colon (strings.Cut), which
// mis-parses any bracketed IPv6 form — "[::]:8080" yields host "[" and port
// "]:8080", producing a URL http.NewRequestWithContext never rejects but
// that can never actually connect, so the healthcheck always failed for that
// (valid, net.Listen-accepted) address. net.SplitHostPort/net.JoinHostPort
// parse and re-quote bracketed IPv6 correctly; "::" (the unspecified IPv6
// address, ARGUS_HTTP_ADDR's IPv6 equivalent of "0.0.0.0") is mapped to
// localhost alongside the existing "" and "0.0.0.0" cases.
func healthURL(addr, endpoint string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// addr has no colon net.SplitHostPort recognises as a host/port
		// delimiter (e.g. a bare port with no leading ':', which net.Listen
		// itself would already have rejected) — best-effort fallback
		// matching the pre-m36 behaviour for this unreachable-in-practice case.
		host, port = "", addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s/%s", net.JoinHostPort(host, port), endpoint)
}
