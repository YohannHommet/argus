// read_toolcalls_test.go is a black-box (package postgres_test) integration
// suite for ListToolCalls (P3-03), reusing read_sessions_test.go's
// toolCallSeed/seedToolCall and newStore/nextTestSessionID helpers rather
// than redefining them.
package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

func TestListToolCalls_SessionScoped(t *testing.T) {
	st, pool := newStore(t)
	sessionID := nextTestSessionID("tc-session")
	otherSessionID := nextTestSessionID("tc-session-other")
	seedSession(t, pool, sessionSeed{ID: sessionID})
	seedSession(t, pool, sessionSeed{ID: otherSessionID})

	base := time.Now().UTC().Add(-time.Hour)
	seedToolCall(t, pool, toolCallSeed{ID: testUUID(9001), SessionID: sessionID, ToolName: "Edit", StartedAt: base})
	seedToolCall(t, pool, toolCallSeed{ID: testUUID(9002), SessionID: otherSessionID, ToolName: "Bash", StartedAt: base.Add(time.Second)})

	got, _, err := st.ListToolCalls(context.Background(), store.ToolCallFilter{SessionID: sessionID}, store.Page{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, sessionID, got[0].SessionID)
	require.Equal(t, "Edit", got[0].ToolName)
}

func TestListToolCalls_CrossSession_ToolAndDecisionSourceFilter(t *testing.T) {
	st, pool := newStore(t)
	sessionA := nextTestSessionID("tc-a")
	sessionB := nextTestSessionID("tc-b")
	seedSession(t, pool, sessionSeed{ID: sessionA})
	seedSession(t, pool, sessionSeed{ID: sessionB})

	base := time.Now().UTC().Add(-time.Hour)
	reject := "user_reject"
	accept := "config"
	seedToolCall(t, pool, toolCallSeed{ID: testUUID(9101), SessionID: sessionA, ToolName: "Edit", DecisionSource: &reject, StartedAt: base})
	seedToolCall(t, pool, toolCallSeed{ID: testUUID(9102), SessionID: sessionB, ToolName: "Edit", DecisionSource: &accept, StartedAt: base.Add(time.Second)})
	seedToolCall(t, pool, toolCallSeed{ID: testUUID(9103), SessionID: sessionB, ToolName: "Bash", DecisionSource: &reject, StartedAt: base.Add(2 * time.Second)})

	got, _, err := st.ListToolCalls(context.Background(), store.ToolCallFilter{
		Tool:           []string{"Edit"},
		DecisionSource: []string{"user_reject"},
	}, store.Page{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, testUUID(9101), got[0].ID)
}

func TestListToolCalls_ProjectFilterJoinsSessions(t *testing.T) {
	st, pool := newStore(t)
	sessionA := nextTestSessionID("tc-proj-a")
	sessionB := nextTestSessionID("tc-proj-b")
	seedSession(t, pool, sessionSeed{ID: sessionA, Project: "argus"})
	seedSession(t, pool, sessionSeed{ID: sessionB, Project: "other-project"})

	base := time.Now().UTC().Add(-time.Hour)
	seedToolCall(t, pool, toolCallSeed{ID: testUUID(9201), SessionID: sessionA, ToolName: "Edit", StartedAt: base})
	seedToolCall(t, pool, toolCallSeed{ID: testUUID(9202), SessionID: sessionB, ToolName: "Edit", StartedAt: base.Add(time.Second)})

	got, _, err := st.ListToolCalls(context.Background(), store.ToolCallFilter{Project: []string{"argus"}}, store.Page{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, testUUID(9201), got[0].ID)
}

func TestListToolCalls_FromToBoundsStartedAt(t *testing.T) {
	st, pool := newStore(t)
	sessionID := nextTestSessionID("tc-window")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	base := time.Now().UTC().Add(-24 * time.Hour)
	seedToolCall(t, pool, toolCallSeed{ID: testUUID(9301), SessionID: sessionID, ToolName: "Edit", StartedAt: base})
	seedToolCall(t, pool, toolCallSeed{ID: testUUID(9302), SessionID: sessionID, ToolName: "Edit", StartedAt: base.Add(12 * time.Hour)})
	seedToolCall(t, pool, toolCallSeed{ID: testUUID(9303), SessionID: sessionID, ToolName: "Edit", StartedAt: base.Add(48 * time.Hour)})

	from := base.Add(time.Hour)
	to := base.Add(13 * time.Hour)
	got, _, err := st.ListToolCalls(context.Background(), store.ToolCallFilter{From: &from, To: &to}, store.Page{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, testUUID(9302), got[0].ID)
}

func TestListToolCalls_KeysetPagination_ZeroDuplicatesZeroOmissions(t *testing.T) {
	st, pool := newStore(t)
	sessionID := nextTestSessionID("tc-page")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	base := time.Now().UTC().Add(-time.Hour)
	const total = 23
	expected := map[string]struct{}{}
	for i := 0; i < total; i++ {
		id := testUUID(9400 + i)
		seedToolCall(t, pool, toolCallSeed{ID: id, SessionID: sessionID, ToolName: "Edit", StartedAt: base.Add(time.Duration(i) * time.Second)})
		expected[id] = struct{}{}
	}

	seen := map[string]int{}
	var cursor store.Cursor
	pages := 0
	for {
		pages++
		require.Lessf(t, pages, 20, "too many pages — pagination is likely looping")
		got, next, err := st.ListToolCalls(context.Background(), store.ToolCallFilter{SessionID: sessionID}, store.Page{Limit: 7, Cursor: cursor})
		require.NoError(t, err)
		for _, tc := range got {
			seen[tc.ID]++
		}
		if next == "" {
			break
		}
		cursor = next
	}

	require.Len(t, seen, total)
	for id := range expected {
		require.Equalf(t, 1, seen[id], "id %s must be returned exactly once, got %d", id, seen[id])
	}
}

func TestListToolCalls_DecodesCorrelationAndNullableFields(t *testing.T) {
	st, pool := newStore(t)
	sessionID := nextTestSessionID("tc-fields")
	seedSession(t, pool, sessionSeed{ID: sessionID})

	durationMS := 42
	seedToolCall(t, pool, toolCallSeed{
		ID: testUUID(9500), SessionID: sessionID, ToolName: "Edit",
		Correlation: string(model.CorrelationHeuristic), DurationMS: &durationMS,
	})

	got, _, err := st.ListToolCalls(context.Background(), store.ToolCallFilter{SessionID: sessionID}, store.Page{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, model.CorrelationHeuristic, got[0].Correlation)
	require.NotNil(t, got[0].DurationMS)
	require.Equal(t, durationMS, *got[0].DurationMS)
	require.Nil(t, got[0].DecidedAt)
}
