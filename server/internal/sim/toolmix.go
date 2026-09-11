package sim

// toolNames is SPEC §7.1's tool-call mix (Read 0.28, Edit 0.18, Bash 0.16, ...).
var toolNames = []weighted[string]{
	{prob: 0.28, val: "Read"},
	{prob: 0.18, val: "Edit"},
	{prob: 0.16, val: "Bash"},
	{prob: 0.10, val: "Grep"},
	{prob: 0.08, val: "Write"},
	{prob: 0.06, val: "Glob"},
	{prob: 0.05, val: "Task"},
	{prob: 0.04, val: "WebFetch"},
	{prob: 0.05, val: "mcp__example__query"},
}

// decisionSources is SPEC §7.1's tool_decision source distribution (six
// documented + one invented for "no Go enum" rule SPEC §0).
var decisionSources = []weighted[string]{
	{prob: 0.55, val: "config"},
	{prob: 0.05, val: "hook"},
	{prob: 0.15, val: "user_permanent"},
	{prob: 0.15, val: "user_temporary"},
	{prob: 0.08, val: "user_reject"},
	{prob: 0.02, val: "user_abort"},
	{prob: 0.02, val: "an_invented_decision_source"},
}

// toolSources is the documented tool_source vocabulary (builtin, mcp,
// sdk_host_builtin_mcp), weighted per live capture observation.
var toolSources = []weighted[string]{
	{prob: 0.75, val: "builtin"},
	{prob: 0.15, val: "mcp"},
	{prob: 0.10, val: "sdk_host_builtin_mcp"},
}
