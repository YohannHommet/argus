package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// canonicalJSON renders v as JSON with sorted keys and no insignificant
// whitespace (SPEC §1.7 rule 2: "canonical_* = JSON with sorted keys and no
// insignificant whitespace, so hashing is stable across map iteration
// order"). encoding/json already sorts map[string]T keys and emits compact
// output by default at every nesting level, so this is exactly canonical
// JSON without a bespoke encoder — verified by TestCanonicalJSON_StableAcrossIterationOrder.
func canonicalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("model: canonical JSON: %w", err)
	}
	return string(b), nil
}

// sha256Hex16 is SPEC §1.7 rule 2's `sha256_16`: the first 16 bytes of
// SHA-256, hex-encoded (32 hex characters).
func sha256Hex16(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

// DedupKeyOTelLog builds the otel_log dedup key (SPEC §1.7 rule 2). When
// vendorSeq is non-nil, event.sequence's verified per-session-monotonic
// property makes {session_id}:{vendor_seq}:{event_name} already a valid
// key on its own; the content hash is kept as cheap insurance against a
// future regression in that counter. When vendorSeq is nil (the field is
// absent from the record), the hash-fallback form is used instead, keyed
// only on session + content hash.
//
// record is the full canonical source record the hash is computed over —
// callers pass the same attrs map they will store in Event.Attrs.
func DedupKeyOTelLog(sessionID string, vendorSeq *int64, eventName string, record map[string]any) (string, error) {
	canon, err := canonicalJSON(record)
	if err != nil {
		return "", err
	}
	hash := sha256Hex16(canon)
	if vendorSeq != nil {
		return fmt.Sprintf("otel:%s:%d:%s:%s", sessionID, *vendorSeq, eventName, hash), nil
	}
	return fmt.Sprintf("otel:%s:h:%s", sessionID, hash), nil
}

// DedupKeyMetric builds the otel_metric dedup key (SPEC §1.7 rule 2):
// "metric:{sha256_16(name|ts|canonical_attrs)}".
func DedupKeyMetric(name string, ts time.Time, attrs map[string]any) (string, error) {
	canon, err := canonicalJSON(attrs)
	if err != nil {
		return "", err
	}
	payload := name + "|" + ts.UTC().Format(time.RFC3339Nano) + "|" + canon
	return "metric:" + sha256Hex16(payload), nil
}

// DedupKeyHook builds the hook dedup key (SPEC §1.7 rule 2):
// "hook:{sha256_16(hook_event_name|session_id|prompt_id|canonical_json(body))}".
// Receipt-time ts on hook events makes any ts-bearing unique key useless for
// hook dedup, so this ledger key is the only mechanism (SPEC §1.7 rule 2).
func DedupKeyHook(hookEventName, sessionID, promptID string, body map[string]any) (string, error) {
	canon, err := canonicalJSON(body)
	if err != nil {
		return "", err
	}
	payload := hookEventName + "|" + sessionID + "|" + promptID + "|" + canon
	return "hook:" + sha256Hex16(payload), nil
}
