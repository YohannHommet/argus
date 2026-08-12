package sim

// modelPrice holds per-million-token USD prices for one model, used to
// compute cost_usd_micros so a generated api_request's reported cost is
// self-consistent with its own token counts (SPEC §7.1: "cost_usd_micros
// from a built-in price table so reported cost is self-consistent"). The
// figures are illustrative Argus-internal constants for the demo/test
// fixture — SPEC does not tie the simulator to real Anthropic pricing, and
// the fidelity rule (SPEC §7) governs *attribute vocabulary*, not the
// numeric value of a self-consistency computation the sim itself owns.
type modelPrice struct {
	inputPerM         float64
	outputPerM        float64
	cacheReadPerM     float64
	cacheCreationPerM float64
}

// priceTable is the built-in price table SPEC §7.1 requires. Models named
// here match the fixed model set §7.1 draws from (claude-opus-5,
// claude-sonnet-4-5, claude-haiku-4-5); an unrecognized model (never
// produced by this generator today, but defensive against a future model
// set change) falls back to priceTable[fallbackModel] in costMicros.
var priceTable = map[string]modelPrice{
	"claude-opus-5":     {inputPerM: 15, outputPerM: 75, cacheReadPerM: 1.5, cacheCreationPerM: 18.75},
	"claude-sonnet-4-5": {inputPerM: 3, outputPerM: 15, cacheReadPerM: 0.3, cacheCreationPerM: 3.75},
	"claude-haiku-4-5":  {inputPerM: 0.8, outputPerM: 4, cacheReadPerM: 0.08, cacheCreationPerM: 1},
}

// fallbackModel is used by costMicros when model is absent from
// priceTable, so a future model-set change never panics or divides by an
// undefined price.
const fallbackModel = "claude-sonnet-4-5"

// costMicros computes cost_usd_micros for one api_request from its token
// counts using priceTable, mirroring how Claude Code itself reports a
// self-consistent cost_usd/cost_usd_micros pair for a request (live
// capture: both fields present on api_request, §1.5.1 mapping table:
// "cost_usd_micros/1e6 → cost_usd (preferred … more precision)"). Returns
// micros (integer, matching the wire's intValue encoding).
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
