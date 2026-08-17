// problem_test.go is the m2 audit finding's regression suite: 22 non-test
// call sites used to put a wrapped store/query error's own text straight
// into a 5xx problem+json `detail`, which could carry internal detail no
// client should see (the audit's worst example: /readyz, unauthenticated by
// default, echoing pgx's `user=%s database=%s` connection-failure string).
// These tests assert the general contract writeInternalError/logStoreError
// now enforce across every handler in this package: the response never
// contains the underlying error's text, the response does carry a
// request_id an operator can use to find the real error, and the real error
// is actually logged under that same request id.
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/httpapi"
	"github.com/YohannHommet/argus/server/internal/model"
)

// TestInternalError_NeverLeaksErrorText_ButLogsItWithRequestID drives GET
// /api/v1/facets to a store failure carrying text no client should ever see
// (a fabricated pgx-style connection failure, matching the ops.go:67 audit
// example almost verbatim) and asserts three things: the 500 body contains
// none of that text, the 500 body carries a non-empty request_id, and the
// access/error log written through Deps.Logger contains both the real error
// text and that same request id — so an operator can join the two.
func TestInternalError_NeverLeaksErrorText_ButLogsItWithRequestID(t *testing.T) {
	t.Parallel()

	const sensitive = "failed to connect to `user=argus_admin database=argus_prod`: connection refused"

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	reader := &fakeReader{
		FacetsFunc: func(context.Context) (model.Facets, error) {
			return model.Facets{}, errPlain(sensitive)
		},
	}
	r := httpapi.New(httpapi.Deps{Analytics: reader, Logger: logger})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/facets", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.NotContains(t, rec.Body.String(), sensitive,
		"the client-visible problem+json body must never contain the underlying error's text")
	require.NotContains(t, rec.Body.String(), "argus_admin")
	require.NotContains(t, rec.Body.String(), "argus_prod")

	var problem httpapi.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	require.NotEmpty(t, problem.RequestID, "the response must still carry a request id an operator can join to the log line")

	logged := logBuf.String()
	require.Contains(t, logged, sensitive, "the real error must still be logged in full")
	require.Contains(t, logged, problem.RequestID, "the log line must carry the same request id the response body does")
}

// errPlain is a minimal error type distinct from fmt.Errorf's *errors.errorString
// only so this file needs no extra import; its Error() is exactly the string
// passed in, with no wrapping noise to account for in the Contains checks
// above.
type errPlain string

func (e errPlain) Error() string { return string(e) }
