package hooks

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Mounter attaches Handler to POST /ingest/hook (satisfies httpapi.Mounter structurally).
type Mounter struct {
	handler http.Handler
}

// NewMounter wraps h with auth middleware (SPEC §3.5). Nil auth → no-op (token empty).
func NewMounter(h *Handler, auth func(http.Handler) http.Handler) *Mounter {
	var handler http.Handler = h
	if auth != nil {
		handler = auth(handler)
	}
	return &Mounter{handler: handler}
}

// Mount registers POST /ingest/hook (SPEC §3.5 names POST only).
func (m *Mounter) Mount(r chi.Router) {
	r.Method(http.MethodPost, "/ingest/hook", m.handler)
}
