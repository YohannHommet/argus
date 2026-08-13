package sim

import "time"

// Mode selects SPEC §7.2's two run shapes: --mode=demo|load.
type Mode string

// Mode values (SPEC §7.2).
const (
	ModeDemo Mode = "demo"
	ModeLoad Mode = "load"
)

// CostMode selects SPEC §7.1's "cost_usd_micros from a built-in price
// table … --cost-mode=omit drops it to exercise the estimated-cost path".
type CostMode string

// CostMode values (SPEC §7.1).
const (
	CostModeReported CostMode = "reported"
	CostModeOmit     CostMode = "omit"
)

// OTLPProtocol selects SPEC §7.2's --otlp-protocol=http/protobuf|http/json.
type OTLPProtocol string

// OTLPProtocol values (SPEC §7.2).
const (
	OTLPProtocolProtobuf OTLPProtocol = "http/protobuf"
	OTLPProtocolJSON     OTLPProtocol = "http/json"
)

// demoDefaultSessions/demoDefaultSpeed/demoDefaultBackfill are SPEC §7.2's
// "--mode=demo (default --sessions=25 --speed=200 --backfill=14d …)".
const (
	demoDefaultSessions = 25
	demoDefaultSpeed    = 200.0
	demoDefaultBackfill = 14 * 24 * time.Hour
)

// Config is every flag SPEC §7 names, parsed once by flags.go and shared by
// cmd/argus-sim and argusd sim's `sim` subcommand (SPEC lead note 7: "two
// binaries, one implementation").
type Config struct {
	// Seed drives the single math/rand/v2.PCG every per-session RNG derives
	// from (SPEC §7.2). Default 1.
	Seed uint64

	// ClockOriginRaw is the raw --clock-origin flag value ("" if not
	// passed); ResolveClockOrigin (clock.go) applies SPEC §7.2's default
	// logic to it.
	ClockOriginRaw string
	// Deterministic is --deterministic: forces the fixed epoch origin even
	// without --out (SPEC §7.2).
	Deterministic bool

	Mode Mode

	// Out is --out=dir/: write payloads to files instead of POSTing (SPEC
	// §7.2). Empty means "POST to Target".
	Out string
	// Target is the live Argus base URL for HTTPTransport (e.g.
	// http://localhost:8080). Required when Out is empty.
	Target string

	OTLPProtocol OTLPProtocol

	// FlushImmediately bypasses the 5s (logs) / 60s (metrics) batching that
	// mirrors Claude Code's defaults (SPEC §7.2).
	FlushImmediately bool

	// Sessions is --sessions (demo mode's session count). Defaults to 25
	// when Mode==ModeDemo and unset.
	Sessions int
	// Speed compresses simulated pacing for live sends (SPEC §7.2:
	// "--speed=X compresses simulated time"). Defaults to 200 in demo mode.
	Speed float64
	// Backfill is how far into the past demo-mode sessions are spread
	// (SPEC §7.2). Defaults to 14 days in demo mode.
	Backfill time.Duration

	// Rate/Concurrency/Duration are --mode=load's knobs (SPEC §7.2:
	// "--rate=<events/s> --concurrency=N --duration=…").
	Rate        float64
	Concurrency int
	Duration    time.Duration

	CostMode CostMode

	// ToolDetails implements --tool-details (default true, matching the
	// quickstart's OTEL_LOG_TOOL_DETAILS=1, SPEC §7.1).
	ToolDetails bool
	// ToolUseIDInHooks implements --tool-use-id-in-hooks (default false,
	// SPEC §7.1: "the unverified case").
	ToolUseIDInHooks bool
	// ToolUseIDInDecision implements --tool-use-id-in-decision (default
	// true, SPEC §7.1: "live-capture-verified").
	ToolUseIDInDecision bool

	// Chaos* implement SPEC §7.1's five --chaos-* flags (chaos.go). All
	// default false and are independently switchable (P2-13 lead note 1):
	// the end-to-end test needs both a clean run (kind='unknown' = 0, no
	// dedup-triggered surprises) and each chaos path assertable on its own,
	// so no two flags may be entangled behind one knob.
	//
	// ChaosDuplicates resends ~3% of sends byte-identical (dedup ledger).
	ChaosDuplicates bool
	// ChaosOutOfOrder holds ~5% of sends for a random 5-60s real delay
	// before delivering them (late rollups via rollup_dirty).
	ChaosOutOfOrder bool
	// ChaosOrphans delivers a session's SessionStart hook after several of
	// that session's own turn events (stub-on-reference + the late-project
	// rollup re-mark, SPEC §2.4).
	ChaosOrphans bool
	// ChaosClockSkew skews ~2% of event timestamps by up to ±1h, and adds
	// one opt-in event timestamped chaosTooOldMonthsBack calendar months
	// back — legitimately inside default retention but in a month the
	// partition manager never creates ahead of time (see chaos.go's doc
	// comment for why this, not the §1.2 clamp, is the reachable path to
	// argus_ingest_too_old_total).
	ChaosClockSkew bool
	// ChaosUnknown emits one event per session whose event.name is not in
	// the §1.5.1 mapping table, exercising the kind='unknown' fallback.
	ChaosUnknown bool
}

// DefaultConfig returns Config with every SPEC §7.2 default applied except
// mode-dependent ones (Sessions/Speed/Backfill), which ApplyModeDefaults
// fills in once Mode is known — flags.go parses Mode first.
func DefaultConfig() Config {
	return Config{
		Seed:                1,
		Mode:                ModeDemo,
		OTLPProtocol:        OTLPProtocolProtobuf,
		CostMode:            CostModeReported,
		ToolDetails:         true,
		ToolUseIDInHooks:    false,
		ToolUseIDInDecision: true,
		Concurrency:         1,
	}
}

// ApplyModeDefaults fills in the fields SPEC §7.2 documents as mode-
// dependent defaults, but only when the caller left them at the zero
// value — an explicit --sessions=N (even N==0, though that is a
// degenerate run) or --speed=N must never be silently overwritten.
func (c *Config) ApplyModeDefaults(sessionsSet, speedSet, backfillSet bool) {
	if c.Mode != ModeDemo {
		return
	}
	if !sessionsSet {
		c.Sessions = demoDefaultSessions
	}
	if !speedSet {
		c.Speed = demoDefaultSpeed
	}
	if !backfillSet {
		c.Backfill = demoDefaultBackfill
	}
}
