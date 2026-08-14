package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/httpapi"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/query"
	"github.com/YohannHommet/argus/server/internal/store"
	"github.com/YohannHommet/argus/server/internal/store/postgres"
)

// fakeReader is the P3-07 test double for httpapi.Reader: the ticket's
// "fake store" AC scenario refers to store/testing.Fake, which is P3-09's
// deliverable and runs after this ticket, so this is a minimal, local
// stand-in implementing only the methods httpapi.Reader declares. P3-09
// should consolidate this (and its events_test.go/toolcalls_test.go
// siblings) into the shared Fake rather than keep three separate ones.
//
// Every field is a settable func so each test wires up only the behaviour
// it needs; an unset func fails loudly (never a silent zero value) so a
// test that forgot to stub a call path is caught immediately rather than
// asserting against an empty response.
type fakeReader struct {
	listSessions  func(ctx context.Context, f store.SessionFilter, p store.Page) ([]model.SessionSummary, store.Cursor, error)
	getSession    func(ctx context.Context, id string) (*model.SessionDetail, error)
	listTurns     func(ctx context.Context, sessionID string) ([]model.Turn, error)
	listEvents    func(ctx context.Context, f store.EventFilter, p store.Page) ([]model.Event, store.Cursor, error)
	getEvent      func(ctx context.Context, ref model.EventRef) (*model.Event, error)
	listToolCalls func(ctx context.Context, f store.ToolCallFilter, p store.Page) ([]model.ToolCall, store.Cursor, error)
	subagentTree  func(ctx context.Context, sessionID string) (model.SubagentTree, error)

	// The eight fields below back httpapi.AnalyticsReader (P3-08:
	// analytics.go/facets.go/quality.go/meta.go's port), extending — not
	// duplicating — this ticket's fakeReader per the P3-08 ticket note
	// ("reuse and extend that rather than creating a second one").
	analyticsSummary   func(ctx context.Context, f store.AnalyticsFilter) (model.Summary, error)
	analyticsSeries    func(ctx context.Context, f store.AnalyticsFilter, g store.Grouping) (model.Series, error)
	analyticsBreakdown func(ctx context.Context, f store.AnalyticsFilter, d store.Dimension) (model.Breakdown, error)
	analyticsDecisions func(ctx context.Context, f store.AnalyticsFilter) (model.DecisionMatrix, error)
	facets             func(ctx context.Context) (model.Facets, error)
	dataQuality        func(ctx context.Context) (model.DataQuality, error)
	unknownKinds       func(ctx context.Context, since time.Time, limit int) ([]model.UnknownKindGroup, error)
	hookLatency        func(ctx context.Context, f store.AnalyticsFilter) (model.HookLatency, error)
}

func (f *fakeReader) ListSessions(ctx context.Context, filter store.SessionFilter, p store.Page) ([]model.SessionSummary, store.Cursor, error) {
	if f.listSessions == nil {
		panic("fakeReader.ListSessions not stubbed")
	}
	return f.listSessions(ctx, filter, p)
}

func (f *fakeReader) GetSession(ctx context.Context, id string) (*model.SessionDetail, error) {
	if f.getSession == nil {
		panic("fakeReader.GetSession not stubbed")
	}
	return f.getSession(ctx, id)
}

func (f *fakeReader) ListTurns(ctx context.Context, sessionID string) ([]model.Turn, error) {
	if f.listTurns == nil {
		panic("fakeReader.ListTurns not stubbed")
	}
	return f.listTurns(ctx, sessionID)
}

func (f *fakeReader) ListEvents(ctx context.Context, filter store.EventFilter, p store.Page) ([]model.Event, store.Cursor, error) {
	if f.listEvents == nil {
		panic("fakeReader.ListEvents not stubbed")
	}
	return f.listEvents(ctx, filter, p)
}

func (f *fakeReader) GetEvent(ctx context.Context, ref model.EventRef) (*model.Event, error) {
	if f.getEvent == nil {
		panic("fakeReader.GetEvent not stubbed")
	}
	return f.getEvent(ctx, ref)
}

func (f *fakeReader) ListToolCalls(ctx context.Context, filter store.ToolCallFilter, p store.Page) ([]model.ToolCall, store.Cursor, error) {
	if f.listToolCalls == nil {
		panic("fakeReader.ListToolCalls not stubbed")
	}
	return f.listToolCalls(ctx, filter, p)
}

func (f *fakeReader) SubagentTree(ctx context.Context, sessionID string) (model.SubagentTree, error) {
	if f.subagentTree == nil {
		panic("fakeReader.SubagentTree not stubbed")
	}
	return f.subagentTree(ctx, sessionID)
}

func (f *fakeReader) AnalyticsSummary(ctx context.Context, filter store.AnalyticsFilter) (model.Summary, error) {
	if f.analyticsSummary == nil {
		panic("fakeReader.AnalyticsSummary not stubbed")
	}
	return f.analyticsSummary(ctx, filter)
}

func (f *fakeReader) AnalyticsSeries(ctx context.Context, filter store.AnalyticsFilter, g store.Grouping) (model.Series, error) {
	if f.analyticsSeries == nil {
		panic("fakeReader.AnalyticsSeries not stubbed")
	}
	return f.analyticsSeries(ctx, filter, g)
}

func (f *fakeReader) AnalyticsBreakdown(ctx context.Context, filter store.AnalyticsFilter, d store.Dimension) (model.Breakdown, error) {
	if f.analyticsBreakdown == nil {
		panic("fakeReader.AnalyticsBreakdown not stubbed")
	}
	return f.analyticsBreakdown(ctx, filter, d)
}

func (f *fakeReader) AnalyticsDecisions(ctx context.Context, filter store.AnalyticsFilter) (model.DecisionMatrix, error) {
	if f.analyticsDecisions == nil {
		panic("fakeReader.AnalyticsDecisions not stubbed")
	}
	return f.analyticsDecisions(ctx, filter)
}

func (f *fakeReader) Facets(ctx context.Context) (model.Facets, error) {
	if f.facets == nil {
		panic("fakeReader.Facets not stubbed")
	}
	return f.facets(ctx)
}

func (f *fakeReader) DataQuality(ctx context.Context) (model.DataQuality, error) {
	if f.dataQuality == nil {
		panic("fakeReader.DataQuality not stubbed")
	}
	return f.dataQuality(ctx)
}

func (f *fakeReader) UnknownKinds(ctx context.Context, since time.Time, limit int) ([]model.UnknownKindGroup, error) {
	if f.unknownKinds == nil {
		panic("fakeReader.UnknownKinds not stubbed")
	}
	return f.unknownKinds(ctx, since, limit)
}

func (f *fakeReader) HookLatency(ctx context.Context, filter store.AnalyticsFilter) (model.HookLatency, error) {
	if f.hookLatency == nil {
		panic("fakeReader.HookLatency not stubbed")
	}
	return f.hookLatency(ctx, filter)
}

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
		listSessions: func(_ context.Context, f store.SessionFilter, _ store.Page) ([]model.SessionSummary, store.Cursor, error) {
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
		listSessions: func(_ context.Context, f store.SessionFilter, p store.Page) ([]model.SessionSummary, store.Cursor, error) {
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
		listSessions: func(_ context.Context, f store.SessionFilter, _ store.Page) ([]model.SessionSummary, store.Cursor, error) {
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
		getSession: func(_ context.Context, _ string) (*model.SessionDetail, error) {
			return nil, postgres.ErrSessionNotFound
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
		getSession: func(_ context.Context, id string) (*model.SessionDetail, error) {
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
		getSession: func(_ context.Context, id string) (*model.SessionDetail, error) {
			return newTestSession(id), nil
		},
		subagentTree: func(_ context.Context, _ string) (model.SubagentTree, error) {
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
		getSession: func(_ context.Context, _ string) (*model.SessionDetail, error) {
			return nil, postgres.ErrSessionNotFound
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
		getSession: func(_ context.Context, id string) (*model.SessionDetail, error) {
			return newTestSession(id), nil
		},
		listTurns: func(_ context.Context, sessionID string) ([]model.Turn, error) {
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

func TestListSessionTurns_InvalidCursorValue_400(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		getSession: func(_ context.Context, id string) (*model.SessionDetail, error) {
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
