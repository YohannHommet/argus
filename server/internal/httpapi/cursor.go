// Package httpapi — cursor.go implements SPEC §4.1's opaque keyset-
// pagination cursor: "URL-safe base64 of {"k":"<sort key>","v":[…]}; opaque,
// validated, 400 on tamper." It is a pure codec with no store/DB dependency
// (depguard forbids internal/store importing internal/httpapi, SPEC §3.1),
// so postgres.ListSessions — the first consumer of this wire format —
// necessarily carries its own small decode/encode of the identical
// "base64(json({k,v}))" shape rather than importing this package; that
// duplication is deliberate and documented on the postgres side, not an
// oversight here.
//
// Tamper detection is structural validation only, by design: DecodeCursor
// rejects anything that is not valid base64, not valid JSON, missing a key
// or values, or minted for a different sort key than the one it is being
// replayed against — but it does not carry an HMAC or other integrity tag.
// This is safe because a cursor's values are not secrets (they are the same
// sort-column value and id already visible in the previous page's response
// body) and because "sort-key binding" (below) closes the only path by
// which a structurally-valid-but-wrong cursor could produce a
// wrong-but-plausible page: without binding, a cursor minted while sorting
// by `cost_usd` could be replayed against a `started_at`-sorted request, and
// the keyset predicate would silently seek using the wrong column's
// semantics, returning a page that looks valid but skips or repeats rows.
// Binding the "k" field into the payload and checking it on every decode
// eliminates that failure mode without needing a MAC: any mutation that
// does not also forge a matching "k" is caught by ordinary shape validation
// (a flipped byte breaks base64 or JSON far more often than it produces
// another well-formed cursor), and a mutation that *does* preserve valid
// shape can, at worst, seek to a different valid position in the same
// sorted set — never a different sort order or a different filter, since
// pagination cursors carry no filter state at all (SPEC §4.1's filters are
// re-supplied by the client on every request, never embedded in the
// cursor).
package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrInvalidCursor is returned by DecodeCursor for any structurally invalid
// or sort-key-mismatched cursor (SPEC §4.1: "opaque, validated, 400 on
// tamper"). httpapi/problem.go maps it onto urn:argus:error:invalid-cursor.
var ErrInvalidCursor = errors.New("httpapi: invalid cursor")

// cursorEncoding is URL-safe base64 with no padding: a cursor travels as a
// query-string value, where '=' padding would need percent-encoding for no
// benefit (the same choice model.EventRef's eventRefEncoding makes for the
// same reason).
var cursorEncoding = base64.RawURLEncoding

// Cursor is the decoded form of an opaque keyset-pagination cursor (SPEC
// §4.1). Key names the sort this cursor was minted under (e.g.
// "last_event_at"); Values holds the keyset tuple in the same column order
// as the backing index — the sort column's value followed by the `id`
// tiebreak (SPEC §2.1: every sort index carries `id DESC` as the tiebreak)
// — encoded as raw JSON so any JSON-safe value (string, number, null)
// round-trips without this package needing to know each sort key's Go type.
type Cursor struct {
	Key    string            `json:"k"`
	Values []json.RawMessage `json:"v"`
}

// EncodeCursor renders a cursor for sort key `key` and keyset tuple
// `values` (typically [sortColumnValue, id]) as the opaque wire string SPEC
// §4.1 describes. Each value is marshalled independently with
// encoding/json, so callers can mix types (e.g. a time.Time sort value
// alongside a string id) freely.
func EncodeCursor(key string, values ...any) (string, error) {
	raw := make([]json.RawMessage, len(values))
	for i, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("httpapi: encode cursor: marshal value %d: %w", i, err)
		}
		raw[i] = b
	}
	body, err := json.Marshal(Cursor{Key: key, Values: raw})
	if err != nil {
		return "", fmt.Errorf("httpapi: encode cursor: %w", err)
	}
	return cursorEncoding.EncodeToString(body), nil
}

// DecodeCursor parses a cursor minted by EncodeCursor and enforces
// sort-key binding: a cursor minted for one sort key (wantKey) is rejected
// with ErrInvalidCursor when replayed against a different one (see the
// package doc comment above for why this is the load-bearing tamper check,
// not a MAC). Any base64/JSON malformation, or a payload missing its key or
// values, is likewise ErrInvalidCursor.
func DecodeCursor(s, wantKey string) (Cursor, error) {
	raw, err := cursorEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: not valid base64: %w", ErrInvalidCursor, err)
	}
	var c Cursor
	if jsonErr := json.Unmarshal(raw, &c); jsonErr != nil {
		return Cursor{}, fmt.Errorf("%w: not valid JSON: %w", ErrInvalidCursor, jsonErr)
	}
	if c.Key == "" || len(c.Values) == 0 {
		return Cursor{}, fmt.Errorf("%w: missing key or values", ErrInvalidCursor)
	}
	if c.Key != wantKey {
		return Cursor{}, fmt.Errorf("%w: minted for sort %q, replayed against %q", ErrInvalidCursor, c.Key, wantKey)
	}
	return c, nil
}
