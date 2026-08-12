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
	"strings"
	"time"

	"github.com/YohannHommet/argus/server/internal/config"
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
// ticket/phase that will wire it up, per P1-02's brief.
var notImplemented = map[string]string{
	"serve":               "not implemented yet (arrives in P1-05: HTTP skeleton)",
	"migrate":             "not implemented yet (arrives in P1-04: store skeleton, embedded goose migrations)",
	"sim":                 "not implemented yet (arrives in Phase 4: traffic simulator)",
	"retention":           "not implemented yet (arrives in Phase 2: retention job)",
	"rebuild-projections": "not implemented yet (arrives in Phase 2: rollups/projections)",
	"prices":              "not implemented yet (arrives in Phase 3: model price table)",
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
	case "serve", "migrate", "sim", "retention", "rebuild-projections", "prices":
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
