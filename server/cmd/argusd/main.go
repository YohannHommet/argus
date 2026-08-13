// Command argusd is Argus's server binary: flag parsing and subcommand
// dispatch only (docs/SPEC.md §3.1). Each subcommand owns its own flag.FlagSet
// (SPEC §3.8: flat flag package, no cobra).
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
  healthcheck             Check /healthz on the configured HTTP listener (exit 0/1)
`

// notImplemented is the message printed by stub subcommands. Each names the
// ticket/phase that will wire it up, per P1-02's brief. "sim" is wired
// (ticket P2-12, runSim below) and "prices" (ticket P3-04, runPrices below)
// are no longer listed here.
var notImplemented = map[string]string{
	"retention":           "not implemented yet (arrives in Phase 2: retention job)",
	"rebuild-projections": "not implemented yet (arrives in Phase 2: rollups/projections)",
}

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
	case "retention", "rebuild-projections":
		return runStub(cmd, rest)
	default:
		fmt.Fprintf(os.Stderr, "argusd: unknown command %q\n\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
}

func runStub(cmd string, args []string) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.String("config", "", "path to an optional YAML config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmt.Fprintln(os.Stderr, notImplemented[cmd])
	return 1
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

func runHealthcheck(args []string) int {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to an optional YAML config file")
	timeout := fs.Duration("timeout", 2*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, _, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argusd: %v\n", err)
		return 1
	}

	url := healthURL(cfg.HTTPAddr)
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

// healthURL turns an ARGUS_HTTP_ADDR listen address (e.g. ":8080",
// "0.0.0.0:8080", "localhost:8080") into a loopback URL for the healthcheck
// subcommand to hit, since the address itself may not be dialable (":8080"
// binds all interfaces but isn't a valid dial target).
func healthURL(addr string) string {
	host, port, ok := strings.Cut(addr, ":")
	if !ok {
		port = addr
		host = ""
	}
	if host == "" || host == "0.0.0.0" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%s/healthz", host, port)
}
