package query

import (
	"context"
	"errors"
	"fmt"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// EventReader is the narrow store port ListEvents/GetEvent/ListToolCalls
// need — the same consumer-owned-port convention as SessionReader.
// ListEvents/ListToolCalls each serve two endpoints apiece (session-scoped
// and cross-session — store.EventFilter.SessionID / store.
// ToolCallFilter.SessionID is what distinguishes them, matching how
// store.Reader itself shares one method per pair, SPEC §4.2/§4.3).
type EventReader interface {
	ListEvents(ctx context.Context, f store.EventFilter, p store.Page) ([]model.Event, store.Cursor, error)
	GetEvent(ctx context.Context, ref model.EventRef) (*model.Event, error)
	ListToolCalls(ctx context.Context, f store.ToolCallFilter, p store.Page) ([]model.ToolCall, store.Cursor, error)
}

// ErrEventNotFound is query's own not-found sentinel for GetEvent (SPEC
// §4.1's `GET /api/v1/events/{ref}` 404). Recognised from the seam-level
// store.ErrEventNotFound, so this package needs no dependency on a concrete
// backend — same rationale as ErrSessionNotFound in sessions.go.
var ErrEventNotFound = errors.New("query: event not found")

// EventsResult is ListEvents' result: the page of rows plus its pagination
// envelope.
type EventsResult struct {
	Events []model.Event
	Page   Page
}

// ListEvents calls through to the store and wraps its result with the page
// envelope. f and p are assumed already validated by httpapi/params.go.
func ListEvents(ctx context.Context, r EventReader, f store.EventFilter, p store.Page) (EventsResult, error) {
	events, cur, err := r.ListEvents(ctx, f, p)
	if err != nil {
		return EventsResult{}, fmt.Errorf("query: list events: %w", err)
	}
	return EventsResult{Events: events, Page: pageFrom(cur)}, nil
}

// GetEvent fetches one event by its (ts, seq) primary key, mapping the
// store's backend-specific not-found error onto ErrEventNotFound.
func GetEvent(ctx context.Context, r EventReader, ref model.EventRef) (*model.Event, error) {
	event, err := r.GetEvent(ctx, ref)
	if err != nil {
		if errors.Is(err, store.ErrEventNotFound) {
			return nil, ErrEventNotFound
		}
		return nil, fmt.Errorf("query: get event: %w", err)
	}
	return event, nil
}

// ToolCallsResult is ListToolCalls' result: the page of rows plus its
// pagination envelope.
type ToolCallsResult struct {
	ToolCalls []model.ToolCall
	Page      Page
}

// ListToolCalls calls through to the store and wraps its result with the
// page envelope. f and p are assumed already validated by
// httpapi/params.go.
func ListToolCalls(ctx context.Context, r EventReader, f store.ToolCallFilter, p store.Page) (ToolCallsResult, error) {
	calls, cur, err := r.ListToolCalls(ctx, f, p)
	if err != nil {
		return ToolCallsResult{}, fmt.Errorf("query: list tool calls: %w", err)
	}
	return ToolCallsResult{ToolCalls: calls, Page: pageFrom(cur)}, nil
}
