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

// ErrSessionNotFound is query's own not-found sentinel for GetSession
// (SPEC §4.3's `GET /api/v1/sessions/{id}` 404, and — via the
// existence-check every session-scoped sub-resource handler runs first —
// every other `/sessions/{id}/...` 404 too).
//
// It is recognised from the seam-level store.ErrSessionNotFound, so this
// package depends on the store.Reader interface alone (SPEC §3.1's
// direction) and any implementation — including storetest.Fake — can signal
// a 404. P3-07 originally matched internal/store/postgres.ErrSessionNotFound
// directly; the sentinel was moved onto the seam in review, because a Fake
// that cannot produce it would make the conformance suite validate a 500
// where production returns 404.
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

// SessionETag hashes a session detail's (max(ts,seq)) position — here,
// last_event_at + event_count, the closest proxy for "underlying
// max(ts,seq)" GetSession's return shape exposes (SPEC §4.1: "hash of the
// underlying max(ts,seq) + filter"; GetSession takes no filter, so the
// digest input is the position alone) — plus the session id, so two
// different sessions' tags never collide. Quoted per RFC 7232's ETag
// syntax.
func SessionETag(s *model.SessionDetail) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", s.ID, s.LastEventAt.UTC().UnixNano(), s.EventCount)))
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// SubagentTree calls through to the store unchanged: model.SubagentTree
// already carries SPEC §4.3's exact wire shape (including
// cost_attribution.per_node_available: false and a nil cost_usd on every
// node, SPEC §1.9), so there is nothing for query to compute here beyond
// the call itself.
func SubagentTree(ctx context.Context, r SessionReader, sessionID string) (model.SubagentTree, error) {
	tree, err := r.SubagentTree(ctx, sessionID)
	if err != nil {
		return model.SubagentTree{}, fmt.Errorf("query: subagent tree for session %q: %w", sessionID, err)
	}
	return tree, nil
}

// TurnsSortKey is the fixed cursor-binding tag for `GET
// /api/v1/sessions/{id}/turns`. Reader.ListTurns takes no store-level
// filter/page (SPEC §3.3's interface has no pagination on this method —
// per-session turn counts are always small, unlike events/tool-calls), so
// ListTurns paginates the store's full result in memory here — query-layer
// behaviour, not SQL, per the ticket note that this package must never
// become one. httpapi encodes/decodes the actual cursor string (only
// httpapi owns that codec, per cursor.go's package doc); this package only
// works with the decoded position.
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

// ListTurns fetches every turn of sessionID, sorts them by
// (first_seen_at, prompt_id) — first_seen_at is never null and monotonic
// with ingestion order, unlike the nullable started_at/turn_index — and
// returns the page starting just after `after` (nil means "from the
// start"), up to limit rows.
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
