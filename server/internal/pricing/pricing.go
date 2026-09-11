// Package pricing implements cost estimation from SPEC §2.4. It is a pure computation package
// (no database, no other internal packages), so tests run without Postgres.
//
// Originally internal/query/pricing (P3-05 defect 2), moved here to allow both internal/query
// and internal/store/postgres (rollup job) to import it without violating depguard's store rule.
package pricing

import (
	"errors"
	"strings"
	"time"
)

// ErrNoPrice is returned when no model_prices row resolves for the given
// model at the given date — neither an exact match nor a prefix match.
// Callers (the rollup job, P3-05) must store SQL NULL for cost in this
// case, never fall back to zero or another model's price: a silent zero
// would be a lie about a cost Argus simply doesn't know (SPEC §2.4).
var ErrNoPrice = errors.New("pricing: no price for model")

// Price is one model_prices row, reduced to what Estimate needs. The
// postgres package converts DB rows (numeric/date pgtypes) into Price
// values; this package works only with plain Go types.
type Price struct {
	Model             string
	EffectiveFrom     time.Time // date, compared at day granularity
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheReadPerMTok  float64
	CacheWritePerMTok float64
}

// Tokens is one event's token counts, priced separately per SPEC §2.4:
// input, output, cache-read, and cache-write each draw from their own
// *_per_mtok column.
type Tokens struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// Estimate resolves the price row per SPEC §2.4 lookup rule and returns USD cost of tokens.
// Returns ErrNoPrice when nothing resolves; callers must not substitute zero or another model's price.
func Estimate(prices []Price, model string, tokens Tokens, at time.Time) (float64, error) {
	p, ok := resolve(prices, model, at)
	if !ok {
		return 0, ErrNoPrice
	}

	usd := float64(tokens.Input)*p.InputPerMTok/1e6 +
		float64(tokens.Output)*p.OutputPerMTok/1e6 +
		float64(tokens.CacheRead)*p.CacheReadPerMTok/1e6 +
		float64(tokens.CacheWrite)*p.CacheWritePerMTok/1e6
	return usd, nil
}

// resolve picks the winning price table model name (bestCandidateModel),
// then the latest row for that model whose EffectiveFrom is not after at.
func resolve(prices []Price, model string, at time.Time) (Price, bool) {
	candidate := bestCandidateModel(prices, model)
	if candidate == "" {
		return Price{}, false
	}
	return latestAtOrBefore(prices, candidate, at)
}

// bestCandidateModel returns the best matching model per SPEC §2.4: exact match wins,
// else the longest prefix match (SPEC §2.4: "versioned suffix" case). Returns "" if no match.
func bestCandidateModel(prices []Price, model string) string {
	longestPrefix := ""
	for _, p := range prices {
		if p.Model == model {
			return p.Model
		}
		if strings.HasPrefix(model, p.Model) && len(p.Model) > len(longestPrefix) {
			longestPrefix = p.Model
		}
	}
	return longestPrefix
}

// latestAtOrBefore returns the row for the given model with the latest EffectiveFrom not after at.
func latestAtOrBefore(prices []Price, model string, at time.Time) (Price, bool) {
	var best Price
	found := false
	for _, p := range prices {
		if p.Model != model || p.EffectiveFrom.After(at) {
			continue
		}
		if !found || p.EffectiveFrom.After(best.EffectiveFrom) {
			best = p
			found = true
		}
	}
	return best, found
}
