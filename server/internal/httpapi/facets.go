package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/query"
)

// facetsCacheTTL is GET /api/v1/facets' in-process cache window (P3-08
// ticket note: "60 s in-process"). openapi.yaml/SPEC name no dedicated
// config key for this — Facets' underlying queries are cheap DISTINCT scans
// over sessions/tool_calls (read_quality.go), not rollups, so a fixed
// constant rather than a configurable one is the deliberately minimal
// choice here.
const facetsCacheTTL = 60 * time.Second

// facetsCache is GET /api/v1/facets' in-process cache: a single shared
// value plus its expiry, guarded by a mutex so concurrent requests (the
// ticket's -race AC) never race on the read-then-maybe-refresh sequence. A
// failed refresh never overwrites the cached value or its expiry — an
// error must not be cached as a success (ticket note) — so a transient
// store error only ever costs one extra store call on the next request,
// never a permanently stuck stale/zero value.
type facetsCache struct {
	mu        sync.Mutex
	value     model.Facets
	expiresAt time.Time
}

// get returns the cached value if still fresh, else calls through
// query.Facets and refreshes the cache — but only on success.
func (c *facetsCache) get(ctx context.Context, reader query.QualityReader) (model.Facets, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if time.Now().Before(c.expiresAt) {
		return c.value, nil
	}

	facets, err := query.Facets(ctx, reader)
	if err != nil {
		return model.Facets{}, err
	}
	c.value = facets
	c.expiresAt = time.Now().Add(facetsCacheTTL)
	return c.value, nil
}

// mountFacetRoutes attaches GET /api/v1/facets (SPEC §4.2), wiring one
// facetsCache instance shared by every request through this mount.
func mountFacetRoutes(r chi.Router, reader AnalyticsReader, logger *slog.Logger) {
	cache := &facetsCache{}
	r.Get("/facets", getFacetsHandler(reader, cache, logger))
}

func getFacetsHandler(reader query.QualityReader, cache *facetsCache, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		facets, err := cache.get(r.Context(), reader)
		if err != nil {
			writeInternalError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, facets)
	}
}
