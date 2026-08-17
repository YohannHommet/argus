package model

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EventRef identifies one row of `events` by its primary key, (ts, seq)
// (SPEC §1.2, §2.2). It is the opaque `event_ref` on every event payload and
// the sole lookup key for GET /api/v1/events/{ref} — there is no index on
// events.id, so id is never a lookup key (SPEC §1.2).
type EventRef struct {
	TS  time.Time
	Seq int64

	// DedupKey is populated only on the EventRef values store.Writer.
	// WriteBatch returns in BatchResult.EventRefs (internal/ingest's
	// matchPersisted keys off it to map a persisted ref back to the
	// submitted batch event it belongs to — SPEC §3.6/§5.3, audit finding
	// M1). It plays no part in Encode/DecodeEventRef: the wire `event_ref`
	// stays exactly (ts, seq), and every other EventRef consumer (GetEvent,
	// pagination cursors, conformance fixtures) leaves this field zero.
	DedupKey string
}

// eventRefEncoding is base64url with no padding: SPEC §1.2 says "base64url
// of ts+seq"; padding characters ('=') would need escaping in a path
// segment for no benefit.
var eventRefEncoding = base64.RawURLEncoding

// Encode renders r as the opaque `event_ref` string SPEC §1.2 and §4.1
// describe: base64url of a "<ts-unix-nanos>:<seq>" payload. TS is
// normalized to UTC so two EventRefs for the same instant in different
// locations encode identically.
func (r EventRef) Encode() string {
	raw := strconv.FormatInt(r.TS.UTC().UnixNano(), 10) + ":" + strconv.FormatInt(r.Seq, 10)
	return eventRefEncoding.EncodeToString([]byte(raw))
}

// DecodeEventRef parses a string produced by EventRef.Encode. Any tampering
// — a flipped character breaking base64, or valid base64 that no longer
// decodes to "<int>:<int>" — is rejected with an error rather than silently
// producing a wrong (ts, seq) pair, per SPEC §4.1's "opaque, validated, 400
// on tamper" rule for cursors, which the same convention applies to here.
func DecodeEventRef(s string) (EventRef, error) {
	raw, err := eventRefEncoding.DecodeString(s)
	if err != nil {
		return EventRef{}, fmt.Errorf("model: decode event_ref: %w", err)
	}
	tsPart, seqPart, ok := strings.Cut(string(raw), ":")
	if !ok {
		return EventRef{}, errors.New("model: decode event_ref: malformed payload")
	}
	nanos, err := strconv.ParseInt(tsPart, 10, 64)
	if err != nil {
		return EventRef{}, fmt.Errorf("model: decode event_ref: invalid ts: %w", err)
	}
	seq, err := strconv.ParseInt(seqPart, 10, 64)
	if err != nil {
		return EventRef{}, fmt.Errorf("model: decode event_ref: invalid seq: %w", err)
	}
	return EventRef{TS: time.Unix(0, nanos).UTC(), Seq: seq}, nil
}
