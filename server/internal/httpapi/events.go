package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/query"
	"github.com/YohannHommet/argus/server/internal/store"
)

// validSortOrders is SPEC §4.3's closed `order` vocabulary ("order=asc|
// desc"), shared by GET /api/v1/events and GET
// /api/v1/sessions/{id}/timeline (sessions.go's getSessionTimelineHandler)
// — same closed-vocabulary-needs-a-400 reasoning as sessions.go's
// validSessionSorts (m1 audit finding).
var validSortOrders = []store.SortOrder{store.OrderAsc, store.OrderDesc}

// timelineEvent is the wire shape SPEC §4.3/openapi.yaml's TimelineEvent
// schema declares, adapted from model.Event. It cannot be model.Event
// marshaled directly: model.Event has no JSON tags at all (it mirrors the
// `events` table 1:1, a storage shape, not a wire one — see its own doc
// comment), and even with tags added the shapes differ structurally
// (model.Event's four flat *Tokens fields nest into one `tokens` object or
// null; CostUSD becomes a single nullable `cost` number, not an object;
// `event_ref` doesn't exist on model.Event at all — it's computed from
// (TS, Seq); IngestedAt/VendorSeq/RequestID/MessageUUID/DedupKey/
// ParentAgentID/Attrs are internal-only and never on the wire here). This
// is flagged as a P3-07 report item: the ticket's "marshal model types
// directly" guidance holds for SessionSummary/SessionDetail/Turn/
// SubagentTree, but not for Event/ToolCall.
type timelineEvent struct {
	EventRef       string            `json:"event_ref"`
	Seq            int64             `json:"seq"`
	ID             string            `json:"id"`
	TS             time.Time         `json:"ts"`
	SessionID      string            `json:"session_id"`
	PromptID       *string           `json:"prompt_id"`
	Kind           model.Kind        `json:"kind"`
	EventName      string            `json:"event_name"`
	Source         model.Source      `json:"source"`
	Vendor         string            `json:"vendor"`
	ToolName       *string           `json:"tool_name"`
	ToolUseID      *string           `json:"tool_use_id"`
	Decision       *string           `json:"decision"`
	DecisionSource *string           `json:"decision_source"`
	ToolSource     *string           `json:"tool_source"`
	QuerySource    *string           `json:"query_source"`
	Model          *string           `json:"model"`
	Tokens         *model.TokenUsage `json:"tokens"`
	Cost           *float64          `json:"cost"`
	DurationMS     *int              `json:"duration_ms"`
	Success        *bool             `json:"success"`
	ErrorType      *string           `json:"error_type"`
	AgentID        *string           `json:"agent_id"`
	AgentType      *string           `json:"agent_type"`
	PermissionMode *string           `json:"permission_mode"`
	FilePath       *string           `json:"file_path"`
	ClockSkewed    bool              `json:"clock_skewed"`
}

// eventDetail is GET /api/v1/events/{ref}'s body (openapi.yaml's
// EventDetail: TimelineEvent plus `attrs`). timelineEvent's fields promote
// unqualified into the JSON object since it is embedded without its own
// tag.
type eventDetail struct {
	timelineEvent
	Attrs map[string]any `json:"attrs"`
}

// timelineListResponse is GET /api/v1/events' and GET
// /api/v1/sessions/{id}/timeline's shared body shape (openapi.yaml's
// TimelineListResponse).
type timelineListResponse struct {
	Data []timelineEvent `json:"data"`
	Page pageInfo        `json:"page"`
}

// newTimelineEvent adapts one model.Event into its wire shape (see
// timelineEvent's doc comment for why this adapter, not direct marshaling,
// is required).
func newTimelineEvent(e model.Event) timelineEvent {
	var tokens *model.TokenUsage
	if e.InputTokens != nil || e.OutputTokens != nil || e.CacheReadTokens != nil || e.CacheCreationTokens != nil {
		tokens = &model.TokenUsage{
			Input:         derefInt64(e.InputTokens),
			Output:        derefInt64(e.OutputTokens),
			CacheRead:     derefInt64(e.CacheReadTokens),
			CacheCreation: derefInt64(e.CacheCreationTokens),
		}
	}
	return timelineEvent{
		EventRef:       (model.EventRef{TS: e.TS, Seq: e.Seq}).Encode(),
		Seq:            e.Seq,
		ID:             e.ID,
		TS:             e.TS,
		SessionID:      e.SessionID,
		PromptID:       e.PromptID,
		Kind:           e.Kind,
		EventName:      e.EventName,
		Source:         e.Source,
		Vendor:         e.Vendor,
		ToolName:       e.ToolName,
		ToolUseID:      e.ToolUseID,
		Decision:       e.Decision,
		DecisionSource: e.DecisionSource,
		ToolSource:     e.ToolSource,
		QuerySource:    e.QuerySource,
		Model:          e.Model,
		Tokens:         tokens,
		Cost:           e.CostUSD,
		DurationMS:     e.DurationMS,
		Success:        e.Success,
		ErrorType:      e.ErrorType,
		AgentID:        e.AgentID,
		AgentType:      e.AgentType,
		PermissionMode: e.PermissionMode,
		FilePath:       e.FilePath,
		ClockSkewed:    e.ClockSkewed,
	}
}

func mapTimelineEvents(events []model.Event) []timelineEvent {
	out := make([]timelineEvent, len(events))
	for i, e := range events {
		out[i] = newTimelineEvent(e)
	}
	return out
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// mountEventRoutes attaches the cross-session event routes this ticket
// owns.
func mountEventRoutes(r chi.Router, reader Reader, logger *slog.Logger) {
	r.Get("/events", listEventsHandler(reader, logger))
	r.Get("/events/{ref}", getEventHandler(reader, logger))
}

// listEventsHandler implements GET /api/v1/events (SPEC §4.3): the
// cross-session counterpart of getSessionTimelineHandler, sharing
// query.ListEvents/timelineEvent via store.EventFilter.SessionID == "".
func listEventsHandler(reader Reader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		from, to, err := parseTimeWindow(r)
		if err != nil {
			writeBindError(w, r, err)
			return
		}
		fields := store.FieldsSlim
		if raw := q.Get("fields"); raw != "" {
			fields = store.Fields(raw)
		}

		f := store.EventFilter{
			Kinds:          castKinds(repeatedParam(r, "kinds")),
			PromptID:       q.Get("prompt_id"),
			AgentID:        q.Get("agent_id"),
			Tool:           repeatedParam(r, "tool"),
			DecisionSource: repeatedParam(r, "decision_source"),
			Project:        repeatedParam(r, "project"),
			Vendor:         repeatedParam(r, "vendor"),
			From:           from,
			To:             to,
			Order:          order,
			Fields:         fields,
		}

		res, err := query.ListEvents(r.Context(), reader, f, page)
		if err != nil {
			writeListStoreError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, timelineListResponse{Data: mapTimelineEvents(res.Events), Page: pageInfoFrom(res.Page)})
	}
}

// getEventHandler implements GET /api/v1/events/{ref} (SPEC §4.1, §4.3): a
// `ref` that does not decode is 400 urn:argus:error:invalid-event-ref; a
// well-formed `ref` naming no row is 404.
func getEventHandler(reader Reader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := chi.URLParam(r, "ref")
		ref, err := model.DecodeEventRef(raw)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid-event-ref", "event_ref is not valid base64url of ts:seq")
			return
		}

		event, err := query.GetEvent(r.Context(), reader, ref)
		if err != nil {
			if errors.Is(err, query.ErrEventNotFound) {
				writeProblem(w, r, http.StatusNotFound, "not-found", "no such resource")
				return
			}
			writeInternalError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, eventDetail{timelineEvent: newTimelineEvent(*event), Attrs: event.Attrs})
	}
}
