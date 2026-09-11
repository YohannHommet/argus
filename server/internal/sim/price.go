package sim

// modelPrice holds per-million-token USD prices for cost_usd_micros
// computation (SPEC §7.1). Figures are illustrative, not real pricing.
type modelPrice struct {
	inputPerM         float64
	outputPerM        float64
	cacheReadPerM     float64
	cacheCreationPerM float64
}

// priceTable is the built-in price table (SPEC §7.1). Models match §7.1's
// fixed set (claude-opus-5, claude-sonnet-4-5, claude-haiku-4-5).
var priceTable = map[string]modelPrice{
	"claude-opus-5":     {inputPerM: 15, outputPerM: 75, cacheReadPerM: 1.5, cacheCreationPerM: 18.75},
	"claude-sonnet-4-5": {inputPerM: 3, outputPerM: 15, cacheReadPerM: 0.3, cacheCreationPerM: 3.75},
	"claude-haiku-4-5":  {inputPerM: 0.8, outputPerM: 4, cacheReadPerM: 0.08, cacheCreationPerM: 1},
}

// fallbackModel is used by costMicros when model is absent from
// priceTable, so a future model-set change never panics or divides by an
// undefined price.
const fallbackModel = "claude-sonnet-4-5"

// costMicros computes cost_usd_micros from token counts and priceTable,
// mirroring Claude Code's self-consistent cost reporting.
func costMicros(model string, inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens int64) int64 {
	p, ok := priceTable[model]
	if !ok {
		p = priceTable[fallbackModel]
	}
	usd := float64(inputTokens)*p.inputPerM/1e6 +
		float64(outputTokens)*p.outputPerM/1e6 +
		float64(cacheReadTokens)*p.cacheReadPerM/1e6 +
		float64(cacheCreationTokens)*p.cacheCreationPerM/1e6
	return int64(usd * 1e6)
}
