package sim

// projects is the fixed project set (SPEC §7.1: argus, platform,
// micro-services/studio, dotfiles, legacy-app). legacy-app is metrics-only.
var projects = []string{"argus", "platform", "micro-services/studio", "dotfiles", "legacy-app"}

// legacyAppProject is the one project name in projects that never gets log
// events, only metric points (SPEC §7.1).
const legacyAppProject = "legacy-app"

// models is the fixed model set with weights (SPEC §7.1: 0.2/0.65/0.15).
var models = []weighted[string]{
	{prob: 0.20, val: "claude-opus-5"},
	{prob: 0.65, val: "claude-sonnet-4-5"},
	{prob: 0.15, val: "claude-haiku-4-5"},
}

// terminalTypes includes live-capture values (wsl-Ubuntu) and invented
// value (some-new-terminal) per SPEC §7.1 ("no Go enum" rule).
var terminalTypes = []string{"wsl-Ubuntu", "vscode", "iTerm.app", "some-new-terminal"}

// startTypes is SessionStart distribution (SPEC §7.1: 0.7/0.2/0.1).
var startTypes = []weighted[string]{
	{prob: 0.7, val: "fresh"},
	{prob: 0.2, val: "resume"},
	{prob: 0.1, val: "continue"},
}
