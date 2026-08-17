package sim

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDemo_LastOrdinalProjectNotForcedByOrdinal is ticket W15's regression
// test ("argusd sim --sessions=N undercount").
//
// The documented ledger diagnosis (docs/review/phase-3-deviations.md)
// blamed backfillOffset placing the last ordinal's events "at or past
// now". That theory does not survive reading the code: RunDemo's clock
// origin is FixedEpoch under --out (clock.go), nothing in this package's
// demo path compares a generated timestamp against time.Now(), and
// generateSession's own RNG draws are entirely independent of
// startOffset. The actual mechanism was in RunDemo's project assignment:
// `projects[ordinal%len(projects)]` put legacy-app — the one project
// SPEC §7.1 makes metrics-only, so a session assigned to it correctly
// emits no log/hook events and thus no `sessions` row — at a *fixed*
// residue that depends only on ordinal, never on the run's seed. Whenever
// --sessions was a multiple of len(projects), ordinal sessions-1 (the
// run's last session) always landed on that residue, so the last session
// of *every* seed's run was unconditionally metrics-only. That is what
// forced an operator to over-provision --sessions to reliably clear
// Phase 4 exit criterion 1 ("/sessions lists >= 20 sessions").
//
// This test reproduces that exact condition (--sessions == len(projects),
// so ordinal sessions-1's old residue was guaranteed to be legacy-app's)
// across many seeds. Before the fix every single trial landed on
// legacy-app (40/40). After the fix it is an ordinary ~1-in-len(projects)
// draw, so requiring under half the trials to land on legacy-app is a
// margin no honest uniform draw would trip while still catching the old
// 100%-forced behaviour.
func TestDemo_LastOrdinalProjectNotForcedByOrdinal(t *testing.T) {
	t.Parallel()

	const sessions = 5 // == len(projects): the exact condition the old bug required
	require.Len(t, projects, sessions, "test setup: sessions must equal len(projects) to reproduce the old residue collision")

	const trials = 40
	legacyCount := 0
	for seed := uint64(1); seed <= trials; seed++ {
		if demoProjectAssignment(seed, sessions)[sessions-1] == legacyAppProject {
			legacyCount++
		}
	}

	require.Less(t, legacyCount, trials/2,
		"ordinal sessions-1's project landed on the metrics-only project in %d/%d seeds; "+
			"it must be an ordinary per-seed draw, not forced by ordinal alone", legacyCount, trials)
}

// TestDemo_NSessionsYieldNDistinctRealisticSessions is the ticket's
// broader AC: "--sessions=N yields N sessions that each carry a realistic
// event count". It runs the exact loop RunDemo runs (backfillOffset +
// demoProjectAssignment + generateSession) for --sessions=20 — Phase 4 exit
// criterion 1's own number — across many seeds, and checks two properties
// that must hold for *every* seed, not just a hand-picked lucky one:
//
//  1. N distinct session IDs are always produced (never fewer, regardless
//     of which ordinals happen to draw the metrics-only project).
//  2. Sessions assigned to a logs-carrying project are not overwhelmingly
//     thin. Natural variance (15% abandoned sessions, geometric turn count
//     with a minimum of 1) means an occasional thin session is expected
//     and must not fail this test — SPEC's distribution is not uniform by
//     design. What must never happen again is the old defect's signature:
//     a *majority* of a run's non-metrics-only sessions collapsing to a
//     handful of events because of a positional (ordinal-based) bias
//     rather than genuine per-session randomness.
func TestDemo_NSessionsYieldNDistinctRealisticSessions(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Sessions = 20
	cfg.Backfill = demoDefaultBackfill
	clock := NewClock(FixedEpoch)

	const trials = 25
	for seed := uint64(1); seed <= trials; seed++ {
		cfg.Seed = seed

		assignment := demoProjectAssignment(cfg.Seed, cfg.Sessions)
		ids := make(map[string]bool, cfg.Sessions)
		nonLegacy, thin := 0, 0
		for ordinal := 0; ordinal < cfg.Sessions; ordinal++ {
			startOffset := backfillOffset(ordinal, cfg.Sessions, cfg.Backfill)
			project := assignment[ordinal]
			result := generateSession(cfg, clock, ordinal, startOffset, project)

			ids[result.SessionID] = true
			if project != legacyAppProject {
				nonLegacy++
				// A single turn with no tool calls still emits a handful of
				// log events (SessionStart's HookRegistered, user_prompt,
				// >=1 api_request, assistant_response); fewer than that is
				// only possible through the old ordinal-forced collapse.
				if len(result.Logs) < 4 {
					thin++
				}
			}
		}

		require.Lenf(t, ids, cfg.Sessions, "seed %d: expected %d distinct sessions, got %d", seed, cfg.Sessions, len(ids))
		require.Positivef(t, nonLegacy, "seed %d: expected at least one non-metrics-only session among %d", seed, cfg.Sessions)
		require.Lessf(t, thin, nonLegacy/2+1, "seed %d: %d/%d non-metrics-only sessions came back near-empty", seed, thin, nonLegacy)
	}
}

// TestPickProject_OrdinalZeroIsAlwaysLogsCapable pins the contract
// session.go's --chaos-clock-skew anchor depends on. That event is emitted
// only when `logsOnly && sessionOrdinal == 0`, so if ordinal 0 ever draws
// the metrics-only legacy-app project, the run's single beyond-retention
// repro is silently not emitted at all — no error, no warning, just a
// counter that never moves. That is exactly how it was found: the
// end-to-end chaos assertion went red for seed 303 the moment project
// selection became a per-ordinal draw.
//
// Asserted across many seeds rather than one, because a single seed passing
// is what made the ordinal-modulo version look fine for so long.
func TestPickProject_OrdinalZeroIsAlwaysLogsCapable(t *testing.T) {
	t.Parallel()

	for seed := uint64(1); seed <= 500; seed++ {
		require.NotEqual(t, legacyAppProject, demoProjectAssignment(seed, 25)[0],
			"seed %d: ordinal 0 must be a logs-capable project, or --chaos-clock-skew's once-per-run beyond-retention event is never emitted", seed)
	}

	// The pin must not flatten the distribution for everyone else: later
	// ordinals still have to be able to draw legacy-app, or the
	// "logs exporter appears off" demo case (SPEC §7.1) has no data.
	sawLegacyApp := false
	for ordinal := 1; ordinal < 200 && !sawLegacyApp; ordinal++ {
		if demoProjectAssignment(7, 200)[ordinal] == legacyAppProject {
			sawLegacyApp = true
		}
	}
	require.True(t, sawLegacyApp,
		"legacy-app must still be reachable for ordinals past 0, or SPEC §7.1's metrics-only demo case never appears")
}

// TestDemoProjectAssignment_YieldsEnoughSessionRowsForEverySeed pins the
// property Phase 4's exit criterion 1 actually depends on: a default demo
// run must put at least 20 sessions in the session list, for every seed —
// not on average.
//
// A metrics-only project correctly produces no `sessions` row (SPEC §4.3's
// metrics_only_projects), so the session-row count is the number of
// logs-capable ordinals. Under the balanced allocation that count is a
// function of --sessions alone, with no seed variance, which is the whole
// reason the allocation is balanced rather than an independent per-ordinal
// draw: an i.i.d. draw at the same default left 34% of seeds below 20, and
// the worst seed at 15. This asserts the invariant across a wide seed
// sample so a future change back to i.i.d. draws fails here — cheaply —
// rather than in a Phase-4 exit-criteria run.
func TestDemoProjectAssignment_YieldsEnoughSessionRowsForEverySeed(t *testing.T) {
	t.Parallel()

	// The floor Phase 4's exit criterion 1 states.
	const wantSessionRows = 20

	worst := demoDefaultSessions
	for seed := uint64(1); seed <= 2000; seed++ {
		rows := 0
		for _, project := range demoProjectAssignment(seed, demoDefaultSessions) {
			if project != legacyAppProject {
				rows++
			}
		}
		if rows < worst {
			worst = rows
		}
	}

	require.GreaterOrEqual(t, worst, wantSessionRows,
		"the default demo run (--sessions=%d) must yield >= %d session rows for every seed (worst seen: %d); "+
			"Phase 4 exit criterion 1 requires the session list to show at least %d from a demo run",
		demoDefaultSessions, wantSessionRows, worst, wantSessionRows)
}
