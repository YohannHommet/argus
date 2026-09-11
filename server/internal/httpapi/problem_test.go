// problem_test.go verifies m2 audit fix: internal errors never leak to clients but are logged with request IDs.
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

// TestInternalError_NeverLeaksErrorText_ButLogsItWithRequestID verifies error logging without client leaks.
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

// errPlain is a minimal error type with no wrapping noise for Contains checks.
type errPlain string

func (e errPlain) Error() string { return string(e) }
