package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/httpapi"
)

// TestStreamAwareTimeout_BypassesOnlyTheTwoSSERoutes is Trap 1's regression
// test (P5-02): chi's root Timeout middleware used to apply to every route,
// including the two SSE ones, so a live stream would be killed at exactly
// `requestTimeout` while every fast unit test stayed green and hid the bug.
// A 30s reproduction is not practical in a unit test, so this drives
// StreamAwareTimeout directly with a 1ms timeout and asserts the two
// directions the ticket calls out: the SSE routes reach the inner handler
// with an undeadlined context (bypassed), while an ordinary route still
// gets chi's own bounded context (and it actually fires).
func TestStreamAwareTimeout_BypassesOnlyTheTwoSSERoutes(t *testing.T) {
	t.Parallel()
	const timeout = time.Millisecond

	streamPaths := []string{"/api/v1/stream", "/api/v1/sessions/abc/stream"}
	for _, path := range streamPaths {
		t.Run("bypassed: "+path, func(t *testing.T) {
			t.Parallel()

			done := make(chan struct{})
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				_, hasDeadline := r.Context().Deadline()
				assert.False(t, hasDeadline, "an SSE route must reach the handler with an undeadlined context")
				w.WriteHeader(http.StatusOK)
			})

			h := httpapi.StreamAwareTimeout(timeout)(inner)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			<-done
			require.Equal(t, http.StatusOK, rec.Code, "the bypassed handler's own response must reach the client unmodified")
		})
	}

	t.Run("bounded: /api/v1/sessions keeps chi's timeout, and it fires", func(t *testing.T) {
		t.Parallel()

		inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			_, hasDeadline := r.Context().Deadline()
			assert.True(t, hasDeadline, "a non-SSE route must keep chi's Timeout deadline")
			// Wait for the 1ms timeout to actually fire (chi's own
			// contract, middleware/timeout.go: the handler must select on
			// ctx.Done() for the timeout to have any observable effect).
			<-r.Context().Done()
			assert.ErrorIs(t, r.Context().Err(), context.DeadlineExceeded)
		})

		h := httpapi.StreamAwareTimeout(timeout)(inner)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		require.Equal(t, http.StatusGatewayTimeout, rec.Code,
			"chi's Timeout middleware writes 504 once its deadline actually fires")
	})
}
