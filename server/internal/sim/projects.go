package sim

// projects is the fixed project set SPEC §7.1 draws from: "Projects from a
// fixed set (argus, platform, micro-services/studio, dotfiles,
// legacy-app)". legacyAppProject is metrics-only (SPEC §7.1: "legacy-app is
// metrics-only (no log events) so the 'logs exporter appears off' banner
// has a demo case") — session.go checks for it by name before emitting any
// log event for a session assigned to it.
var projects = []string{"argus", "platform", "micro-services/studio", "dotfiles", "legacy-app"}

// legacyAppProject is the one project name in projects that never gets log
// events, only metric points (SPEC §7.1).
const legacyAppProject = "legacy-app"

// models is the fixed model set and weight table SPEC §7.1 specifies:
// "models from claude-opus-5, claude-sonnet-4-5, claude-haiku-4-5 weighted
// 0.2/0.65/0.15".
var models = []weighted[string]{
	{prob: 0.20, val: "claude-opus-5"},
	{prob: 0.65, val: "claude-sonnet-4-5"},
	{prob: 0.15, val: "claude-haiku-4-5"},
}

// terminalTypes is SPEC §7.1's "terminal.type is drawn from a set that
// includes undocumented values": wsl-Ubuntu is the live-capture-observed
// value outside the documented iTerm.app|vscode|cursor|tmux enum (live
// capture §4.5); iTerm.app/vscode are documented; some-new-terminal is the
// deliberately invented value exercising the "no Go enum, ever" rule (SPEC
// §0) for this column.
var terminalTypes = []string{"wsl-Ubuntu", "vscode", "iTerm.app", "some-new-terminal"}

// startTypes is SPEC §7.1's SessionStart distribution: "start_type fresh
// 0.7 / resume 0.2 / continue 0.1" (documented values, telemetry-surfaces.md
// line 26: "start_type ∈ fresh|resume|continue|agents_view").
var startTypes = []weighted[string]{
	{prob: 0.7, val: "fresh"},
	{prob: 0.2, val: "resume"},
	{prob: 0.1, val: "continue"},
}
