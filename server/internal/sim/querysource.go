package sim

// querySources is SPEC §7.1's mixed query_source distribution, verbatim:
// "sdk 0.45, ""(absent) 0.25, main 0.10, subagent 0.08,
// generate_session_title 0.07, auxiliary 0.03, a_future_query_source 0.02".
// This is deliberately plain data, never a Go enum (SPEC §0, §1.9): "sdk"
// and "generate_session_title" are live-capture-observed (research doc §3),
// "main"/"subagent"/"auxiliary" are documented but unobserved
// (telemetry-surfaces.md line 29), and "a_future_query_source" is the
// ticket's required invented value proving no code path can constrain this
// column (AC: "a test asserts at least one emitted query_source value is
// outside any Argus constant").
var querySources = []weighted[string]{
	{prob: 0.45, val: "sdk"},
	{prob: 0.25, val: ""}, // absent: an empty string here means "omit the attribute" (see withQuerySource)
	{prob: 0.10, val: "main"},
	{prob: 0.08, val: "subagent"},
	{prob: 0.07, val: "generate_session_title"},
	{prob: 0.03, val: "auxiliary"},
	{prob: 0.02, val: "a_future_query_source"},
}

// invalidQuerySource is the invented value this file's table can draw,
// asserted directly by fidelity_test.go's "outside any Argus constant" AC
// rather than re-deriving it from the table above.
const invalidQuerySource = "a_future_query_source"

// generateSessionTitleQuerySource is the one query_source value SPEC §7.1
// says "deliberately omit [prompt.id], exercising the out-of-turn path
// (§1.1)" — session.go checks for it by name to skip attaching prompt.id.
const generateSessionTitleQuerySource = "generate_session_title"
