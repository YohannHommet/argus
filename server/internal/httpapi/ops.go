package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
)

// ReadyState is the atomic readiness flag internal/app flips during the
// SPEC §3.8 graceful-shutdown sequence: step (1) is "/readyz starts
// failing", which must take effect before the HTTP server even stops
// accepting new connections so a load balancer polling /readyz sees the
// node draining immediately.
//
// The zero value reports not-ready; construct with NewReadyState to start
// ready. A nil *ReadyState (e.g. a test that doesn't care about draining)
// is treated as always-ready.
type ReadyState struct {
	ready atomic.Bool
}

// NewReadyState returns a ReadyState that starts ready.
func NewReadyState() *ReadyState {
	s := &ReadyState{}
	s.ready.Store(true)
	return s
}

// SetReady flips the readiness flag. internal/app calls SetReady(false) as
// the first step of Serve's shutdown sequence.
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

// readyzHandler reports SPEC §3.8's full readiness contract: draining
// state, DB ping, migrations current, and queue not saturated (the last two
// gained real checks in P2-09; Phase 1 asserted "migrations":"current"
// without checking it, recorded as deviation D-5).
//
// mc and qc are both nil-safe (their interface docs explain why): a nil
// MigrationsChecker reports "current" unconditionally, matching Phase 1's
// existing test contract, and a nil QueueSaturationChecker never fails
// readiness on that ground — both are the P1-05 default until internal/app
// wires the real store and pipeline in.
func readyzHandler(hc HealthChecker, mc MigrationsChecker, qc QueueSaturationChecker, rs *ReadyState, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !rs.Ready() {
			writeProblem(w, r, http.StatusServiceUnavailable, "not-ready", "server is shutting down")
			return
		}
		if hc != nil {
			if err := hc.Health(r.Context()); err != nil {
				// m2 audit finding: pool.Ping's own error text can be pgx's
				// `failed to connect to `user=%s database=%s`:` — and
				// /readyz sits outside RequireAPIToken (SPEC §3.5's read
				// API is unauthenticated by default), so that text must
				// never reach the client. Logged instead (logStoreError),
				// tagged with the request id this response also carries.
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
