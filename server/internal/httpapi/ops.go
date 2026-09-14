package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
)

// ReadyState is the atomic readiness flag (SPEC §3.8 graceful-shutdown).
// Zero value is not-ready; nil *ReadyState is treated as always-ready.
type ReadyState struct {
	ready atomic.Bool
}

// NewReadyState returns a ReadyState that starts ready.
func NewReadyState() *ReadyState {
	s := &ReadyState{}
	s.ready.Store(true)
	return s
}

// SetReady flips the readiness flag (called during graceful shutdown).
func (s *ReadyState) SetReady(v bool) {
	s.ready.Store(v)
}

// Ready reports the current readiness flag.
func (s *ReadyState) Ready() bool {
	if s == nil {
		return true
	}
	return s.ready.Load()
}

// healthzHandler is liveness only (SPEC §3.8): no DB, no readiness check, so
// it stays cheap and correct even while the store is unavailable.
func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyzHandler reports full readiness (SPEC §3.8): draining, DB ping, migrations, queue.
// mc and qc are nil-safe: nil values report ready/pass unconditionally.
func readyzHandler(hc HealthChecker, mc MigrationsChecker, qc QueueSaturationChecker, rs *ReadyState, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !rs.Ready() {
			writeProblem(w, r, http.StatusServiceUnavailable, "not-ready", "server is shutting down")
			return
		}
		if hc != nil {
			if err := hc.Health(r.Context()); err != nil {
				// m2: /readyz is unauthenticated; never expose DB error text, log instead.
				logStoreError(r, logger, err)
				writeProblem(w, r, http.StatusServiceUnavailable, "not-ready", "database health check failed")
				return
			}
		}
		if mc != nil {
			current, err := mc.MigrationsCurrent(r.Context())
			if err != nil {
				logStoreError(r, logger, err)
				writeProblem(w, r, http.StatusServiceUnavailable, "not-ready", "migrations check failed")
				return
			}
			if !current {
				writeProblem(w, r, http.StatusServiceUnavailable, "not-ready", "migrations pending")
				return
			}
		}
		if qc != nil && qc.QueueSaturated() {
			writeProblem(w, r, http.StatusServiceUnavailable, "not-ready", "ingest queue saturated")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "migrations": "current"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
