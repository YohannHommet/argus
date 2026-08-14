package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/httpapi"
	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

func TestListToolCalls_RepeatedToolParamsOR(t *testing.T) {
	t.Parallel()

	var gotFilter store.ToolCallFilter
	reader := &fakeReader{
		ListToolCallsFunc: func(_ context.Context, f store.ToolCallFilter, _ store.Page) ([]model.ToolCall, store.Cursor, error) {
			gotFilter = f
			return []model.ToolCall{}, "", nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/tool-calls?tool=Edit&tool=Read", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"Edit", "Read"}, gotFilter.Tool)
}

func TestListToolCalls_LimitClamps(t *testing.T) {
	t.Parallel()

	var gotPage store.Page
	reader := &fakeReader{
		ListToolCallsFunc: func(_ context.Context, _ store.ToolCallFilter, p store.Page) ([]model.ToolCall, store.Cursor, error) {
			gotPage = p
			return []model.ToolCall{}, "", nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/tool-calls?limit=9999", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 500, gotPage.Limit)
}

func TestListToolCalls_NegativeLimit_400NamesParameter(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/tool-calls?limit=-5", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:invalid-parameter"`)
	require.Contains(t, rec.Body.String(), `limit:`)
}

func TestListSessionToolCalls_UnknownSessionID_404(t *testing.T) {
	t.Parallel()

	reader := &fakeReader{
		GetSessionFunc: func(_ context.Context, _ string) (*model.SessionDetail, error) {
			return nil, store.ErrSessionNotFound
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/does-not-exist/tool-calls", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), `"type":"urn:argus:error:not-found"`)
}

func TestListSessionToolCalls_HappyPath(t *testing.T) {
	t.Parallel()

	toolUseID := "toolu_01A"
	reader := &fakeReader{
		GetSessionFunc: func(_ context.Context, id string) (*model.SessionDetail, error) {
			return newTestSession(id), nil
		},
		ListToolCallsFunc: func(_ context.Context, f store.ToolCallFilter, p store.Page) ([]model.ToolCall, store.Cursor, error) {
			require.Equal(t, "s1", f.SessionID)
			require.Equal(t, 50, p.Limit)
			return []model.ToolCall{
				{
					ID:          "tc1",
					SessionID:   "s1",
					ToolUseID:   &toolUseID,
					ToolName:    "Edit",
					StartedAt:   time.Date(2026, 8, 11, 9, 12, 4, 0, time.UTC),
					Correlation: model.CorrelationExact,
					EventCount:  2,
				},
			}, "", nil
		},
	}
	r := httpapi.New(httpapi.Deps{Reader: reader})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/s1/tool-calls", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"tool_use_id":"toolu_01A"`)
	require.Contains(t, rec.Body.String(), `"correlation":"exact"`)
	require.Contains(t, rec.Body.String(), `"event_count":2`)
}
