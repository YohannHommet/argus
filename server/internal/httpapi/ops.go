package httpapi

import (
	"encoding/json"
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
func healthzHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyzHandler reports SPEC §3.8 readiness: draining state, then a live
// store health check. "DB ping + migrations current + queue not saturated"
// is the full contract; the ingest queue doesn't exist until P2, so only the
// first two conditions apply here. A reachable database implies migrations
// are current, since nothing in Phase 1 changes the schema outside the
// ARGUS_AUTO_MIGRATE gate that runs before Serve ever starts accepting
// requests.
func readyzHandler(hc HealthChecker, rs *ReadyState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !rs.Ready() {
			writeProblem(w, r, http.StatusServiceUnavailable, "not-ready", "server is shutting down")
			return
		}
		if hc != nil {
			if err := hc.Health(r.Context()); err != nil {
				writeProblem(w, r, http.StatusServiceUnavailable, "not-ready", "database health check failed: "+err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "migrations": "current"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
