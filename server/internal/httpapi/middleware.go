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

// AccessLog logs every request via logger, sampling successful (status <
// 400) requests at 1-in-sampleRate — the same "sampled 1/100 on success"
// rule SPEC §3.8 states for ingest logs, applied here to the general access
// log so a busy read API can't flood the log file either. Errors are always
// logged in full. sampleRate <= 1 disables sampling (logs everything).
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

// RequireAPIToken guards the read API (SPEC §3.5): a no-op when token is
// empty (ARGUS_API_TOKEN unset — DECISIONS.md: no auth in v1 by default),
// otherwise it requires a matching "Authorization: Bearer <token>" header,
// compared in constant time.
func RequireAPIToken(token string) func(http.Handler) http.Handler {
	return requireBearerToken(token)
}

// RequireIngestToken guards the ingest surface (OTLP + hooks, SPEC §3.5):
// same no-op-when-empty, constant-time-compare seam as RequireAPIToken, but
// gated by ARGUS_INGEST_TOKEN. Nothing mounts behind it yet in Phase 1 (P2's
// ingest.Mounter will), but the seam must exist now so router.go never needs
// touching again.
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

// CORS is the optional cross-origin middleware SPEC §3.7 says is "needed
// only for pnpm dev on :5173": when origins is empty it is a no-op, matching
// the default (`ARGUS_CORS_ORIGINS` unset) in which the SPA is always
// same-origin with the API. origins is a comma-separated allow-list.
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
