// Package query is httpapi's read-service layer, sitting between the HTTP
// handlers and internal/store (SPEC §3.1: "httpapi -> query -> store").
// Parameter binding and validation lives in httpapi/params.go; this package
// owns request-shaped read services — assembling a filter + page from
// already-validated inputs, calling the store, and computing the SPEC
// §4.1 page envelope (next_cursor/has_more) plus the session-detail ETag
// (SPEC §4.1: "hash of the underlying max(ts,seq) + filter"). It never
// builds SQL of its own.
package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/store"
)

// SessionReader is the narrow store port ListSessions/GetSession/ListTurns/
// SubagentTree need — the same consumer-owned-port convention
// httpapi/router.go already establishes for HealthChecker/
// MigrationsChecker: internal/store.Store satisfies this structurally, but
// query depends only on the methods it actually calls.
type SessionReader interface {
	ListSessions(ctx context.Context, f store.SessionFilter, p store.Page) ([]model.SessionSummary, store.Cursor, error)
	GetSession(ctx context.Context, id string) (*model.SessionDetail, error)
	ListTurns(ctx context.Context, sessionID string) ([]model.Turn, error)
	SubagentTree(ctx context.Context, sessionID string) (model.SubagentTree, error)
}

// ErrSessionNotFound is query's not-found sentinel, recognised from seam-level store.ErrSessionNotFound
// (SPEC §3.1's direction: query depends on the interface, not implementations).
// P3-07 moved it to the seam so storetest.Fake can signal 404 in conformance tests.
var ErrSessionNotFound = errors.New("query: session not found")

// Page is the SPEC §4.1 pagination envelope query computes for every
// store-paginated list endpoint: NextCursor == "" means no next page
// (httpapi renders that as JSON null, never an empty string).
type Page struct {
	NextCursor string
	HasMore    bool
}

func pageFrom(cur store.Cursor) Page {
	return Page{NextCursor: string(cur), HasMore: cur != ""}
}

// SessionsResult is ListSessions' result: the page of rows plus its
// pagination envelope.
type SessionsResult struct {
	Sessions []model.SessionSummary
	Page     Page
}

// ListSessions calls through to the store and wraps its result with the
// page envelope. f and p are assumed already validated by
// httpapi/params.go.
func ListSessions(ctx context.Context, r SessionReader, f store.SessionFilter, p store.Page) (SessionsResult, error) {
	sessions, cur, err := r.ListSessions(ctx, f, p)
	if err != nil {
		return SessionsResult{}, fmt.Errorf("query: list sessions: %w", err)
	}
	return SessionsResult{Sessions: sessions, Page: pageFrom(cur)}, nil
}

// GetSession fetches one session's detail, mapping the store's
// backend-specific not-found error onto ErrSessionNotFound (see its doc
// comment).
func GetSession(ctx context.Context, r SessionReader, id string) (*model.SessionDetail, error) {
	detail, err := r.GetSession(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("query: get session %q: %w", id, err)
	}
	return detail, nil
}

// SessionETag hashes session position (last_event_at + event_count as proxy for max(ts,seq), SPEC §4.1)
// plus session id to ensure no collisions. Per RFC 7232 ETag syntax.
func SessionETag(s *model.SessionDetail) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", s.ID, s.LastEventAt.UTC().UnixNano(), s.EventCount)))
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// SubagentTree calls through to the store unchanged: model.SubagentTree carries SPEC §4.3's exact wire shape (SPEC §1.9).
func SubagentTree(ctx context.Context, r SessionReader, sessionID string) (model.SubagentTree, error) {
	tree, err := r.SubagentTree(ctx, sessionID)
	if err != nil {
		return model.SubagentTree{}, fmt.Errorf("query: subagent tree for session %q: %w", sessionID, err)
	}
	return tree, nil
}

// TurnsSortKey is the cursor-binding tag for GET /api/v1/sessions/{id}/turns.
// Reader.ListTurns takes no store-level pagination (SPEC §3.3), so paging is in-memory here.
const TurnsSortKey = "first_seen_at"

// TurnsAfter is the decoded keyset position ListTurns resumes after —
// httpapi decodes the incoming `?cursor=` into this shape via
// httpapi.DecodeCursor(raw, TurnsSortKey) before calling ListTurns, and
// encodes the next one from the last returned Turn the same way.
type TurnsAfter struct {
	FirstSeenAt time.Time
	PromptID    string
}

// TurnsResult is ListTurns' result: the page of rows plus whether more
// remain past it.
type TurnsResult struct {
	Turns   []model.Turn
	HasMore bool
}

// ListTurns fetches and sorts every turn by (first_seen_at, prompt_id),
// returning the page after `after` (nil = from start) up to limit rows.
func ListTurns(ctx context.Context, r SessionReader, sessionID string, after *TurnsAfter, limit int) (TurnsResult, error) {
	turns, err := r.ListTurns(ctx, sessionID)
	if err != nil {
		return TurnsResult{}, fmt.Errorf("query: list turns for session %q: %w", sessionID, err)
	}

	sort.SliceStable(turns, func(i, j int) bool {
		if !turns[i].FirstSeenAt.Equal(turns[j].FirstSeenAt) {
			return turns[i].FirstSeenAt.Before(turns[j].FirstSeenAt)
		}
		return turns[i].PromptID < turns[j].PromptID
	})

	start := 0
	if after != nil {
		start = sort.Search(len(turns), func(i int) bool {
			t := turns[i]
			if !t.FirstSeenAt.Equal(after.FirstSeenAt) {
				return t.FirstSeenAt.After(after.FirstSeenAt)
			}
			return t.PromptID > after.PromptID
		})
	}
	turns = turns[start:]

	hasMore := len(turns) > limit
	if hasMore {
		turns = turns[:limit]
	}
	return TurnsResult{Turns: turns, HasMore: hasMore}, nil
}
