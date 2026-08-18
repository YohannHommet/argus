# Phase 5 accepted deviations and findings

Live view: `internal/stream` hub, SSE endpoints, `liveStore`, `LiveView`, live mode in the explorer,
plus ticket 0 (D-30). Entries marked **RULING NEEDED** were not ratified when this file was written;
the rest are lead calls recorded for completeness.

Every measured number below comes from a live `docker compose` stack driven by
`argusd sim --mode=load`, not from a fixture.

---

## Deviations needing an owner ruling

| ID | Deviation | Evidence | Recommendation |
|---|---|---|---|
| D-31 | **`StreamSessionFrame` was widened from SPEC §5.1's four illustrative fields to the whole `SessionSummary`.** SPEC §5.1 shows `{"id","status","turn_count","cost"}`; the server sends the full projection row. | `server/api/openapi.yaml`'s `StreamSessionFrame` is now `allOf: [SessionSummary]`. The four documented fields are all present, so this is a superset and no client breaks. The subset is not implementable as specified: `SessionTable` rows and the KPI strip both re-render from this frame (P5-06), and the hub's own `Filter.MatchSession` filters on `project`/`vendor`, neither of which the subset carries. | Ratify the widening and treat SPEC §5.1's example as illustrative, or add the missing fields to SPEC §5.1's snippet. Do not narrow the frame back — a four-field frame cannot drive the two consumers Phase 5 was asked to build. |
| D-32 | **`stats.ingest_lag_ms` cannot express "not measured yet", so `0` carries two meanings.** SPEC §4.1 forbids a zero standing in for an unknown, but `openapi.yaml` types the field as a required non-nullable integer. | Measured live: with the sim POSTing immediately, mean lag over a 2 s window is sub-millisecond and rounds to `0` — a real measurement that is indistinguishable on the wire from "no event has been observed yet". `HealthStrip` renders `0` as `—`, which is right for the second meaning and wrong for the first. | Make the field nullable, or give it sub-millisecond resolution (a float, or `ingest_lag_us`). Either is a schema change and was out of Phase 5's scope. Until then the UI errs toward `—`, i.e. toward admitting ignorance. |
| D-33 | **Sorting the session list by cost still ranks an all-estimated session at 0**, even though ticket 0 fixed the number the session *displays*. | `sessions.cost_usd` stays reported-only because it backs `sessions_cost_idx` and is exactly what SPEC §4.3's `sort=cost_usd` orders by. `buildSessionCost` sums reported + estimated only at the wire layer, so the displayed cost and the sort key legitimately disagree for a session Argus priced itself. | Add a stored, indexed total (or an expression index) and point `sort=cost_usd` at it — a SPEC §4.3 + §2.1 change. Recorded rather than silently left: the D-30 fix closes the honesty gap on the screen, not in the ordering. |

---

## Lead rulings made during the phase (no ruling expected)

- **D-30 accepted, fixed in ticket 0** (see `phase-4-gauntlet.md`): the sessions *and* turns
  projections now run SPEC §2.4's estimator rather than emitting a structural zero. Making `usd`
  nullable was rejected — the number is knowable, so refusing to compute it trades one dishonesty for
  another.
- **D-26 … D-29 accepted as shipped** (see `phase-4-deviations.md`). D-28's `dropped_total` now has a
  real backing field, so the data-quality dropped tile can be wired up in Phase 6.
- **`stats.dropped_total` is ingest drops only, not summed with the hub's.** An ingest drop is
  permanent — nothing was stored, so no reconnect can replay it — while a hub drop means the event is
  stored and one subscriber's buffer fell behind, which SPEC §5.1 already reports through its own
  per-subscriber `lag` frame and `/metrics` reports fleet-wide as `argus_stream_dropped_total`.
  Summing them would make a self-healing display-layer hiccup indistinguishable from permanent data
  loss in the one number a data-quality alert fires on. `HealthStrip` shows both as separate,
  separately-labelled tiles. Pinned by `TestStatsSnapshot_DroppedTotalCountsOnlyPermanentLoss`.
- **A `reset` frame clears the client's `sessions` map too.** SPEC §5.2 says the client "drops local
  state and refetches via REST"; a projection snapshot that survived an acknowledged gap would render
  as though it were current. The lifetime drop/malformed counters deliberately survive — they are the
  evidence the gap happened.
- **PLAN's `[P] P5-02, P5-03` was serialized.** Both tickets need `internal/app/{app,serve}.go`
  (P5-02 defines `Deps.Stream`/`Deps.Replay`, P5-03 wires them), so running them in parallel would
  have collided on the same files.
- **`P5-01a` was added to the ticket list**: `postgres.Store.EventsSince` was still a P1-04 stub
  returning `ErrNotImplemented`. PLAN lists P3-03 as P5-02's dependency on the assumption it had
  landed; it had not, and without it SSE replay could only ever work against the in-memory fake.
- **`EventsSince` is hand-written pgx, not sqlc**, unlike its `GetEventByRef` neighbour. Verified
  directly: sqlc v1.31.1 types the row comparison's second parameter (`seq`, a bigint) as the first
  element's `timestamptz`, so the generated binding is unusable. Documented at the query.
- **Two new methods on `postgres.Store` are absent from the `store.Store` seam**
  (`SessionSummary`, `ActiveSessionCount`), consumed through narrow consumer-owned ports — the
  precedent `MigrationsCurrent`/`QueueSaturated`/`ImportPrices` already set.
- **`AppShell` gained no tab-wide connection indicator**, though SPEC §6.3 lists one. A topbar
  indicator implies a tab-wide subscription, which would keep an `EventSource` open on every page and
  contradict the ref-counted design exit criterion 6 requires. Connection state lives in
  `HealthStrip` on `/live` instead.
- **`CostAttributionCard` marks the estimated total only when the reported split is empty.**
  `by_query_source` is reported-cost-only by SPEC §2.1, so a partly-estimated session's split is
  true-but-incomplete rather than a lie, and the KPI strip already marks the total as estimated.

---

## Integration defects found only by wiring the layers together

Each of these passed every unit test in its own package and would have shipped broken.

1. **chi's `Timeout` middleware would have killed every SSE connection at 30 s.**
   `chimw.Timeout` is installed on the root router and cancels `r.Context()` unconditionally; the SSE
   handler selects on `r.Context().Done()` as its teardown signal. Every millisecond-long unit test
   passed. chi fixes its middleware stack before any route is registered and the stream routes cannot
   leave the `/api/v1` subtree, so the exemption is per-request by path (`StreamAwareTimeout`), pinned
   by a test that drives it with a 1 ms bound and asserts both directions.
2. **`http.Server.WriteTimeout` (30 s) bounds the whole response.** `serve.go` carried a comment
   addressed to this ticket; the handler now refreshes a per-write deadline through
   `http.ResponseController` rather than the global bound being raised, so a stream can outlive 30 s
   while a genuinely dead TCP peer is still reaped.
3. **The naive shutdown order deadlocks until the grace expires.** `http.Server.Shutdown` waits for
   every active handler and an SSE handler never returns on its own, so shutting the server down
   before the hub would hold every stream open for the full `ARGUS_SHUTDOWN_GRACE` with the
   `event: shutdown` frame never sent — the exact failure SPEC §3.8 step 2 exists to prevent. The hub
   is shut down first.
4. **`loadEvent` refused every call made without a current session, which on `/live` is every call.**
   Clicking a live-feed row opened the detail sheet with the correct `event_ref` and permanently blank
   content. `GET /api/v1/events/{ref}` is addressed by `event_ref` alone (SPEC §4.1: "there is no
   lookup by id"); only the choice of cache slot ever needed a session. Verified fixed in a real
   browser against a live stack.
5. **`CostAttributionCard`'s D-30 props were never passed by its only caller**, so the server-side fix
   was inert on the screen the gauntlet photographed. Now wired in `SessionDetailView.vue` and pinned
   by a view-level test — a component-level test cannot catch an unwired prop.

---

## Latent test defect fixed in passing

`TestRetry_PermanentErrorNotRetried` and `TestRetry_TransientExhaustsThenDrops` waited on the
`WriteFailed` counter and then read `Dropped`, which `retryLoop` increments *afterwards* — so a loaded
machine can resume inside the window between the two and read `Dropped` as 0. Both now wait on both
counters. Reproduced under real CPU contention, not theorised.

---

## Verification notes for whoever reads this next

- **`rtk` truncates piped output at a 500-byte boundary.** `curl … | python3 -c 'json.load(…)'`
  failed three times with a parse error at char 500-521 while the very same request written to a file
  was a clean 6391 bytes that parsed on 5/5 attempts. This is the same family as the already-documented
  artifacts where `rtk` hid a `go test` build failure behind a green summary and dropped a merge commit
  from `git log`. **Write the body to a file before parsing it**, and check `$?` explicitly.
- **`sim --mode=load` timestamps events well into the future** (measured: `last_event_at` ~40 minutes
  ahead of wall clock), because a session's simulated event span is added to a start offset that
  itself advances in real time. Consequences for anyone verifying live behaviour: neither
  `status=active` nor `last_event_at` freshness identifies an in-flight session — `status` only decays
  after `ARGUS_SESSION_IDLE_TIMEOUT` (15 min), and the timestamps are future-dated. The only sound
  selector is **tallying `session_id`s off the firehose itself**. Two verification runs produced
  false negatives on exit criterion 2 before this was understood; it is not a product defect, but it
  is a trap worth one paragraph.
