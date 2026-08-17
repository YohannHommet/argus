package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/httpapi"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/query"
	"github.com/YohannHommet/argus/server/internal/store"
	storetest "github.com/YohannHommet/argus/server/internal/store/testing"
)

// fakeReader is this package's shared httpapi.Reader/httpapi.AnalyticsReader
// test double: storetest.Fake (P3-09's shared in-memory store.Reader
// double), consolidating what used to be three near-identical local doubles
// here, in events_test.go, and in toolcalls_test.go (P3-07/P3-08). Aliased
// under the pre-P3-09 name so every existing `&fakeReader{...}` literal in
// this package keeps working unchanged.
type fakeReader = storetest.Fake

// newTestSession is a minimal, valid *model.SessionDetail for handlers that
// only need "some session", not a specific one's fields.
func newTestSession(id string) *model.SessionDetail {
	return &model.SessionDetail{
		SessionSummary: model.SessionSummary{
			ID:          id,
			Vendor:      "claude_code",
			Status:      model.SessionStatusActive,
			LastEventAt: time.Date(2026, 8, 11, 9, 31, 44, 900_000_000, time.UTC),
			EventCount:  480,
			Tokens:      model.TokenUsage{},
			Cost:        model.SessionCost{},
			Models:      []string{},
		},
		PermissionModeHistory: []model.PermissionModeChange{},
		TopTools:              []model.ToolUsageSummary{},
		DecisionSummary:       model.SessionDecisionSummary{},
		SourcesSeen:           []model.Source{},
	}
}

func TestListSessions_RepeatedProjectParamsOR(t *testing.T) {
	t.Parallel()

	var gotFilter store.SessionFilter
	reader := &fakeReader{
		ListSessionsFunc: func(_ context.Context, f store.SessionFilter, _ store.Page) ([]model.SessionSummary, store.Cursor, error) {
			gotFilter = f
			return []model.SessionSummary{}, "", nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions?project=a&project=b", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"a", "b"}, gotFilter.Project)
}

func TestListSessions_LimitClampsAndDefaultTimeIsUnbounded(t *testing.T) {
	t.Parallel()

	var gotPage store.Page
	var gotFilter store.SessionFilter
	reader := &fakeReader{
		ListSessionsFunc: func(_ context.Context, f store.SessionFilter, p store.Page) ([]model.SessionSummary, store.Cursor, error) {
			gotFilter = f
			gotPage = p
			return []model.SessionSummary{}, "", nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions?limit=9999", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 500, gotPage.Limit, "limit=9999 must clamp to 500, not error")
	require.Nil(t, gotFilter.From)
	require.Nil(t, gotFilter.To)
}

func TestListSessions_FromRelativeShorthandParses(t *testing.T) {
	t.Parallel()

	var gotFilter store.SessionFilter
	reader := &fakeReader{
		ListSessionsFunc: func(_ context.Context, f store.SessionFilter, _ store.Page) ([]model.SessionSummary, store.Cursor, error) {
			gotFilter = f
			return []model.SessionSummary{}, "", nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions?from=-7d", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, gotFilter.From)
	require.WithinDuration(t, time.Now().Add(-7*24*time.Hour), *gotFilter.From, 5*time.Second)
}

func TestListSessions_FromGarbage_400NamesParameter(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions?from=garbage", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:invalid-parameter"`)
	require.Contains(t, rec.Body.String(), `from:`)
}

func TestGetSession_UnknownID_404Problem(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		GetSessionFunc: func(_ context.Context, _ string) (*model.SessionDetail, error) {
			return nil, store.ErrSessionNotFound
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/does-not-exist", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:not-found"`)
}

func TestGetSession_ETagAndIfNoneMatch(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		GetSessionFunc: func(_ context.Context, id string) (*model.SessionDetail, error) {
			return newTestSession(id), nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	// First request: no If-None-Match, expect 200 plus an ETag header.
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/s1", nil)
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	etag := rec.Header().Get("ETag")
	require.NotEmpty(t, etag)

	// Second request: matching If-None-Match, expect 304 with a genuinely
	// empty body.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/s1", nil)
	req2.Header.Set("If-None-Match", etag)
	r.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusNotModified, rec2.Code)
	require.Zero(t, rec2.Body.Len(), "304 must have an empty body")

	// A non-matching If-None-Match still gets the full 200 response.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/s1", nil)
	req3.Header.Set("If-None-Match", `"stale-tag"`)
	r.ServeHTTP(rec3, req3)
	require.Equal(t, http.StatusOK, rec3.Code)
	require.NotZero(t, rec3.Body.Len())
}

func TestGetSessionSubagents_CostAttributionPerNodeUnavailableAndCostNull(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		GetSessionFunc: func(_ context.Context, id string) (*model.SessionDetail, error) {
			return newTestSession(id), nil
		},
		SubagentTreeFunc: func(_ context.Context, _ string) (model.SubagentTree, error) {
			return model.SubagentTree{
				Nodes: []model.SubagentNode{
					{
						AgentID:   "root",
						AgentType: "main",
						Status:    model.SubagentStatusRunning,
						CostUSD:   nil,
						Children:  []model.SubagentNode{},
					},
				},
				CostAttribution: model.SubagentCostAttribution{
					PerNodeAvailable: false,
					Note:             "Claude Code does not emit per-agent cost; api_request carries query_source only.",
				},
			}, nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/s1/subagents", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"per_node_available":false`)
	require.Contains(t, rec.Body.String(), `"cost_usd":null`)
}

func TestGetSessionSubagents_UnknownSessionID_404(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		GetSessionFunc: func(_ context.Context, _ string) (*model.SessionDetail, error) {
			return nil, store.ErrSessionNotFound
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/does-not-exist/subagents", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:not-found"`)
}

func TestListSessionTurns_PaginatesInMemoryAndRoundTripsCursor(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2026, 8, 11, 9, 11, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	allTurns := []model.Turn{
		{SessionID: "s1", PromptID: "p2", FirstSeenAt: t1},
		{SessionID: "s1", PromptID: "p1", FirstSeenAt: t0},
	}
	reader := &fakeReader{
		GetSessionFunc: func(_ context.Context, id string) (*model.SessionDetail, error) {
			return newTestSession(id), nil
		},
		ListTurnsFunc: func(_ context.Context, sessionID string) ([]model.Turn, error) {
			require.Equal(t, "s1", sessionID)
			return allTurns, nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	// First page: limit=1 must return the earlier turn (p1, sorted by
	// first_seen_at) and signal has_more with a next_cursor.
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/s1/turns?limit=1", nil)
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"prompt_id":"p1"`)
	require.Contains(t, rec.Body.String(), `"has_more":true`)

	var page struct {
		Page struct {
			NextCursor *string `json:"next_cursor"`
		} `json:"page"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.NotNil(t, page.Page.NextCursor)

	// Second page, using that cursor, must return the later turn (p2) with
	// no more pages left.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/s1/turns?limit=1&cursor="+*page.Page.NextCursor, nil)
	r.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Contains(t, rec2.Body.String(), `"prompt_id":"p2"`)
	require.Contains(t, rec2.Body.String(), `"has_more":false`)
}

// TestListSessions_UnknownSort_400InvalidParameter is the m1 audit
// finding's regression test: an out-of-enum `sort` used to be cast
// unvalidated and rejected only by the store with a plain fmt.Errorf,
// surfacing as a 500 with internal error text. ListSessionsFunc must never
// even be called: sort validation happens before the store is asked
// anything.
func TestListSessions_UnknownSort_400InvalidParameter(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		ListSessionsFunc: func(_ context.Context, _ store.SessionFilter, _ store.Page) ([]model.SessionSummary, store.Cursor, error) {
			t.Fatal("ListSessions must not be called for an invalid sort")
			return nil, "", nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions?sort=bogus", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:invalid-parameter"`)
	require.Contains(t, rec.Body.String(), "sort must be one of")
}

// TestGetSessionTimeline_UnknownOrder_400InvalidParameter is m1's
// regression test for the timeline endpoint's `order` parameter (shared
// validation with listEventsHandler's own copy in events_test.go).
func TestGetSessionTimeline_UnknownOrder_400InvalidParameter(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		GetSessionFunc: func(_ context.Context, id string) (*model.SessionDetail, error) {
			return newTestSession(id), nil
		},
		ListEventsFunc: func(_ context.Context, _ store.EventFilter, _ store.Page) ([]model.Event, store.Cursor, error) {
			t.Fatal("ListEvents must not be called for an invalid order")
			return nil, "", nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/s1/timeline?order=DESC", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:invalid-parameter"`)
	require.Contains(t, rec.Body.String(), "order must be one of")
}

// TestListSessions_StoreInvalidCursor_400 is M14's handler-level regression
// test: a cursor that passes httpapi's own shallow shape check
// (DecodeCursor) but fails the store's stricter decode used to escape as a
// 500 echoing the store's internal error text. ListSessionsFunc simulates
// exactly that failure mode (postgres's decodeSessionCursor wraps
// store.ErrInvalidCursor the same way) without needing a real backend —
// TestListSessions_RealPostgres_MalformedCursor_400 in
// cursor_postgres_test.go covers the real decode path this fake cannot
// exercise (storetest.Fake never decodes a cursor at all).
func TestListSessions_StoreInvalidCursor_400(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		ListSessionsFunc: func(_ context.Context, _ store.SessionFilter, _ store.Page) ([]model.SessionSummary, store.Cursor, error) {
			return nil, "", fmt.Errorf("postgres: list sessions: %w: missing key or malformed values", store.ErrInvalidCursor)
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions?cursor=eyJrIjoibGFzdF9ldmVudF9hdCIsInYiOlsieCJdfQ", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:invalid-cursor"`)
}

func TestListSessionTurns_InvalidCursorValue_400(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		GetSessionFunc: func(_ context.Context, id string) (*model.SessionDetail, error) {
			return newTestSession(id), nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	// Structurally valid, sort-key-bound cursor whose first value is not a
	// parseable timestamp — exercises turnsAfterFromCursor's own decode
	// error, distinct from DecodeCursor's structural/tamper checks.
	badCursor, err := httpapi.EncodeCursor(query.TurnsSortKey, "not-a-time", "p1")
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/s1/turns?cursor="+badCursor, nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:invalid-cursor"`)
}
