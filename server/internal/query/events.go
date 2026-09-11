package query

import (
	"context"
	"errors"
	"fmt"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// EventReader is the narrow store port for event/tool-call list operations.
// ListEvents/ListToolCalls each serve two endpoints via filter.SessionID (session-scoped or cross-session).
type EventReader interface {
	ListEvents(ctx context.Context, f store.EventFilter, p store.Page) ([]model.Event, store.Cursor, error)
	GetEvent(ctx context.Context, ref model.EventRef) (*model.Event, error)
	ListToolCalls(ctx context.Context, f store.ToolCallFilter, p store.Page) ([]model.ToolCall, store.Cursor, error)
}

// ErrEventNotFound is query's not-found sentinel, recognised from seam-level store.ErrEventNotFound
// (same rationale as ErrSessionNotFound: no concrete backend dependency).
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
