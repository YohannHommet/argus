package sim

import (
	"math"
	"math/rand/v2"
)

// sessionRNG bundles a session's *rand.Rand with a uuid minting helper
// (attrs.go's uuid method), so every synthetic identifier this package
// mints stays inside the seeded PCG stream instead of reaching for
// crypto/rand or math/rand/v2's unseeded global source — either of which
// would break SPEC §7.2's "identical seed ⇒ byte-identical payloads"
// guarantee.
type sessionRNG struct {
	*rand.Rand
}

// newSessionRNG builds the seeded RNG for one session (see sessionRand's
// doc comment for the derivation rule).
func newSessionRNG(seed uint64, sessionOrdinal int) *sessionRNG {
	return &sessionRNG{Rand: sessionRand(seed, sessionOrdinal)}
}

// sessionRNGReader adapts *rand.Rand to io.Reader so google/uuid's
// NewRandomFromReader can mint UUIDs from the same seeded stream (attrs.go).
type sessionRNGReader struct {
	r *rand.Rand
}

// Read fills p with bytes drawn from the wrapped *rand.Rand's stream,
// eight at a time via Uint64, matching the deterministic-fill pattern
// crypto/rand-free RNG adapters commonly use. Never returns an error: a
// math/rand/v2 source cannot fail to produce a value.
func (s sessionRNGReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		v := s.r.Uint64()
		for i := 0; i < 8 && n < len(p); i++ {
			p[n] = byte(v)
			v >>= 8
			n++
		}
	}
	return n, nil
}

// sessionRand derives a session's own *rand.Rand from the run's --seed and
// that session's 0-based ordinal (SPEC §7.2: "per-session generators are
// derived by rand.NewPCG(seed, sessionOrdinal) so a session's content is
// independent of concurrency"). Two runs with the same --seed therefore
// produce byte-identical per-session content regardless of how many workers
// generated them or in what order, which is the property golden_test.go's
// determinism AC depends on.
func sessionRand(seed uint64, sessionOrdinal int) *rand.Rand {
	return rand.New(rand.NewPCG(seed, uint64(sessionOrdinal))) //nolint:gosec // sessionOrdinal is always >=0 by construction (loop counter)
}

// weighted is one (value, probability) pair in a distribution table. Tables
// built from this type are the mechanism §7.1's mixed distributions
// (query_source, tool mix, decision source, terminal.type, ...) use —
// deliberately plain data, never a Go enum (SPEC §0: no closed vocabulary
// over a vendor-supplied string).
type weighted[T any] struct {
	prob float64
	val  T
}

// pick draws one value from a weighted table using r, treating the
// probabilities as a cumulative distribution over [0,1). The table need not
// sum to exactly 1.0 — a shortfall silently falls through to the last
// entry, which keeps every §7.1 table (some of which round to 1.0 only
// approximately, e.g. 0.45+0.25+0.10+0.08+0.07+0.03+0.02=1.00 exactly, but
// others do not) safe against floating-point drift without a normalization
// pass.
func pick[T any](r *rand.Rand, table []weighted[T]) T {
	// The draw is scaled by the table's total weight rather than assuming the
	// weights sum to exactly 1. Some SPEC §7.1 distributions do not: the
	// tool_decision `source` table sums to 1.02 as the spec writes it
	// (0.55+0.05+0.15+0.15+0.08+0.02+0.02), and with an unscaled u in [0,1)
	// the cumulative scan reached 1.0 at `user_abort` and the final entry —
	// the *invented* source value SPEC §7.1 explicitly requires, and which
	// Phase-2 exit criterion 6 asserts reaches the database — could never be
	// drawn at all. Scaling preserves the documented relative weights while
	// making every entry reachable, and protects every other table from the
	// same silent-unreachability class of bug.
	var total float64
	for _, w := range table {
		total += w.prob
	}
	u := r.Float64() * total
	var acc float64
	for _, w := range table {
		acc += w.prob
		if u < acc {
			return w.val
		}
	}
	return table[len(table)-1].val
}

// bernoulli reports true with probability p (0 <= p <= 1). Used throughout
// for the §7.1 per-event coin flips (success rates, occasional event
// probabilities, feature toggles).
func bernoulli(r *rand.Rand, p float64) bool {
	return r.Float64() < p
}

// geometricClamped draws a 1-based geometric count with mean 1/p, clamped
// to [minV, maxV]. SPEC §7.1 item 2: "1-20 turns (geometric, mean 6)" ⇒
// p = 1/6, min=1, max=20. Implemented via inverse-CDF sampling
// (k = ceil(ln(1-u)/ln(1-p))) rather than repeated Bernoulli trials so a
// single r.Float64() draw determines the count, keeping the RNG stream
// position insensitive to the clamp bounds.
func geometricClamped(r *rand.Rand, mean float64, minV, maxV int) int {
	p := 1.0 / mean
	u := r.Float64()
	k := int(math.Ceil(math.Log(1-u) / math.Log(1-p)))
	if k < minV {
		k = minV
	}
	if k > maxV {
		k = maxV
	}
	return k
}

// lognormal draws exp(mu + sigma*Z) for standard normal Z, the distribution
// shape every token/duration/latency field in SPEC §7.1 is specified as
// ("lognormal μ=ln(1500) σ=0.8", etc.).
func lognormal(r *rand.Rand, mu, sigma float64) float64 {
	return math.Exp(mu + sigma*r.NormFloat64())
}

// uniformRange draws a count for "0-N" ranges SPEC §7.1
// specifies only as a mix/range rather than a named distribution (tool
// calls per turn: "0-12 tool calls"). A uniform draw over [minV, maxV] is
// used instead of a true Poisson: SPEC does not name a distribution shape
// for this range the way it does for turns/tokens/durations, and a uniform
// range is the simplest honest reading of "0-12" that does not invent a
// mean the SPEC never states.
func uniformRange(r *rand.Rand, minV, maxV int) int {
	if maxV <= minV {
		return minV
	}
	return minV + r.IntN(maxV-minV+1)
}
