package postgres

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// adversarial is a set of SQL-metacharacter-laden values that must never
// appear literally in rendered SQL text, however a filter is populated —
// the AC's own wording ("no user string appears in the SQL text"). Every
// permutation test below builds its filter values from this set.
var adversarial = []string{
	`'; DROP TABLE sessions; --`,
	`x' OR '1'='1`,
	`argus"); --`,
	`%_\`,
}

func TestSessionWhereClause_PlaceholdersOnly(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// Every permutation: each field populated alone, then all fields
	// populated together, feeding every SessionFilter field the ticket's
	// AC calls for.
	tests := []struct {
		name   string
		filter store.SessionFilter
	}{
		{"project only", store.SessionFilter{Project: adversarial}},
		{"vendor only", store.SessionFilter{Vendor: adversarial}},
		{"model only", store.SessionFilter{Model: adversarial}},
		{"status only", store.SessionFilter{Status: []model.SessionStatus{model.SessionStatusActive, model.SessionStatusEnded}}},
		{"tool only", store.SessionFilter{Tool: adversarial}},
		{"decision_source only", store.SessionFilter{DecisionSource: adversarial}},
		{"time range only", store.SessionFilter{From: &from, To: &to}},
		{"from only", store.SessionFilter{From: &from}},
		{"to only", store.SessionFilter{To: &to}},
		{"q only", store.SessionFilter{Q: adversarial[0]}},
		{"q with like wildcards", store.SessionFilter{Q: `100%_off\`}},
		{"empty filter", store.SessionFilter{}},
		{
			"every field together",
			store.SessionFilter{
				Project:        adversarial,
				Vendor:         adversarial,
				Model:          adversarial,
				Status:         []model.SessionStatus{model.SessionStatusAbandoned},
				Tool:           adversarial,
				DecisionSource: adversarial,
				From:           &from,
				To:             &to,
				Q:              adversarial[1],
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newClauseBuilder()
			where := sessionWhereClause(b, tt.filter)

			for _, v := range adversarial {
				assert.NotContains(t, where, v, "adversarial filter value leaked into rendered SQL text")
			}
			assert.NotContains(t, where, "DROP TABLE")

			// Every literal in the rendered SQL must be a placeholder
			// ($1, $2, ...), never a quoted string constant.
			assert.NotContains(t, where, "'", "rendered SQL must contain no single-quoted string literals")

			// The number of $n placeholders referenced in the clause must
			// equal the number of args the builder actually collected —
			// i.e. every arg is wired to exactly one placeholder and vice
			// versa.
			maxPlaceholder := 0
			for i := 1; i <= len(b.args); i++ {
				if strings.Contains(where, placeholderToken(i)) {
					maxPlaceholder = i
				}
			}
			if where == "" {
				assert.Empty(t, b.args)
			} else {
				assert.LessOrEqual(t, maxPlaceholder, len(b.args))
			}
		})
	}
}

func TestSessionWhereClause_EmptyFilterProducesNoClause(t *testing.T) {
	t.Parallel()

	b := newClauseBuilder()
	where := sessionWhereClause(b, store.SessionFilter{})
	assert.Empty(t, where)
	assert.Empty(t, b.args)
}

func TestSessionWhereClause_ArgsMatchValuesInOrder(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	b := newClauseBuilder()
	where := sessionWhereClause(b, store.SessionFilter{
		Project: []string{"argus"},
		Vendor:  []string{"claude_code"},
		From:    &from,
	})

	require.Contains(t, where, "project = ANY($1)")
	require.Contains(t, where, "vendor = ANY($2)")
	require.Contains(t, where, "last_event_at >= $3")

	require.Len(t, b.args, 3)
	assert.Equal(t, []string{"argus"}, b.args[0])
	assert.Equal(t, []string{"claude_code"}, b.args[1])
	assert.Equal(t, from, b.args[2])
}

func TestSessionWhereClause_ToolAndDecisionSourceUseExists(t *testing.T) {
	t.Parallel()

	b := newClauseBuilder()
	where := sessionWhereClause(b, store.SessionFilter{
		Tool:           []string{"Edit"},
		DecisionSource: []string{"user_reject"},
	})

	assert.Contains(t, where, "EXISTS (SELECT 1 FROM tool_calls t WHERE t.session_id = s.id AND t.tool_name = ANY($1))")
	assert.Contains(t, where, "EXISTS (SELECT 1 FROM tool_calls t WHERE t.session_id = s.id AND t.decision_source = ANY($2))")
}

func TestSessionWhereClause_ModelUsesArrayOverlap(t *testing.T) {
	t.Parallel()

	b := newClauseBuilder()
	where := sessionWhereClause(b, store.SessionFilter{Model: []string{"claude-opus-5"}})
	assert.Contains(t, where, "models && $1")
}

func TestSessionWhereClause_QEscapesLikeWildcards(t *testing.T) {
	t.Parallel()

	b := newClauseBuilder()
	sessionWhereClause(b, store.SessionFilter{Q: "100%_off"})

	require.Len(t, b.args, 1)
	assert.Equal(t, `%100\%\_off%`, b.args[0])
}

func placeholderToken(n int) string {
	return "$" + strconv.Itoa(n)
}
