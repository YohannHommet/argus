package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/httpapi"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// TestListEvents_UnknownOrder_400InvalidParameter is the m1 audit finding's
// regression test for GET /api/v1/events' own `order` parameter (events.go
// defines validSortOrders, shared with sessions.go's timeline handler).
func TestListEvents_UnknownOrder_400InvalidParameter(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		ListEventsFunc: func(_ context.Context, _ store.EventFilter, _ store.Page) ([]model.Event, store.Cursor, error) {
			t.Fatal("ListEvents must not be called for an invalid order")
			return nil, "", nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?order=DESC", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:invalid-parameter"`)
	require.Contains(t, rec.Body.String(), "order must be one of")
}

// TestListEvents_StoreInvalidCursor_400 is M14's handler-level regression
// test for GET /api/v1/events (the cross-session counterpart of
// TestListSessions_StoreInvalidCursor_400 in sessions_test.go): a cursor
// valid at httpapi's shallow check but rejected by the store's stricter
// decode must map onto 400, not 500.
func TestListEvents_StoreInvalidCursor_400(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		ListEventsFunc: func(_ context.Context, _ store.EventFilter, _ store.Page) ([]model.Event, store.Cursor, error) {
			return nil, "", fmt.Errorf("postgres: list events: %w: missing key or malformed values", store.ErrInvalidCursor)
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?cursor=eyJrIjoiYXNjIiwidiI6WyJ4Il19", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:invalid-cursor"`)
}

func TestGetEvent_MalformedRef_400InvalidEventRef(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events/not-a-ref", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:invalid-event-ref"`)
}

func TestGetEvent_WellFormedButUnknownRef_404(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		GetEventFunc: func(_ context.Context, _ model.EventRef) (*model.Event, error) {
			return nil, store.ErrEventNotFound
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	ref := model.EventRef{TS: time.Date(2026, 8, 11, 9, 12, 4, 221_000_000, time.UTC), Seq: 918233}

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events/"+ref.Encode(), nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:not-found"`)
}

func TestGetEvent_Found_ReturnsAttrs(t *testing.T) {
	t.Parallel()

	toolName := "Edit"
	ref := model.EventRef{TS: time.Date(2026, 8, 11, 9, 12, 4, 221_000_000, time.UTC), Seq: 918233}
	reader := &fakeReader{
		GetEventFunc: func(_ context.Context, gotRef model.EventRef) (*model.Event, error) {
			require.True(t, gotRef.TS.Equal(ref.TS))
			require.Equal(t, ref.Seq, gotRef.Seq)
			return &model.Event{
				Seq:       ref.Seq,
				ID:        "0192abcd-0000-0000-0000-000000000001",
				TS:        ref.TS,
				SessionID: "s1",
				Kind:      model.KindToolDecision,
				Source:    model.SourceOTelLog,
				Vendor:    "claude_code",
				ToolName:  &toolName,
				Attrs:     map[string]any{"tool_decision.tool_use_id": "toolu_01A"},
			}, nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events/"+ref.Encode(), nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"event_ref":"`+ref.Encode()+`"`)
	require.Contains(t, rec.Body.String(), `"tool_decision.tool_use_id":"toolu_01A"`)
	require.Contains(t, rec.Body.String(), `"tool_name":"Edit"`)
}

func TestListEvents_RepeatedProjectParamsOR(t *testing.T) {
	t.Parallel()

	var gotFilter store.EventFilter
	reader := &fakeReader{
		ListEventsFunc: func(_ context.Context, f store.EventFilter, _ store.Page) ([]model.Event, store.Cursor, error) {
			gotFilter = f
			return []model.Event{}, "", nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?project=a&project=b", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"a", "b"}, gotFilter.Project)
}

func TestListEvents_LimitClamps(t *testing.T) {
	t.Parallel()

	var gotPage store.Page
	reader := &fakeReader{
		ListEventsFunc: func(_ context.Context, _ store.EventFilter, p store.Page) ([]model.Event, store.Cursor, error) {
			gotPage = p
			return []model.Event{}, "", nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?limit=9999", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 500, gotPage.Limit)
}

func TestGetSessionTimeline_UnknownSessionID_404(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		GetSessionFunc: func(_ context.Context, _ string) (*model.SessionDetail, error) {
			return nil, store.ErrSessionNotFound
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/does-not-exist/timeline", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:not-found"`)
}

func TestGetSessionTimeline_HappyPath(t *testing.T) {
	t.Parallel()

	inputTokens := int64(41233)
	ts := time.Date(2026, 8, 11, 9, 12, 4, 221_000_000, time.UTC)
	reader := &fakeReader{
		GetSessionFunc: func(_ context.Context, id string) (*model.SessionDetail, error) {
			return newTestSession(id), nil
		},
		ListEventsFunc: func(_ context.Context, f store.EventFilter, p store.Page) ([]model.Event, store.Cursor, error) {
			require.Equal(t, "s1", f.SessionID)
			require.Equal(t, 50, p.Limit)
			return []model.Event{
				{Seq: 1, ID: "e1", TS: ts, SessionID: "s1", Kind: model.KindLLMRequest, Source: model.SourceOTelLog, Vendor: "claude_code", InputTokens: &inputTokens},
			}, "", nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/s1/timeline?order=desc&fields=full", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"tokens":{"input":41233,"output":0,"cache_read":0,"cache_creation":0}`)
	require.Contains(t, rec.Body.String(), `"has_more":false`)
}
