package ingest

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// RetryClass is SPEC §3.6's three-way classification of a
// store.Writer.WriteBatch/WriteMetrics failure. It exists as its own type
// (rather than just branching on the error inline) so retry.go's rules and
// pipeline.go's retry-loop mechanics stay in separate, independently
// testable files — see retry_test.go for the classification table.
type RetryClass int

const (
	// ClassNone means err was nil: nothing to classify.
	ClassNone RetryClass = iota
	// ClassConflict is 40P01 (deadlock_detected) / 40001 (serialization_failure):
	// expected under concurrency thanks to the lock-ordering invariant
	// (SPEC §1.6), retried up to ARGUS_INGEST_RETRY_CONFLICT times because a
	// flat low retry count would drop data on a routine deadlock.
	ClassConflict
	// ClassTransient is a connection-level failure (08xxx, 57P01, a context
	// deadline) or anything Postgres-shaped SPEC §3.6 doesn't explicitly
	// name — see ClassifyError's default case — retried up to
	// ARGUS_INGEST_RETRY_TRANSIENT times.
	ClassTransient
	// ClassPermanent is a constraint or programming error (23xxx, 42xxx):
	// retrying it would just fail the same way forever, so SPEC §3.6 says
	// drop it immediately and log the batch's first event id at ERROR.
	ClassPermanent
)

// String renders the class as the Prometheus label value SPEC §3.6 uses
// ("class=\"permanent\"" etc.).
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

// conflictSQLSTATEs / transientSQLSTATEPrefix / permanentSQLSTATEPrefixes
// encode SPEC §3.6's exact classification table.
var conflictSQLSTATEs = map[string]bool{
	"40P01": true, // deadlock_detected
	"40001": true, // serialization_failure
}

const transientAdminShutdown = "57P01"

var permanentSQLSTATEPrefixes = []string{"23", "42"}

// ClassifyError applies SPEC §3.6's retry classification to a
// WriteBatch/WriteMetrics error. A non-pgconn error (including
// context.DeadlineExceeded, which pgx does not wrap in a *pgconn.PgError)
// and any SQLSTATE class the spec does not name both fall through to
// ClassTransient: SPEC §3.6 only ever names three outcomes and the two data
// -loss-averse ones (conflict, transient) are bounded retries, so an unknown
// failure mode gets a bounded number of chances rather than either being
// dropped on first sight (ruled out by SPEC §3.6's whole premise) or
// retried forever like a genuine, expected deadlock. context.Canceled is
// classified the same way for the same reason: internal/ingest's own Close
// cancels its worker context on a drain-deadline timeout (see pipeline.go),
// which surfaces here as a Canceled error on whatever batch was in flight —
// treating it as transient means the batch gets exactly one more bounded
// shot at the (already-expired) retry budget before being dropped and
// counted, never silently discarded.
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

// conflictBackoffBase is SPEC §3.6's "jittered backoff from 5ms" starting
// point for ClassConflict retries.
const conflictBackoffBase = 5 * time.Millisecond

// conflictBackoff returns the delay before conflict-retry attempt n
// (1-based, i.e. the delay before the (n+1)th call to WriteBatch): a linear
// ramp from the 5ms base, jittered by up to 50% so many workers retrying
// the same deadlock don't re-collide in lockstep.
func conflictBackoff(n int) time.Duration {
	base := conflictBackoffBase * time.Duration(n)
	jitter := time.Duration(rand.Float64() * float64(base) / 2) //nolint:gosec // backoff jitter, not security-sensitive
	return base + jitter
}

// transientBackoffSchedule is SPEC §3.6's fixed schedule for
// ClassTransient retries: 100ms, 400ms, 1.6s for attempts 1, 2, 3. A
// transient budget higher than 3 repeats the last (longest) delay rather
// than panicking or growing unbounded.
var transientBackoffSchedule = []time.Duration{
	100 * time.Millisecond,
	400 * time.Millisecond,
	1600 * time.Millisecond,
}

// transientBackoff returns the delay before transient-retry attempt n
// (1-based).
func transientBackoff(n int) time.Duration {
	if n <= 0 {
		return transientBackoffSchedule[0]
	}
	if n > len(transientBackoffSchedule) {
		return transientBackoffSchedule[len(transientBackoffSchedule)-1]
	}
	return transientBackoffSchedule[n-1]
}
