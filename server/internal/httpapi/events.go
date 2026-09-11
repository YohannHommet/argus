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

// validSortOrders is SPEC §4.3's closed order vocabulary.
var validSortOrders = []store.SortOrder{store.OrderAsc, store.OrderDesc}

// timelineEvent is SPEC §4.3's TimelineEvent wire shape, adapted from model.Event.
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

// eventDetail is GET /api/v1/events/{ref}'s body: TimelineEvent plus attrs.
type eventDetail struct {
	timelineEvent
	Attrs map[string]any `json:"attrs"`
}

// timelineListResponse is the shared body for GET /events and GET /sessions/{id}/timeline.
type timelineListResponse struct {
	Data []timelineEvent `json:"data"`
	Page pageInfo        `json:"page"`
}

// newTimelineEvent adapts model.Event into timelineEvent wire shape.
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

// mountEventRoutes attaches the cross-session event routes.
func mountEventRoutes(r chi.Router, reader Reader, logger *slog.Logger) {
	r.Get("/events", listEventsHandler(reader, logger))
	r.Get("/events/{ref}", getEventHandler(reader, logger))
}

// listEventsHandler implements GET /api/v1/events (SPEC §4.3): cross-session events.
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

// getEventHandler implements GET /api/v1/events/{ref} (SPEC §4.1, §4.3).
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
