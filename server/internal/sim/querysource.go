package sim

// querySources is SPEC §7.1's query_source distribution (plain data, no
// Go enum per SPEC §0). Includes invented value a_future_query_source
// (AC: outside any Argus constant).
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

// generateSessionTitleQuerySource deliberately omits prompt.id to exercise
// out-of-turn path (SPEC §7.1 §1.1).
const generateSessionTitleQuerySource = "generate_session_title"
