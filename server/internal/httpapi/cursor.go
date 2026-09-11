// Package httpapi — cursor.go implements SPEC §4.1's opaque keyset-pagination cursor.
// Tamper detection is structural: sort-key binding prevents cursors minted for one sort
// being replayed against another (the only path to silent wrong-but-plausible results).
package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrInvalidCursor is returned for structurally invalid or sort-key-mismatched cursors.
var ErrInvalidCursor = errors.New("httpapi: invalid cursor")

// cursorEncoding is URL-safe base64 with no padding (avoids percent-encoding in query strings).
var cursorEncoding = base64.RawURLEncoding

// Cursor is the decoded form of an opaque keyset-pagination cursor (SPEC §4.1).
// Key is the sort; Values hold the keyset tuple [sort_value, id] as raw JSON.
type Cursor struct {
	Key    string            `json:"k"`
	Values []json.RawMessage `json:"v"`
}

// EncodeCursor renders a cursor for sort key and keyset tuple as an opaque wire string.
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

// DecodeCursor parses a cursor and enforces sort-key binding; rejects cursors
// minted for a different sort key or with base64/JSON malformation.
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
