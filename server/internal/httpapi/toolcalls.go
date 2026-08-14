package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/query"
	"github.com/YohannHommet/argus/server/internal/store"
)

// toolCallSortKey is the fixed cursor-binding tag both tool-call list
// endpoints use, matching internal/store/postgres/read_toolcalls.go's own
// `toolCallCursorKey` ("started_at"): neither endpoint exposes a `sort`/
// `order` parameter (openapi.yaml), so there is exactly one order.
const toolCallSortKey = "started_at"

// toolCall is the wire shape SPEC's ToolCall schema declares — openapi.yaml
// documents its field names as "mirror[ing] internal/model.ToolCall's Go
// fields snake_cased", but model.ToolCall itself carries no JSON tags at
// all (unlike every other model type this ticket marshals directly), so a
// bare json.Marshal(model.ToolCall) would emit exact-case Go field names
// ("ID", "SessionID", "DurationMS", ...) instead of the contract's
// snake_case. This adapter is what satisfies the contract in practice —
// flagged as a P3-07 report item alongside timelineEvent's own version of
// the same gap.
type toolCall struct {
	ID              string            `json:"id"`
	SessionID       string            `json:"session_id"`
	PromptID        *string           `json:"prompt_id"`
	ToolUseID       *string           `json:"tool_use_id"`
	ToolName        string            `json:"tool_name"`
	ToolSource      *string           `json:"tool_source"`
	AgentID         *string           `json:"agent_id"`
	Decision        *string           `json:"decision"`
	DecisionSource  *string           `json:"decision_source"`
	PermissionMode  *string           `json:"permission_mode"`
	StartedAt       time.Time         `json:"started_at"`
	DecidedAt       *time.Time        `json:"decided_at"`
	EndedAt         *time.Time        `json:"ended_at"`
	DurationMS      *int              `json:"duration_ms"`
	WaitMS          *int              `json:"wait_ms"`
	Success         *bool             `json:"success"`
	ErrorType       *string           `json:"error_type"`
	FilePath        *string           `json:"file_path"`
	InputSizeBytes  *int              `json:"input_size_bytes"`
	ResultSizeBytes *int              `json:"result_size_bytes"`
	Correlation     model.Correlation `json:"correlation"`
	EventCount      int               `json:"event_count"`
}

// toolCallsListResponse is the shared body shape of GET
// /api/v1/sessions/{id}/tool-calls and GET /api/v1/tool-calls
// (openapi.yaml's ToolCallsListResponse).
type toolCallsListResponse struct {
	Data []toolCall `json:"data"`
	Page pageInfo   `json:"page"`
}

func newToolCall(tc model.ToolCall) toolCall {
	return toolCall{
		ID:              tc.ID,
		SessionID:       tc.SessionID,
		PromptID:        tc.PromptID,
		ToolUseID:       tc.ToolUseID,
		ToolName:        tc.ToolName,
		ToolSource:      tc.ToolSource,
		AgentID:         tc.AgentID,
		Decision:        tc.Decision,
		DecisionSource:  tc.DecisionSource,
		PermissionMode:  tc.PermissionMode,
		StartedAt:       tc.StartedAt,
		DecidedAt:       tc.DecidedAt,
		EndedAt:         tc.EndedAt,
		DurationMS:      tc.DurationMS,
		WaitMS:          tc.WaitMS,
		Success:         tc.Success,
		ErrorType:       tc.ErrorType,
		FilePath:        tc.FilePath,
		InputSizeBytes:  tc.InputSizeBytes,
		ResultSizeBytes: tc.ResultSizeBytes,
		Correlation:     tc.Correlation,
		EventCount:      tc.EventCount,
	}
}

func mapToolCalls(calls []model.ToolCall) []toolCall {
	out := make([]toolCall, len(calls))
	for i, c := range calls {
		out[i] = newToolCall(c)
	}
	return out
}

// mountToolCallRoutes attaches both tool-call list routes: the
// session-scoped drill-down and the cross-session one, sharing
// query.ListToolCalls via store.ToolCallFilter.SessionID.
func mountToolCallRoutes(r chi.Router, reader Reader) {
	r.Get("/sessions/{id}/tool-calls", listSessionToolCallsHandler(reader))
	r.Get("/tool-calls", listToolCallsHandler(reader))
}

// listSessionToolCallsHandler implements GET
// /api/v1/sessions/{id}/tool-calls (SPEC §4.2).
func listSessionToolCallsHandler(reader Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if _, err := query.GetSession(r.Context(), reader, id); err != nil {
			writeSessionLookupError(w, r, err)
			return
		}

		page, err := bindLimitAndCursor(r, toolCallSortKey)
		if err != nil {
			writeBindError(w, r, err)
			return
		}

		f := store.ToolCallFilter{
			SessionID:      id,
			Tool:           repeatedParam(r, "tool"),
			DecisionSource: repeatedParam(r, "decision_source"),
		}

		res, err := query.ListToolCalls(r.Context(), reader, f, page)
		if err != nil {
			writeProblem(w, r, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toolCallsListResponse{Data: mapToolCalls(res.ToolCalls), Page: pageInfoFrom(res.Page)})
	}
}

// listToolCallsHandler implements GET /api/v1/tool-calls (SPEC §4.2): the
// cross-session decision-provenance drill-down.
func listToolCallsHandler(reader Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, err := bindLimitAndCursor(r, toolCallSortKey)
		if err != nil {
			writeBindError(w, r, err)
			return
		}
		from, to, err := parseTimeWindow(r)
		if err != nil {
			writeBindError(w, r, err)
			return
		}

		f := store.ToolCallFilter{
			Project:        repeatedParam(r, "project"),
			Tool:           repeatedParam(r, "tool"),
			DecisionSource: repeatedParam(r, "decision_source"),
			From:           from,
			To:             to,
		}

		res, err := query.ListToolCalls(r.Context(), reader, f, page)
		if err != nil {
			writeProblem(w, r, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toolCallsListResponse{Data: mapToolCalls(res.ToolCalls), Page: pageInfoFrom(res.Page)})
	}
}
