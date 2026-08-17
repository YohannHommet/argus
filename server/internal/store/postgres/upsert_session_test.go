package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
	"github.com/YohannHommet/argus/server/internal/model"
)

// --- M3: sessions.entrypoint must be populated from the `app.entrypoint`
// attribute (falling back to `resource.app.entrypoint`), mirroring the
// adjacent app_version/resource.service.version block (SPEC §1.5.3: "hooks
// don't carry them"). Before the fix, upsertSessions looked up a bare
// "entrypoint" key that no producer (OTel or sim) ever emits, so the column
// was permanently NULL.

func TestWriteBatch_PopulatesSessionEntrypointFromAppEntrypointAttr(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-entrypoint-direct"
	ev := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base,
		withAttrs(map[string]any{"app.entrypoint": "cli"}))

	_, err := st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err)

	var entrypoint string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COALESCE(entrypoint, '') FROM sessions WHERE id = $1`, sessionID,
	).Scan(&entrypoint))
	require.Equal(t, "cli", entrypoint)
}

func TestWriteBatch_PopulatesSessionEntrypointFromResourceFallback(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 3, 1, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base)

	sessionID := "session-entrypoint-resource-fallback"
	ev := mkEvent(t, sessionID, model.KindLLMRequest, model.SourceOTelLog, base,
		withAttrs(map[string]any{"resource.app.entrypoint": "sdk-ts"}))

	_, err := st.WriteBatch(ctx, []model.Event{ev})
	require.NoError(t, err)

	var entrypoint string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COALESCE(entrypoint, '') FROM sessions WHERE id = $1`, sessionID,
	).Scan(&entrypoint))
	require.Equal(t, "sdk-ts", entrypoint)
}

// --- M5: one NUL byte in a vendor string attribute must never take out the
// whole WriteBatch. This is the lead's required repro spec: a batch of 500
// events — from 500 unrelated sessions, so a batch-wide drop would be
// unmistakable — where exactly ONE event carries a NUL byte in a string
// attribute, run through the real normalize -> WriteBatch path against the
// real Postgres. Before M5, the poisoned event's attrs map would make the
// `attrs jsonb` cast in the events INSERT raise SQLSTATE 22P05 (a permanent
// error, per retry.go's classification), which fails the single INSERT
// statement the whole batch shares — taking all 500 events down with it,
// even though a 2xx had already gone out to every contributing client.
// Sanitizing at normalize time (otlpattrs.go's sanitizeAttrString) removes
// the poison before it ever reaches the store, so this test's only way to
// pass is that the batch writes cleanly end to end — there is no per-event
// isolation to fall back on (that is explicitly a separate, larger ticket).
func TestWriteBatch_OneNULPoisonedEventDoesNotDropWholeBatch(t *testing.T) {
	st, pool := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	ensureRange(t, st, base, base.Add(2*time.Hour))

	const batchSize = 500
	const poisonedIndex = 250 // an arbitrary interior index, not first/last

	now := func() time.Time { return base.Add(time.Hour) }
	n := normalize.NewNormalizer(now, 30*24*time.Hour)

	var allEvents []model.Event
	for i := 0; i < batchSize; i++ {
		sessionID := fmt.Sprintf("session-nul-batch-%03d", i)
		outputValue := "clean tool output"
		if i == poisonedIndex {
			outputValue = "line one\x00line two"
		}

		data := &logspb.LogsData{
			ResourceLogs: []*logspb.ResourceLogs{{
				ScopeLogs: []*logspb.ScopeLogs{{
					LogRecords: []*logspb.LogRecord{{
						EventName: "tool_result",
						Attributes: []*commonpb.KeyValue{
							{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: sessionID}}},
							{Key: "tool.output", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: outputValue}}},
						},
					}},
				}},
			}},
		}

		events, rejections := n.FromOTLPLogs(data)
		require.Empty(t, rejections)
		require.Len(t, events, 1)
		events[0].ID = nextID() // ID assignment is the ingest pipeline's job, not normalize's (see other call sites' comments)
		allEvents = append(allEvents, events[0])
	}
	require.Len(t, allEvents, batchSize)

	result, err := st.WriteBatch(ctx, allEvents)
	require.NoError(t, err)
	require.Equal(t, batchSize, result.Written, "the NUL byte in one event must not drop the rest of the batch")
	require.Equal(t, 0, result.Deduped)

	var count int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE event_name = 'tool_result'`).Scan(&count))
	require.Equal(t, int64(batchSize), count)

	poisonedSessionID := fmt.Sprintf("session-nul-batch-%03d", poisonedIndex)
	var storedOutput string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT attrs->>'tool.output' FROM events WHERE session_id = $1`, poisonedSessionID,
	).Scan(&storedOutput))
	require.NotContains(t, storedOutput, "\x00")
	require.Equal(t, "line one�line two", storedOutput)

	// A sibling event that never carried any poison must be entirely
	// unaffected — proves this isn't "everyone gets sanitized/mangled",
	// only the one value that actually needed it.
	cleanSessionID := fmt.Sprintf("session-nul-batch-%03d", 0)
	var cleanOutput string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT attrs->>'tool.output' FROM events WHERE session_id = $1`, cleanSessionID,
	).Scan(&cleanOutput))
	require.Equal(t, "clean tool output", cleanOutput)
}
