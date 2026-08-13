package hooks

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Mounter attaches Handler to a chi.Router under `POST /ingest/hook`
// (SPEC §3.5). It structurally satisfies internal/httpapi.Mounter
// (`Mount(r chi.Router)`) without importing internal/httpapi — depguard
// forbids internal/ingest depending on internal/httpapi (SPEC §3.1), and
// Go interfaces are satisfied structurally, so no import is needed for
// internal/app to hand this to httpapi.Deps.HookMounter.
type Mounter struct {
	handler http.Handler
}

// NewMounter wraps h with auth (SPEC §3.5: "if ARGUS_INGEST_TOKEN is set,
// both OTLP and hook endpoints require Authorization: Bearer <token>").
// auth is httpapi.RequireIngestToken's return value, passed in as a plain
// func(http.Handler) http.Handler rather than imported: the concrete
// middleware.go file lives in internal/httpapi and depguard forbids this
// package importing it, so internal/app is the one place that can name
// both types and closes the seam. A nil auth mounts h unwrapped — matching
// RequireIngestToken's own no-op-when-token-empty behaviour, so a nil is
// indistinguishable in effect from "no token configured" and NewMounter
// never needs its own empty-token special case.
func NewMounter(h *Handler, auth func(http.Handler) http.Handler) *Mounter {
	var handler http.Handler = h
	if auth != nil {
		handler = auth(handler)
	}
	return &Mounter{handler: handler}
}

// Mount registers `POST /ingest/hook` on r. Only POST is registered (SPEC
// §3.5 names no other verb for this endpoint); chi answers any other
// method on this exact path with its default 405, and any other path with
// its default 404 — router.go's problem+json NotFound/MethodNotAllowed
// handlers are scoped to the `/api` subrouter only (router.go), so this
// top-level route, like the OTLP mount seam beside it, does not inherit
// them.
func (m *Mounter) Mount(r chi.Router) {
	r.Method(http.MethodPost, "/ingest/hook", m.handler)
}
