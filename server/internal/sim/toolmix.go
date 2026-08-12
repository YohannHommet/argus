package sim

// toolNames is SPEC §7.1's tool-call mix, verbatim: "Read 0.28, Edit 0.18,
// Bash 0.16, Grep 0.10, Write 0.08, Glob 0.06, Task 0.05, WebFetch 0.04,
// mcp__* 0.05".
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

// decisionSources is SPEC §7.1's tool_decision source distribution:
// "source config 0.55, hook 0.05, user_permanent 0.15, user_temporary 0.15,
// user_reject 0.08, user_abort 0.02, plus a 2% draw of an invented source
// value" — the six documented values are live-capture-verified on
// tool_decision (research doc §2's observed key list includes `source`;
// SPEC §1.5.1's mapping table cites the same six-valued vocabulary). The
// seventh entry is the required invented value.
var decisionSources = []weighted[string]{
	{prob: 0.55, val: "config"},
	{prob: 0.05, val: "hook"},
	{prob: 0.15, val: "user_permanent"},
	{prob: 0.15, val: "user_temporary"},
	{prob: 0.08, val: "user_reject"},
	{prob: 0.02, val: "user_abort"},
	{prob: 0.02, val: "an_invented_decision_source"},
}

// toolSources is the documented tool_source vocabulary observed on
// tool_decision (live capture §2 attribute list includes `tool_source`;
// SPEC §1.5.1 row cites "builtin"). telemetry-surfaces.md's
// code_edit_tool.decision metric also lists `tool_source ∈
// builtin|mcp|sdk_host_builtin_mcp` for the analogous vocabulary. Weighted
// so `builtin` (the only value the live capture actually observed) is the
// common case.
var toolSources = []weighted[string]{
	{prob: 0.75, val: "builtin"},
	{prob: 0.15, val: "mcp"},
	{prob: 0.10, val: "sdk_host_builtin_mcp"},
}
