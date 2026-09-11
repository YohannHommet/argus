package postgres_test

// upsert_session_cost_test.go is D-30's projection-level regression test
// (docs/review/phase-4-gauntlet.md, candidate D-30, owner-ratified
// 2026-08-18): before this fix, upsert_session.go/upsert_turn.go only ever
// added to cost_estimated_usd when an event already carried
// cost_source="estimated" — a value nothing in this codebase ever mints
// (otel_logs.go stamped "reported" unconditionally, and no other producer
// sets it at all). So an uncosted llm.request (--cost-mode=omit, or any
// agent that just doesn't report cost) folded to a hard cost_estimated_usd=0
// forever, even though internal/pricing could estimate it from model_prices
// — exactly the "measured $0.00 for cost it cannot know" defect the ticket
// title names. These tests drive the real WriteBatch (never a hand-rolled
// SQL insert) and read the result back through the real ListSessions/
// GetSession/ListTurns path, so they fail before the fix and pass after it
// for the actual reason (the fold, not the read side, which SPEC §2.4
// already got right).

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/model"
)

// --- a single uncosted llm.request with a known model must estimate from
// model_prices, and the estimate must fully explain both cost.usd and
// cost.estimated_share (SPEC §2.4: "cost_usd = reported + estimated").

func TestWriteBatch_SessionCost_UncostedLLMRequestEstimatesFromModelPrices(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	summary, err := st.ImportPrices(ctx)
	require.NoError(t, err)
	require.Positive(t, summary.Inserted)

	sessionID := "session-cost-estimate-basic"
	promptID := "prompt-cost-estimate-basic"
	// No withCost: cost_usd stays NULL (the --cost-mode=omit shape), so this
	// is exactly the case the read side used to render as a silent $0.00.
	ev := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base,
		withPromptID(promptID), withModel("claude-opus-5"), withTokens(1_000_000, 1_000_000))

	_, err = st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err)

	detail, err := st.GetSession(ctx, sessionID)
	require.NoError(t, err)

	// db/prices/model_prices.json seeds claude-opus-5 at input=15,
	// output=75 per Mtok effective 2025-01-01 (same rate rollups_test.go's
	// TestRunRollups_UncostedLLMRequestEstimatedFromModelPrices pins for the
	// rollup job's own use of the identical internal/pricing.Estimate call):
	// 1M input + 1M output tokens = 15 + 75 = 90 USD.
	require.InDelta(t, 90.0, detail.Cost.EstimatedUSD, 1e-6, "cost.estimated_usd must match internal/pricing.Estimate's rate for claude-opus-5")
	require.Zero(t, detail.Cost.ReportedUSD, "no event carried a reported cost_usd")
	require.InDelta(t, 90.0, detail.Cost.USD, 1e-6, "cost.usd = reported + estimated (SPEC §2.4)")
	require.InDelta(t, 1.0, detail.Cost.EstimatedShare, 1e-9, "an all-estimated session must report estimated_share=1, not 0")

	turns, err := st.ListTurns(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, turns, 1)
	require.InDelta(t, 90.0, turns[0].CostEstimatedUSD, 1e-6, "turns.cost_estimated_usd must be populated the same way sessions.cost_estimated_usd is")
	require.Zero(t, turns[0].CostUSD)
}

// --- a session mixing a reported event and an uncosted-but-priceable event
// must split its cost across both fields rather than collapsing to one.

func TestWriteBatch_SessionCost_MixedReportedAndEstimatedSplitsAcrossBothFields(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base.Add(time.Hour))

	summary, err := st.ImportPrices(ctx)
	require.NoError(t, err)
	require.Positive(t, summary.Inserted)

	sessionID := "session-cost-estimate-mixed"
	promptID := "prompt-cost-estimate-mixed"

	reportedEv := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base,
		withPromptID(promptID), withModel("claude-sonnet-4-5"), withCost(2.5, "reported"))
	// Same turn, same session, a later request that reports no cost at all
	// but does carry a model + tokens the seeded price table can resolve.
	uncostedEv := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base.Add(time.Minute),
		withPromptID(promptID), withModel("claude-opus-5"), withTokens(1_000_000, 1_000_000))

	_, err = st.WriteBatch(ctx, []model.Event{reportedEv, uncostedEv})
	require.NoError(t, err)

	detail, err := st.GetSession(ctx, sessionID)
	require.NoError(t, err)

	require.InDelta(t, 2.5, detail.Cost.ReportedUSD, 1e-9)
	require.InDelta(t, 90.0, detail.Cost.EstimatedUSD, 1e-6)
	require.InDelta(t, 92.5, detail.Cost.USD, 1e-6)
	require.InDelta(t, 90.0/92.5, detail.Cost.EstimatedShare, 1e-9)

	// SPEC §2.1: cost_by_query_source sums *reported* cost only — widening
	// it to include estimated contributions would be an undocumented
	// deviation this ticket was explicitly told not to make.
	require.InDelta(t, 2.5, detail.Cost.ByQuerySource[""], 1e-9,
		"cost_by_query_source must stay reported-only: the estimated event must not land in it")

	turns, err := st.ListTurns(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, turns, 1)
	require.InDelta(t, 2.5, turns[0].CostUSD, 1e-9)
	require.InDelta(t, 90.0, turns[0].CostEstimatedUSD, 1e-6)
}

// --- an uncosted llm.request for a model with no model_prices row must
// contribute exactly 0 to cost_estimated_usd (never a fabricated stand-in
// from another model's price, per internal/pricing.Estimate's documented
// contract) and must never fail the write.

func TestWriteBatch_SessionCost_UncostedLLMRequestWithNoPriceRow_ContributesZeroNoError(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 1, 11, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	// Deliberately no ImportPrices call (mirrors
	// TestRunRollups_UncostedLLMRequestWithNoMatchingPrice_StaysZero): with
	// model_prices empty, every model is unresolvable, exercising the
	// pricing.ErrNoPrice "contribute nothing" branch via WriteBatch's own
	// price-loading path (not the rollup job's).
	sessionID := "session-cost-estimate-nopricerow"
	promptID := "prompt-cost-estimate-nopricerow"
	ev := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base,
		withPromptID(promptID), withModel("some-unknown-model-xyz"), withTokens(1_000_000, 1_000_000))

	_, err := st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err, "an unresolvable price must never fail the write")

	detail, err := st.GetSession(ctx, sessionID)
	require.NoError(t, err)
	require.Zero(t, detail.Cost.EstimatedUSD)
	require.Zero(t, detail.Cost.ReportedUSD)
	require.Zero(t, detail.Cost.USD)
	require.Zero(t, detail.Cost.EstimatedShare)

	turns, err := st.ListTurns(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, turns, 1)
	require.Zero(t, turns[0].CostEstimatedUSD)
	require.Zero(t, turns[0].CostUSD)
}
