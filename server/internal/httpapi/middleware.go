package httpapi

import (
	"crypto/subtle"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// AccessLog logs requests, sampling successful (status < 400) by sampleRate, always logging errors.
func AccessLog(logger *slog.Logger, sampleRate int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			if status >= http.StatusBadRequest || sampleRate <= 1 || rand.IntN(sampleRate) == 0 { //nolint:gosec // sampling access-log volume, not security-sensitive
				logger.LogAttrs(r.Context(), slog.LevelInfo, "http_request",
					slog.String("request_id", chimw.GetReqID(r.Context())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int("status", status),
					slog.Int("bytes", ww.BytesWritten()),
					slog.Duration("duration", time.Since(start)),
					slog.String("remote_addr", r.RemoteAddr),
				)
			}
		})
	}
}

// RequireAPIToken guards the read API (SPEC §3.5); no-op if token is empty.
func RequireAPIToken(token string) func(http.Handler) http.Handler {
	return requireBearerToken(token)
}

// RequireIngestToken guards the ingest surface (SPEC §3.5); no-op if token is empty.
func RequireIngestToken(token string) func(http.Handler) http.Handler {
	return requireBearerToken(token)
}

func requireBearerToken(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if token == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, ok := bearerToken(r)
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	return strings.TrimPrefix(h, prefix), true
}

// CORS is optional cross-origin middleware (SPEC §3.7); no-op if origins is empty.
func CORS(origins string) func(http.Handler) http.Handler {
	allowed := parseOrigins(origins)
	return func(next http.Handler) http.Handler {
		if len(allowed) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// StreamAwareTimeout wraps chi's Timeout, exempting SSE routes to avoid killing live streams.
// SSE handlers select r.Context().Done() for teardown; chi's Timeout would fire at exactly
// `timeout` and abort the stream. Chi's fixed middleware stack forbids routing-level exemption,
// so we check isStreamPath (sse.go) per-request to bypass Timeout for stream routes only.
func StreamAwareTimeout(timeout time.Duration) func(http.Handler) http.Handler {
	bound := chimw.Timeout(timeout)
	return func(next http.Handler) http.Handler {
		timed := bound(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isStreamPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			timed.ServeHTTP(w, r)
		})
	}
}

func parseOrigins(raw string) map[string]bool {
	if raw == "" {
		return nil
	}
	allowed := make(map[string]bool)
	for _, o := range strings.Split(raw, ",") {
		o = strings.TrimSpace(o)
		if o != "" {
			allowed[o] = true
		}
	}
	return allowed
}
