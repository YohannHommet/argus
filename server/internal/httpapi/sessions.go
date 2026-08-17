package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/query"
	"github.com/YohannHommet/argus/server/internal/store"
)

// validSessionSorts is SPEC §4.3's closed `sort` vocabulary for GET
// /api/v1/sessions ("sort ∈ last_event_at|started_at|cost_usd|
// event_count"). Unlike `status`/`tool`/`decision_source` (SPEC's own
// OR-set filter fields, permissive by design — see castSessionStatuses),
// `sort` names a specific store column: an unrecognized value has no safe
// permissive interpretation, so it is a 400 here rather than falling
// through to the store's own plain-fmt.Errorf rejection (m1 audit finding
// — see contains/joinStrings in analytics.go for the shared closed-
// vocabulary validation this mirrors, analytics.go:139).
var validSessionSorts = []store.SessionSort{
	store.SessionSortLastEventAt, store.SessionSortStartedAt, store.SessionSortCostUSD, store.SessionSortEventCount,
}

// sessionsListResponse is GET /api/v1/sessions' body (SPEC §4.3,
// openapi.yaml's SessionsListResponse): model.SessionSummary already
// carries the exact wire shape (correct JSON tags, correct nesting), so it
// is marshaled directly — no parallel wire struct needed here.
type sessionsListResponse struct {
	Data []model.SessionSummary `json:"data"`
	Page pageInfo               `json:"page"`
}

// turnsListResponse is GET /api/v1/sessions/{id}/turns' body
// (openapi.yaml's TurnsListResponse): model.Turn already carries the exact
// wire shape.
type turnsListResponse struct {
	Data []model.Turn `json:"data"`
	Page pageInfo     `json:"page"`
}

// mountSessionRoutes attaches every `/sessions...` read route this ticket
// owns except tool-calls (toolcalls.go's mountToolCallRoutes owns
// `/sessions/{id}/tool-calls`, alongside the cross-session
// `/tool-calls` it shares a query-layer function with).
func mountSessionRoutes(r chi.Router, reader Reader, logger *slog.Logger) {
	r.Get("/sessions", listSessionsHandler(reader, logger))
	r.Get("/sessions/{id}", getSessionHandler(reader, logger))
	r.Get("/sessions/{id}/timeline", getSessionTimelineHandler(reader, logger))
	r.Get("/sessions/{id}/turns", listSessionTurnsHandler(reader, logger))
	r.Get("/sessions/{id}/subagents", getSessionSubagentsHandler(reader, logger))
}

// listSessionsHandler implements GET /api/v1/sessions (SPEC §4.3).
func listSessionsHandler(reader Reader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		// sort is validated against the closed SessionSort set (m1 audit
		// finding: an unrecognized value used to flow through unvalidated
		// to the store, which rejected it with a plain fmt.Errorf that
		// surfaced as a 500 with internal text — SPEC §4.3 lists exactly
		// four valid values, and openapi.yaml declares 400 as the only
		// client-error status here). The store-layer check stays in place
		// as defence in depth.
		sortKey := store.SessionSortLastEventAt
		if raw := q.Get("sort"); raw != "" {
			sortKey = store.SessionSort(raw)
			if !contains(validSessionSorts, sortKey) {
				writeProblem(w, r, http.StatusBadRequest, "invalid-parameter",
					"sort must be one of "+joinStrings(validSessionSorts))
				return
			}
		}

		page, err := bindLimitAndCursor(r, string(sortKey))
		if err != nil {
			writeBindError(w, r, err)
			return
		}
		from, to, err := parseTimeWindow(r)
		if err != nil {
			writeBindError(w, r, err)
			return
		}

		f := store.SessionFilter{
			Project:        repeatedParam(r, "project"),
			Vendor:         repeatedParam(r, "vendor"),
			Model:          repeatedParam(r, "model"),
			Status:         castSessionStatuses(repeatedParam(r, "status")),
			Tool:           repeatedParam(r, "tool"),
			DecisionSource: repeatedParam(r, "decision_source"),
			From:           from,
			To:             to,
			Q:              q.Get("q"),
			Sort:           sortKey,
		}

		res, err := query.ListSessions(r.Context(), reader, f, page)
		if err != nil {
			writeListStoreError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, sessionsListResponse{Data: res.Sessions, Page: pageInfoFrom(res.Page)})
	}
}

// writeListStoreError maps a query.ListSessions/query.ListEvents/
// query.ListToolCalls failure onto the right problem+json response
// (shared by every store-paginated list handler in this package and in
// events.go/toolcalls.go): store.ErrInvalidCursor means the cursor passed
// httpapi's own shallow shape check but failed the backend's stricter
// decode (M14 audit finding — SPEC §4.1 "opaque, validated, 400 on
// tamper"); anything else is an unexpected store failure, routed through
// writeInternalError so it never echoes err's own text to the client (m2
// audit finding).
func writeListStoreError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	if errors.Is(err, store.ErrInvalidCursor) {
		writeProblem(w, r, http.StatusBadRequest, "invalid-cursor", err.Error())
		return
	}
	writeInternalError(w, r, logger, err)
}

// getSessionHandler implements GET /api/v1/sessions/{id} (SPEC §4.3),
// including the ETag/If-None-Match pair SPEC §4.1 requires on session
// detail: a matching If-None-Match short-circuits to 304 with no body.
func getSessionHandler(reader Reader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")

		detail, err := query.GetSession(r.Context(), reader, id)
		if err != nil {
			writeSessionLookupError(w, r, logger, err)
			return
		}

		etag := query.SessionETag(detail)
		w.Header().Set("ETag", etag)
		if inm := r.Header.Get("If-None-Match"); inm != "" && inm == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, http.StatusOK, detail)
	}
}

// getSessionTimelineHandler implements GET /api/v1/sessions/{id}/timeline
// (SPEC §4.3). It shares query.ListEvents/timelineEvent with the
// cross-session listEventsHandler in events.go via
// store.EventFilter.SessionID, matching store.Reader.ListEvents' own
// design.
//
// `collapse` (openapi.yaml, default false) is bound nowhere: neither SPEC
// nor store.EventFilter defines what a collapsed timeline row looks like,
// so there is nothing here to implement against — see the P3-07 report.
func getSessionTimelineHandler(reader Reader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if _, err := query.GetSession(r.Context(), reader, id); err != nil {
			writeSessionLookupError(w, r, logger, err)
			return
		}

		q := r.URL.Query()
		order := store.OrderAsc
		if raw := q.Get("order"); raw != "" {
			order = store.SortOrder(raw)
			if !contains(validSortOrders, order) {
				writeProblem(w, r, http.StatusBadRequest, "invalid-parameter",
					"order must be one of "+joinStrings(validSortOrders))
				return
			}
		}
		page, err := bindLimitAndCursor(r, string(order))
		if err != nil {
			writeBindError(w, r, err)
			return
		}
		fields := store.FieldsSlim
		if raw := q.Get("fields"); raw != "" {
			fields = store.Fields(raw)
		}

		f := store.EventFilter{
			SessionID: id,
			Kinds:     castKinds(repeatedParam(r, "kinds")),
			PromptID:  q.Get("prompt_id"),
			AgentID:   q.Get("agent_id"),
			Order:     order,
			Fields:    fields,
		}

		res, err := query.ListEvents(r.Context(), reader, f, page)
		if err != nil {
			writeListStoreError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, timelineListResponse{Data: mapTimelineEvents(res.Events), Page: pageInfoFrom(res.Page)})
	}
}

// listSessionTurnsHandler implements GET /api/v1/sessions/{id}/turns (SPEC
// §4.3), pagination handled in-memory by query.ListTurns (see its doc
// comment for why).
func listSessionTurnsHandler(reader Reader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if _, err := query.GetSession(r.Context(), reader, id); err != nil {
			writeSessionLookupError(w, r, logger, err)
			return
		}

		limit, err := parseLimit(r.URL.Query().Get("limit"))
		if err != nil {
			writeBindError(w, r, err)
			return
		}

		var after *query.TurnsAfter
		if raw := r.URL.Query().Get("cursor"); raw != "" {
			c, decErr := DecodeCursor(raw, query.TurnsSortKey)
			if decErr != nil {
				writeProblem(w, r, http.StatusBadRequest, "invalid-cursor", decErr.Error())
				return
			}
			a, parseErr := turnsAfterFromCursor(c)
			if parseErr != nil {
				writeProblem(w, r, http.StatusBadRequest, "invalid-cursor", parseErr.Error())
				return
			}
			after = &a
		}

		res, err := query.ListTurns(r.Context(), reader, id, after, limit)
		if err != nil {
			writeInternalError(w, r, logger, err)
			return
		}

		var nextCursor *string
		if res.HasMore && len(res.Turns) > 0 {
			last := res.Turns[len(res.Turns)-1]
			enc, encErr := EncodeCursor(query.TurnsSortKey, last.FirstSeenAt, last.PromptID)
			if encErr != nil {
				writeInternalError(w, r, logger, encErr)
				return
			}
			nextCursor = &enc
		}
		writeJSON(w, http.StatusOK, turnsListResponse{
			Data: res.Turns,
			Page: pageInfo{NextCursor: nextCursor, HasMore: res.HasMore},
		})
	}
}

// getSessionSubagentsHandler implements GET /api/v1/sessions/{id}/subagents
// (SPEC §4.3): model.SubagentTree already carries the exact wire shape
// (data + cost_attribution), so it is marshaled directly.
func getSessionSubagentsHandler(reader Reader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if _, err := query.GetSession(r.Context(), reader, id); err != nil {
			writeSessionLookupError(w, r, logger, err)
			return
		}

		tree, err := query.SubagentTree(r.Context(), reader, id)
		if err != nil {
			writeInternalError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, tree)
	}
}

// turnsAfterFromCursor extracts the (first_seen_at, prompt_id) keyset
// position from a cursor already structurally validated and sort-key-bound
// by DecodeCursor(raw, query.TurnsSortKey).
func turnsAfterFromCursor(c Cursor) (query.TurnsAfter, error) {
	if len(c.Values) != 2 {
		return query.TurnsAfter{}, fmt.Errorf("%w: expected 2 values, got %d", ErrInvalidCursor, len(c.Values))
	}
	var ts time.Time
	if err := json.Unmarshal(c.Values[0], &ts); err != nil {
		return query.TurnsAfter{}, fmt.Errorf("%w: invalid first_seen_at: %w", ErrInvalidCursor, err)
	}
	var promptID string
	if err := json.Unmarshal(c.Values[1], &promptID); err != nil {
		return query.TurnsAfter{}, fmt.Errorf("%w: invalid prompt_id: %w", ErrInvalidCursor, err)
	}
	return query.TurnsAfter{FirstSeenAt: ts, PromptID: promptID}, nil
}
