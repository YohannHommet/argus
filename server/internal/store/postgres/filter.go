// Package postgres — filter.go implements SPEC §3.3's "whitelist-based
// clause builder ... placeholders only, never interpolation" for the three
// dynamic-filter reads (ListSessions, this ticket; ListEvents and
// ListToolCalls, P3-03). clauseBuilder is the reusable primitive: a set of
// small methods that each append zero or one WHERE fragment plus its
// positional args, using only `$n` placeholders. No caller-supplied string
// is ever formatted into the returned SQL text — only into the parallel
// args slice alongside a placeholder that stands in for it.
//
// Contract every method here follows:
//
//   - Column/table names are Go string literals supplied by the calling
//     query-builder function (sessionWhereClause below, and its P3-03
//     siblings later) — never derived from request input. That is what
//     "whitelist" means: the set of columns a filter can touch is fixed by
//     the code that calls clauseBuilder, not by anything a client sends.
//   - A method that renders "OR within a field" (SPEC §4.1: repeated params
//     OR) takes a []string of values and renders ONE placeholder holding
//     the whole slice, matched with `= ANY($n)` / `&& $n`, never one
//     placeholder per value — so query plans and this file's own
//     placeholder-counting stay stable regardless of how many values a
//     field carries.
//   - Every method returns "" when it has nothing to contribute (nil/empty
//     values, a nil time, an empty string) and appends nothing to args in
//     that case — callers filter empty results out before joining "AND".
//   - AND-across-fields (SPEC §4.1) is the caller's job: join the non-empty
//     clauses with " AND ".
//
// filter_test.go feeds every SessionFilter permutation — including
// adversarial, SQL-metacharacter-laden values — through sessionWhereClause
// and asserts none of those values appear anywhere in the rendered SQL
// text, only in args.
package postgres

import (
	"fmt"
	"strings"
	"time"

	"github.com/YohannHommet/argus/server/internal/store"
)

// clauseBuilder accumulates positional query args as its methods render
// WHERE fragments, so every fragment produced by the same builder shares one
// consistent, gap-free `$n` numbering.
type clauseBuilder struct {
	args []any
}

func newClauseBuilder() *clauseBuilder {
	return &clauseBuilder{}
}

// placeholder appends v to args and returns its `$n` reference.
func (b *clauseBuilder) placeholder(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", len(b.args))
}

// anyOf renders `column = ANY($n)` — OR within the field (SPEC §4.1) — or
// "" if values is empty.
func (b *clauseBuilder) anyOf(column string, values []string) string {
	if len(values) == 0 {
		return ""
	}
	return fmt.Sprintf("%s = ANY(%s)", column, b.placeholder(values))
}

// overlapsAny renders `column && $n` for an array-typed column (e.g.
// sessions.models text[]): true when column shares at least one element
// with values (SPEC §4.1's OR-within-field, applied to a column that is
// itself a set rather than a scalar). "" if values is empty.
func (b *clauseBuilder) overlapsAny(column string, values []string) string {
	if len(values) == 0 {
		return ""
	}
	return fmt.Sprintf("%s && %s", column, b.placeholder(values))
}

// timeRange renders `column >= $n` / `column <= $n` for whichever of
// from/to is non-nil, joined with AND if both are set; "" if neither is.
func (b *clauseBuilder) timeRange(column string, from, to *time.Time) string {
	var parts []string
	if from != nil {
		parts = append(parts, fmt.Sprintf("%s >= %s", column, b.placeholder(*from)))
	}
	if to != nil {
		parts = append(parts, fmt.Sprintf("%s <= %s", column, b.placeholder(*to)))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " AND ")
}

// ilikeAny renders `(col1 ILIKE $n OR col2 ILIKE $n OR ...)`, sharing one
// placeholder across every column (SPEC §4.3's `q=` substring filter on
// id/project/cwd). q is wrapped in `%...%` and its own `%`/`_`/`\`
// metacharacters are escaped first, so a literal percent or underscore in
// q matches itself rather than acting as an ILIKE wildcard. "" if q is
// empty.
func (b *clauseBuilder) ilikeAny(columns []string, q string) string {
	if q == "" {
		return ""
	}
	ph := b.placeholder("%" + escapeLikePattern(q) + "%")
	parts := make([]string, len(columns))
	for i, c := range columns {
		parts[i] = fmt.Sprintf("%s ILIKE %s", c, ph)
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// existsAny renders a correlated EXISTS(...) clause: at least one row of
// `table` (aliased "t" inside the subquery) has `t.correlateColumn =
// outerRef` and `t.column` in values (OR within field). Used for filters
// that live on a joined table rather than directly on the row being
// filtered — e.g. ListSessions' Tool/DecisionSource filters, which are
// really "this session has a tool_calls row matching X". "" if values is
// empty.
func (b *clauseBuilder) existsAny(table, outerRef, correlateColumn, column string, values []string) string {
	if len(values) == 0 {
		return ""
	}
	ph := b.placeholder(values)
	return fmt.Sprintf(
		"EXISTS (SELECT 1 FROM %s t WHERE t.%s = %s AND t.%s = ANY(%s))",
		table, correlateColumn, outerRef, column, ph,
	)
}

// escapeLikePattern escapes the three characters that are meaningful to
// Postgres's default LIKE/ILIKE escape convention (backslash-escaped `%`
// and `_`), so a q= value containing them is matched literally rather than
// as a wildcard.
func escapeLikePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// whereAll joins the non-empty clauses with AND (SPEC §4.1's
// AND-across-fields), returning "" — no WHERE clause at all — if every
// clause was empty.
func whereAll(clauses ...string) string {
	var nonEmpty []string
	for _, c := range clauses {
		if c != "" {
			nonEmpty = append(nonEmpty, c)
		}
	}
	return strings.Join(nonEmpty, " AND ")
}

// sessionWhereClause renders store.SessionFilter (SPEC §4.3, store.go's own
// doc comment on the field semantics) into a WHERE-clause body (no "WHERE"
// keyword — read_sessions.go prefixes it, and appends the keyset predicate
// as one more AND-ed clause from the same builder) plus its positional
// args, in $n order. Returns "" if f has no active filters at all.
func sessionWhereClause(b *clauseBuilder, f store.SessionFilter) string {
	statusValues := make([]string, len(f.Status))
	for i, s := range f.Status {
		statusValues[i] = string(s)
	}

	return whereAll(
		b.anyOf("project", f.Project),
		b.anyOf("vendor", f.Vendor),
		b.overlapsAny("models", f.Model),
		b.anyOf("status", statusValues),
		b.existsAny("tool_calls", "s.id", "session_id", "tool_name", f.Tool),
		b.existsAny("tool_calls", "s.id", "session_id", "decision_source", f.DecisionSource),
		b.timeRange("last_event_at", f.From, f.To),
		b.ilikeAny([]string{"s.id", "project", "cwd"}, f.Q),
	)
}
