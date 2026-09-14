package hooks_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/ingest/hooks"
	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
	"github.com/YohannHommet/argus/server/internal/model"
)

// postHook posts body to the given target (path + optional query) and returns
// the recorder, so these cases can vary the URL rather than only the body.
func postHook(t *testing.T, h *hooks.Handler, target string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newEventParamHandler(t *testing.T) (*hooks.Handler, *captureEnqueuer) {
	t.Helper()
	enq := &captureEnqueuer{}
	norm := normalize.NewHookNormalizer(time.Now, 90*24*time.Hour, false)
	return hooks.NewHandler(enq, norm, 1<<20, hooks.WithRegisterer(prometheus.NewRegistry())), enq
}

func TestHandler_EventQueryParamClassifiesNamelessPayload(t *testing.T) {
	h, enq := newEventParamHandler(t)

	rec := postHook(t, h, "/ingest/hook?event=SessionStart",
		`{"session_id":"sess-1","cwd":"/home/u/proj","source":"startup"}`)
	require.Equal(t, http.StatusAccepted, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "SessionStart", resp["event"], "the 202 echo must report the name actually applied")

	events := enq.allEvents()
	require.Len(t, events, 1)
	require.Equal(t, model.KindSessionStart, events[0].Kind)
}

func TestHandler_PayloadHookEventNameWinsOverQueryParam(t *testing.T) {
	h, enq := newEventParamHandler(t)

	rec := postHook(t, h, "/ingest/hook?event=SessionStart", sessionEndPayload)
	require.Equal(t, http.StatusAccepted, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "SessionEnd", resp["event"])

	events := enq.allEvents()
	require.Len(t, events, 1)
	require.Equal(t, model.KindSessionEnd, events[0].Kind)
}

// Backward compatibility: the pre-?event wiring (and argus-sim) posts a
// self-naming body to the bare path and must behave exactly as before.
func TestHandler_NoEventParamKeepsPayloadOnlyBehaviour(t *testing.T) {
	h, enq := newEventParamHandler(t)

	rec := postHook(t, h, "/ingest/hook", sessionEndPayload)
	require.Equal(t, http.StatusAccepted, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "SessionEnd", resp["event"])
	require.Equal(t, model.KindSessionEnd, enq.allEvents()[0].Kind)
}

func TestHandler_NoEventParamAndNamelessPayloadStaysUnknown(t *testing.T) {
	h, enq := newEventParamHandler(t)

	rec := postHook(t, h, "/ingest/hook", `{"session_id":"sess-1","tool_response":{"stdout":"hi"}}`)
	require.Equal(t, http.StatusAccepted, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Empty(t, resp["event"])
	require.Equal(t, model.KindUnknown, enq.allEvents()[0].Kind)
}

// An array body replayed through one ?event= URL: every nameless element
// takes the param, and the echo lists what each element ended up as.
func TestHandler_EventQueryParamAppliesAcrossArrayBody(t *testing.T) {
	h, enq := newEventParamHandler(t)

	rec := postHook(t, h, "/ingest/hook?event=SessionStart",
		`[{"session_id":"s1"},{"session_id":"s2","hook_event_name":"SessionEnd"}]`)
	require.Equal(t, http.StatusAccepted, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "SessionStart,SessionEnd", resp["event"])

	events := enq.allEvents()
	require.Len(t, events, 2)
	require.Equal(t, model.KindSessionStart, events[0].Kind)
	require.Equal(t, model.KindSessionEnd, events[1].Kind)
}
