// Package pricing implements the cost-estimation half of docs/SPEC.md
// §2.4: given a model name, a set of token counts, and the event's
// timestamp, resolve the applicable model_prices row and compute a USD
// cost. It is a pure computation package — no database, no
// internal/store, no internal/httpapi import — so its tests run without a
// live Postgres. internal/store/postgres/prices.go owns reading rows out
// of model_prices and hands them to Estimate as a []Price.
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

// Estimate resolves the price row for model at the event date `at` and
// returns the USD cost of tokens, per the SPEC §2.4 lookup rule: "latest
// effective_from <= event date, exact model, else longest matching
// prefix". Exact match always wins over any prefix match; among prefix
// candidates the longest price-table model name that is a prefix of model
// wins; within the chosen model, the row with the latest EffectiveFrom not
// after `at` wins. Returns ErrNoPrice when nothing resolves at all — the
// caller must never substitute zero or another model's price.
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

// bestCandidateModel returns, among the distinct model names present in
// prices, the one that best matches the observed model per SPEC §2.4: an
// exact match wins outright; otherwise the longest price-table model name
// that is a prefix of model wins (the "versioned suffix" case, e.g. a
// price row for "claude-sonnet-4-5" matching an observed
// "claude-sonnet-4-5-20250929"). Returns "" when nothing matches.
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

// latestAtOrBefore returns the row for the given model with the latest
// EffectiveFrom not after at, or false if the model has no such row (e.g.
// every row for it is dated after at).
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
