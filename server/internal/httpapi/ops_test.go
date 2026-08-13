package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/httpapi"
)

// fakeMigrations is the httpapi.MigrationsChecker fake used to exercise
// /readyz's migrations-pending/failed branches, wired in by P2-09 to close
// Phase-1 deviation D-5.
type fakeMigrations struct {
	current bool
	err     error
}

func (f fakeMigrations) MigrationsCurrent(_ context.Context) (bool, error) { return f.current, f.err }

// fakeQueue is the httpapi.QueueSaturationChecker fake used to exercise
// /readyz's third SPEC §3.8 condition.
type fakeQueue struct {
	saturated bool
}

func (f fakeQueue) QueueSaturated() bool { return f.saturated }

func TestReadyz_MigrationsPending(t *testing.T) {
	r := httpapi.New(httpapi.Deps{
		Store:      fakeStore{},
		Migrations: fakeMigrations{current: false},
		Assets:     testAssets(t),
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "migrations pending")
}

func TestReadyz_MigrationsCheckFails(t *testing.T) {
	r := httpapi.New(httpapi.Deps{
		Store:      fakeStore{},
		Migrations: fakeMigrations{err: errors.New("goose: connection refused")},
		Assets:     testAssets(t),
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "migrations check failed")
}

func TestReadyz_MigrationsCurrent_ReportsCurrentInBody(t *testing.T) {
	r := httpapi.New(httpapi.Deps{
		Store:      fakeStore{},
		Migrations: fakeMigrations{current: true},
		Assets:     testAssets(t),
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"status":"ok","migrations":"current"}`, rec.Body.String())
}

func TestReadyz_QueueSaturated(t *testing.T) {
	r := httpapi.New(httpapi.Deps{
		Store:  fakeStore{},
		Queue:  fakeQueue{saturated: true},
		Assets: testAssets(t),
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "queue saturated")
}

func TestReadyz_QueueNotSaturated_OK(t *testing.T) {
	r := httpapi.New(httpapi.Deps{
		Store:  fakeStore{},
		Queue:  fakeQueue{saturated: false},
		Assets: testAssets(t),
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusOK, rec.Code)
}

// TestReadyz_NilMigrationsAndQueue_PreservesPhase1Behaviour is the
// nil-safety AC: neither port set (Deps zero value for both) must behave
// exactly like Phase 1 — "migrations":"current" asserted, no queue check —
// so router_test.go's pre-existing TestReadyz_UpDB keeps passing unchanged.
func TestReadyz_NilMigrationsAndQueue_PreservesPhase1Behaviour(t *testing.T) {
	r := httpapi.New(httpapi.Deps{Store: fakeStore{}, Assets: testAssets(t)})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"status":"ok","migrations":"current"}`, rec.Body.String())
}
