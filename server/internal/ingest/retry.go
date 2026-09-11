package ingest

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// RetryClass is SPEC §3.6's three-way failure classification (retry.go rules separate from pipeline logic).
type RetryClass int

const (
	// ClassNone means err was nil: nothing to classify.
	ClassNone RetryClass = iota
	// ClassConflict is 40P01/40001 (deadlock/serialization), expected under concurrency (SPEC §1.6).
	ClassConflict
	// ClassTransient is connection failure (08xxx, 57P01, deadline) or unknown SPEC-unspecified.
	ClassTransient
	// ClassPermanent is constraint/programming error (23xxx, 42xxx) — drop immediately.
	ClassPermanent
)

// String renders as Prometheus label value (SPEC §3.6: "class=\"permanent\"" etc.).
func (c RetryClass) String() string {
	switch c {
	case ClassConflict:
		return "conflict"
	case ClassTransient:
		return "transient"
	case ClassPermanent:
		return "permanent"
	case ClassNone:
		return "none"
	default:
		return "none"
	}
}

// conflictSQLSTATEs encode SPEC §3.6 classification table.
var conflictSQLSTATEs = map[string]bool{
	"40P01": true, // deadlock_detected
	"40001": true, // serialization_failure
}

const transientAdminShutdown = "57P01"

// permanentSQLSTATEPrefixes: SQLSTATE classes retry cannot fix (SPEC §3.6: 23, 42; added: 22 deviation).
var permanentSQLSTATEPrefixes = []string{"22", "23", "42"}

// ClassifyError applies SPEC §3.6 classification (unknown errors → ClassTransient for data safety).
func ClassifyError(err error) RetryClass {
	if err == nil {
		return ClassNone
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ClassTransient
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if conflictSQLSTATEs[pgErr.Code] {
			return ClassConflict
		}
		if strings.HasPrefix(pgErr.Code, "08") || pgErr.Code == transientAdminShutdown {
			return ClassTransient
		}
		for _, prefix := range permanentSQLSTATEPrefixes {
			if strings.HasPrefix(pgErr.Code, prefix) {
				return ClassPermanent
			}
		}
	}
	return ClassTransient
}

// conflictBackoffBase is SPEC §3.6's starting point for jittered conflict backoff.
const conflictBackoffBase = 5 * time.Millisecond

// conflictBackoff returns delay before attempt n (1-based): linear ramp + jitter to avoid lockstep.
func conflictBackoff(n int) time.Duration {
	base := conflictBackoffBase * time.Duration(n)
	jitter := time.Duration(rand.Float64() * float64(base) / 2) //nolint:gosec // backoff jitter, not security-sensitive
	return base + jitter
}

// transientBackoffSchedule is SPEC §3.6's fixed schedule (100ms, 400ms, 1.6s).
var transientBackoffSchedule = []time.Duration{
	100 * time.Millisecond,
	400 * time.Millisecond,
	1600 * time.Millisecond,
}

// transientBackoff returns delay before attempt n (1-based).
func transientBackoff(n int) time.Duration {
	if n <= 0 {
		return transientBackoffSchedule[0]
	}
	if n > len(transientBackoffSchedule) {
		return transientBackoffSchedule[len(transientBackoffSchedule)-1]
	}
	return transientBackoffSchedule[n-1]
}
