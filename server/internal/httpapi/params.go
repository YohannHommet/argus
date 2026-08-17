package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/YohannHommet/argus/server/internal/model"
	"github.com/YohannHommet/argus/server/internal/query"
	"github.com/YohannHommet/argus/server/internal/store"
)

// defaultLimit / maxLimit are SPEC §4.1's pagination defaults ("?limit=
// (default 50, max 500)"), shared by every list endpoint this ticket binds.
const (
	defaultLimit = 50
	maxLimit     = 500
)

// paramError is httpapi's internal representation of one invalid query/path
// parameter. Its Error() string is already the exact `detail` text every
// `urn:argus:error:invalid-parameter` problem+json response uses (SPEC
// §4.1, openapi.yaml's BadRequest.invalidParameter example: "from: not a
// valid RFC 3339 timestamp or relative shorthand: ..."), so handlers never
// reformat it — see writeBindError.
type paramError struct {
	param   string
	message string
}

func (e *paramError) Error() string {
	return e.param + ": " + e.message
}

func newParamError(param, format string, args ...any) *paramError {
	return &paramError{param: param, message: fmt.Sprintf(format, args...)}
}

// parseLimit binds `?limit=` (SPEC §4.1). Absent or explicit "0" defaults
// to 50; anything above 500 clamps silently to 500 (not an error); a
// negative or non-numeric value is a paramError naming "limit" (P3-07
// ticket note: limit=9999 clamps, it is not an error — only a negative or
// non-numeric value is).
func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, newParamError("limit", "not a valid integer: %q", raw)
	}
	if n < 0 {
		return 0, newParamError("limit", "must not be negative: %d", n)
	}
	if n == 0 {
		return defaultLimit, nil
	}
	if n > maxLimit {
		return maxLimit, nil
	}
	return n, nil
}

// parseRelativeShorthand parses SPEC §4.1's relative time shorthand (`-24h`,
// `-7d`) into the time.Duration to add to "now". Go's time.ParseDuration
// already accepts "-24h" natively (h/m/s/ms/us/ns units); only the "d"
// (days) unit needs hand-rolling, since ParseDuration has no day unit by
// design — SPEC's shorthand is casual enough that a fixed 24h/day is fine
// here.
func parseRelativeShorthand(raw string) (time.Duration, bool) {
	if d, err := time.ParseDuration(raw); err == nil {
		return d, true
	}
	if !strings.HasSuffix(raw, "d") {
		return 0, false
	}
	days, err := strconv.ParseFloat(strings.TrimSuffix(raw, "d"), 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(days * float64(24*time.Hour)), true
}

// parseTimeParam binds a single `from`/`to` value (SPEC §4.1: RFC 3339 or
// relative shorthand). now is resolved once per request by the caller
// (parseTimeWindow) so `from`/`to` never straddle two different clock
// reads. An empty raw value means "absent" (nil, nil); callers apply their
// own endpoint-specific default (unbounded for sessions, -24h for
// analytics — SPEC §4.1).
func parseTimeParam(name, raw string, now time.Time) (*time.Time, error) {
	if raw == "" {
		return nil, nil //nolint:nilnil // absent is a valid, distinct outcome from "invalid" here — see doc comment
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		t = t.UTC()
		return &t, nil
	}
	if d, ok := parseRelativeShorthand(raw); ok {
		t := now.Add(d)
		return &t, nil
	}
	return nil, newParamError(name, "not a valid RFC 3339 timestamp or relative shorthand: %q", raw)
}

// parseTimeWindow binds `from`/`to` together against one shared "now" (see
// parseTimeParam's doc comment), so a relative pair like `from=-7d&to=-1d`
// resolves both ends against the same instant rather than two clock reads
// that could straddle a boundary.
func parseTimeWindow(r *http.Request) (from, to *time.Time, err error) {
	now := time.Now()
	q := r.URL.Query()
	from, err = parseTimeParam("from", q.Get("from"), now)
	if err != nil {
		return nil, nil, err
	}
	to, err = parseTimeParam("to", q.Get("to"), now)
	if err != nil {
		return nil, nil, err
	}
	return from, to, nil
}

// repeatedParam returns every value of a repeated query parameter (SPEC
// §4.1: "repeated params OR within a field"), or nil if absent.
func repeatedParam(r *http.Request, name string) []string {
	return r.URL.Query()[name]
}

// castSessionStatuses casts repeated `status` values to model.SessionStatus
// without validating against the closed set: an unrecognized value simply
// matches no stored session (store's own filter is a plain column
// equality), which is a friendlier failure mode than a 400 for what is,
// after all, one of SessionFilter's OR-set fields.
func castSessionStatuses(raw []string) []model.SessionStatus {
	if len(raw) == 0 {
		return nil
	}
	out := make([]model.SessionStatus, len(raw))
	for i, s := range raw {
		out[i] = model.SessionStatus(s)
	}
	return out
}

// castKinds casts repeated `kinds` values to model.Kind — same
// no-strict-validation reasoning as castSessionStatuses.
func castKinds(raw []string) []model.Kind {
	if len(raw) == 0 {
		return nil
	}
	out := make([]model.Kind, len(raw))
	for i, s := range raw {
		out[i] = model.Kind(s)
	}
	return out
}

// decodeCursorParam validates `?cursor=` against wantKey (SPEC §4.1:
// "opaque, validated, 400 on tamper") and, if valid, passes the original
// string through unchanged as a store.Cursor: internal/store's backends own
// their own decode of this identical wire format (cursor.go's package doc
// explains why), so httpapi never re-encodes what it already validated.
func decodeCursorParam(r *http.Request, wantKey string) (store.Cursor, error) {
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return "", nil
	}
	if _, err := DecodeCursor(raw, wantKey); err != nil {
		return "", err
	}
	return store.Cursor(raw), nil
}

// bindLimitAndCursor is the common `limit`+`cursor` binding sequence every
// store-paginated list handler needs (SPEC §4.1). wantKey names the
// sort/order concept this endpoint's cursor is bound to — a sort key name
// for sessions, an order direction for events, a fixed tag for tool calls
// (see each handler's own comment for which, mirroring the corresponding
// postgres implementation's cursor payload).
func bindLimitAndCursor(r *http.Request, wantKey string) (store.Page, error) {
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return store.Page{}, err
	}
	cursor, err := decodeCursorParam(r, wantKey)
	if err != nil {
		return store.Page{}, err
	}
	return store.Page{Cursor: cursor, Limit: limit}, nil
}

// writeBindError maps a binding error from parseLimit/parseTimeWindow/
// decodeCursorParam/bindLimitAndCursor onto the right problem+json slug:
// a *paramError becomes urn:argus:error:invalid-parameter (naming the
// offending parameter in `detail`, per the BadRequest.invalidParameter
// example), anything else (only ever ErrInvalidCursor-wrapping errors, in
// practice) becomes urn:argus:error:invalid-cursor.
func writeBindError(w http.ResponseWriter, r *http.Request, err error) {
	var pe *paramError
	if errors.As(err, &pe) {
		writeProblem(w, r, http.StatusBadRequest, "invalid-parameter", err.Error())
		return
	}
	writeProblem(w, r, http.StatusBadRequest, "invalid-cursor", err.Error())
}

// writeSessionLookupError maps query.GetSession's error onto the right
// problem+json response: query.ErrSessionNotFound is SPEC §4.3's `GET
// /api/v1/sessions/{id}` 404 (and, by reuse, every session-scoped
// sub-resource's own 404 — see each handler's existence-check comment);
// anything else is an unexpected store failure (m2 audit finding: routed
// through writeInternalError so it never echoes err's own text to the
// client).
func writeSessionLookupError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	if errors.Is(err, query.ErrSessionNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not-found", "no such resource")
		return
	}
	writeInternalError(w, r, logger, err)
}

// pageInfo is the SPEC §4.1 pagination envelope's wire shape
// (`{"next_cursor": "…"|null, "has_more": bool}`), shared by every list
// response this ticket implements.
type pageInfo struct {
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// pageInfoFrom renders a query.Page as the wire pageInfo: an empty
// NextCursor (query.Page's "no next page" convention) becomes JSON null,
// never an empty string.
func pageInfoFrom(p query.Page) pageInfo {
	if p.NextCursor == "" {
		return pageInfo{HasMore: false}
	}
	nc := p.NextCursor
	return pageInfo{NextCursor: &nc, HasMore: p.HasMore}
}
