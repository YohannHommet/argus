package sim

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// RunCLI implements the sim subcommand (SPEC §7), returning exit code.
// Shared by cmd/argus-sim/main.go and argusd (SPEC lead note 7).
func RunCLI(args []string, stdout, stderr io.Writer) int {
	code, _ := RunCLIWithReport(args, stdout, stderr)
	return code
}

// RunCLIWithReport is RunCLI's superset: same behavior plus returns *Report
// for callers that need typed fields (P2-13's e2e_ingest_test.go).
func RunCLIWithReport(args []string, stdout, stderr io.Writer) (code int, report *Report) {
	fs := flag.NewFlagSet("sim", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfg := DefaultConfig()

	seed := fs.Uint64("seed", cfg.Seed, "PCG seed driving every deterministic draw (SPEC §7.2)")
	clockOrigin := fs.String("clock-origin", "", "RFC3339 timestamp every event is anchored to (default: fixed epoch under --out/--deterministic, else now-backfill)")
	deterministic := fs.Bool("deterministic", false, "force the fixed clock-origin epoch even without --out")
	mode := fs.String("mode", string(cfg.Mode), "demo|load (SPEC §7.2)")
	out := fs.String("out", "", "write payloads to this directory instead of POSTing (fixture generation)")
	target := fs.String("target", "http://localhost:8080", "base URL of a live Argus server to POST to (ignored when --out is set)")
	otlpProtocol := fs.String("otlp-protocol", string(cfg.OTLPProtocol), "http/protobuf|http/json")
	flushImmediately := fs.Bool("flush-immediately", false, "bypass the 5s/60s batching that mirrors Claude Code's defaults")
	sessions := fs.Int("sessions", 0, "number of virtual sessions to generate (demo mode; default 25)")
	speed := fs.Float64("speed", 0, "simulated-time compression factor (demo mode; default 200)")
	backfill := fs.Duration("backfill", 0, "how far into the past demo sessions are spread (default 14d)")
	rate := fs.Float64("rate", 0, "target events/s (load mode)")
	concurrency := fs.Int("concurrency", 1, "number of concurrent workers (load mode)")
	duration := fs.Duration("duration", 0, "how long to run (load mode)")
	costMode := fs.String("cost-mode", string(cfg.CostMode), "reported|omit")
	toolDetails := fs.Bool("tool-details", true, "emit tool_parameters.file_path/subagent_type (matches OTEL_LOG_TOOL_DETAILS=1)")
	toolUseIDInHooks := fs.Bool("tool-use-id-in-hooks", false, "include tool_use_id on PreToolUse/PostToolUse hook payloads")
	toolUseIDInDecision := fs.Bool("tool-use-id-in-decision", true, "include tool_use_id on tool_decision OTel events")
	chaosDuplicates := fs.Bool("chaos-duplicates", false, "resend ~3% of sends byte-identical (SPEC §7.1: exercises the dedup ledger)")
	chaosOutOfOrder := fs.Bool("chaos-out-of-order", false, "hold ~5% of sends for a random 5-60s real delay (SPEC §7.1: late rollups via rollup_dirty)")
	chaosOrphans := fs.Bool("chaos-orphans", false, "deliver SessionStart after several turn events (SPEC §7.1: stub-on-reference + late-project rollup re-mark)")
	chaosClockSkew := fs.Bool("chaos-clock-skew", false, "skew ~2% of timestamps by up to ±1h, plus one opt-in beyond-partition-horizon event (SPEC §7.1: too_old)")
	chaosUnknown := fs.Bool("chaos-unknown", false, "emit one invented event.name per session (SPEC §7.1: exercises kind='unknown')")

	if err := fs.Parse(args); err != nil {
		return 2, nil
	}

	sessionsSet, speedSet, backfillSet := false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "sessions":
			sessionsSet = true
		case "speed":
			speedSet = true
		case "backfill":
			backfillSet = true
		}
	})

	cfg.Seed = *seed
	cfg.ClockOriginRaw = *clockOrigin
	cfg.Deterministic = *deterministic
	cfg.Mode = Mode(*mode)
	cfg.Out = *out
	cfg.Target = *target
	cfg.OTLPProtocol = OTLPProtocol(*otlpProtocol)
	cfg.FlushImmediately = *flushImmediately
	cfg.Sessions = *sessions
	cfg.Speed = *speed
	cfg.Backfill = *backfill
	cfg.Rate = *rate
	cfg.Concurrency = *concurrency
	cfg.Duration = *duration
	cfg.CostMode = CostMode(*costMode)
	cfg.ToolDetails = *toolDetails
	cfg.ToolUseIDInHooks = *toolUseIDInHooks
	cfg.ToolUseIDInDecision = *toolUseIDInDecision
	cfg.ChaosDuplicates = *chaosDuplicates
	cfg.ChaosOutOfOrder = *chaosOutOfOrder
	cfg.ChaosOrphans = *chaosOrphans
	cfg.ChaosClockSkew = *chaosClockSkew
	cfg.ChaosUnknown = *chaosUnknown
	cfg.ApplyModeDefaults(sessionsSet, speedSet, backfillSet)

	if cfg.Mode != ModeDemo && cfg.Mode != ModeLoad {
		_, _ = fmt.Fprintf(stderr, "argus-sim: unknown --mode %q (want demo or load)\n", *mode)
		return 2, nil
	}
	if cfg.OTLPProtocol != OTLPProtocolProtobuf && cfg.OTLPProtocol != OTLPProtocolJSON {
		_, _ = fmt.Fprintf(stderr, "argus-sim: unknown --otlp-protocol %q (want http/protobuf or http/json)\n", *otlpProtocol) // best-effort write; a failed write to this stream has no recovery action here
		return 2, nil
	}
	if cfg.CostMode != CostModeReported && cfg.CostMode != CostModeOmit {
		_, _ = fmt.Fprintf(stderr, "argus-sim: unknown --cost-mode %q (want reported or omit)\n", *costMode) // best-effort write; a failed write to this stream has no recovery action here
		return 2, nil
	}
	if cfg.Mode == ModeLoad {
		if cfg.Rate <= 0 {
			_, _ = fmt.Fprintln(stderr, "argus-sim: --mode=load requires --rate > 0") // best-effort write; a failed write to this stream has no recovery action here
			return 2, nil
		}
		if cfg.Duration <= 0 {
			_, _ = fmt.Fprintln(stderr, "argus-sim: --mode=load requires --duration > 0") // best-effort write; a failed write to this stream has no recovery action here
			return 2, nil
		}
	}

	var transport Transport
	if cfg.Out != "" {
		if err := os.MkdirAll(cfg.Out, 0o755); err != nil { //nolint:gosec // fixture output directory, not a security boundary
			_, _ = fmt.Fprintf(stderr, "argus-sim: %v\n", err) // best-effort write; a failed write to this stream has no recovery action here
			return 1, nil
		}
		transport = &FileTransport{Dir: cfg.Out}
	} else {
		transport = &HTTPTransport{Client: &http.Client{Timeout: 30 * time.Second}, Target: cfg.Target}
	}

	// Chaos flags decorate Transport (not runner.go's send loop) to avoid
	// changing runner.go for each new chaos concern (see chaos.go, doc.go).
	if cfg.ChaosDuplicates || cfg.ChaosOutOfOrder {
		transport = newChaosTransport(cfg, transport)
	}

	runner := NewRunner(cfg, transport)
	ctx := context.Background()
	runErr := runner.Run(ctx)
	if ct, ok := transport.(*chaosTransport); ok {
		// Wait until all held sends have fired so the exit report
		// reflects the true end state (not a snapshot with stragglers).
		ct.Wait()
	}
	if runErr != nil {
		_, _ = fmt.Fprintf(stderr, "argus-sim: %v\n", runErr) // best-effort write; a failed write to this stream has no recovery action here
		return 1, runner.Report
	}
	runner.Report.Print(stdout)

	if cfg.Out == "" && !runner.Report.AllOK() {
		return 1, runner.Report
	}
	return 0, runner.Report
}
