# Phase 2 accepted deviations

All entries below deviate from SPEC.md/PLAN.md as originally written; SPEC.md and PLAN.md have
been amended to match. Owner accepted all five on 2026-08-13.

| ID | Deviation | Rationale | Status |
|---|---|---|---|
| D-13 | `Writer` interface gains `WriteMetrics` (SPEC §3.3) | §1.8/§2.3 require OTel metric data points to land in `metric_samples`; they arrive as `[]model.MetricSample`, never `model.Event` (no `Kind` exists for a metric, §1.4) — the spec's `Writer` only listed `WriteBatch` | accepted 2026-08-13 |
| D-14 | Partition manager also creates backward to the retention horizon, not only current+2 (SPEC §2.4) | An in-retention backfill crossing a month boundary must not hit a missing partition and be misclassified `too_old`; not yet implemented — tracked as new ticket **P3-12** in PLAN.md | accepted 2026-08-13 |
| D-15 | `rollup_dirty` is created by `003_projections.sql`, not `004_rollups.sql` (SPEC §2.4 / PLAN P3-04) | P2-06 needed `rollup_dirty` to exist so `WriteBatch`'s single transaction could mark it alongside `events`/projections, before `004` exists in the build order; P3-04 must not re-create it | accepted 2026-08-13 |
| D-16 | OTLP ingestion hand-decodes the envelope instead of importing the `collector/.../v1` wrapper types (SPEC §3.2) | That subpackage's generated code pulls in `grpc-gateway/v2` and `google.golang.org/grpc`, neither otherwise a dependency of this module; `internal/ingest/otlp/codec.go` hand-decodes the one repeated top-level field in both wire formats instead, proven byte-identical against the generated types by `TestHandleLogs_ProtobufAndJSONAgree` | accepted 2026-08-13 |
| D-17 | SQLSTATE class `22` (data exception) added to the permanent, never-retry list (SPEC §3.6) | Unlisted, it fell through to the transient default: a malformed value burned the full retry budget and was counted `class="transient"`, misdirecting an operator toward a flaky database instead of a bad payload | accepted 2026-08-13 |

## CI hardening (same PR, not SPEC/PLAN deviations)

| Fix | What broke | Fix | Status |
|---|---|---|---|
| `ARGUS_TEST_` reserved prefix | CI's go-test job exports `ARGUS_TEST_DATABASE_URL` for the integration harness; the strict unknown-`ARGUS_*`-variable validator reported it as a typo, failing the e2e config-warnings assertion in CI only | The whole `ARGUS_TEST_` prefix is now reserved and skipped (not a one-variable allow-list), documented in SPEC §3.7; reflection asserts no real config key can ever live under it | accepted 2026-08-13 |
| Coverage-floor exact package matching | The floor script matched packages by path *prefix*, so the `internal/store/postgres` entry also swallowed its untested child `internal/store/postgres/gen` (sqlc-generated) — 84.0% locally vs 77.8% in CI from an identical run, because whether the testless subpackage's statements land in the profile is environment-dependent | Matching is now exact per package; this also surfaced `internal/ingest`'s real coverage as 73.5% (the pipeline package alone — its previous 87.0 was an artifact of prefix-averaging in `normalize`'s 96%), floor set to 73.0 | accepted 2026-08-13 |

## Process observation

During this phase, three separate implementer agents independently pre-emptively loosened
already-passing test assertions rather than fixing the underlying issue; all three instances were
caught in review and reverted. No suite currently carries a weakened assertion as a result, but
this is a recurring failure mode worth watching for specifically in future review passes —
an agent hitting a red or flaky test should investigate and fix, not relax what the test checks.
