package sim

import (
	"testing"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/ingest/normalize"
	"github.com/YohannHommet/argus/server/internal/model"
)

// TestGenerator_RoundTripsThroughNormalizers is the AC2 test: "the emitted
// payloads round-trip through the P2-02/P2-04 normalizers with zero
// `unknown` kinds and zero rejections (a unit test wiring generator ->
// normalizer, no server)". It calls generateSession directly (pure,
// doc.go's generator/transport split) and feeds the resulting protobuf
// messages and hook payloads straight into the real normalizers — no
// encoding, no HTTP, no file I/O, so this stays a cheap, deterministic unit
// test.
func TestGenerator_RoundTripsThroughNormalizers(t *testing.T) {
	t.Parallel()

	logNorm := normalize.NewNormalizer(time.Now, 400*24*time.Hour)
	hookNorm := normalize.NewHookNormalizer(time.Now, 400*24*time.Hour, false)

	cfg := DefaultConfig()
	clock := NewClock(FixedEpoch)

	// One session per fixed project (projects.go), including legacy-app,
	// so the metrics-only path is exercised too.
	for ordinal, project := range projects {
		t.Run(project, func(t *testing.T) {
			t.Parallel()
			result := generateSession(cfg, clock, ordinal, 0, project)
			require.NotEmpty(t, result.SessionID)

			if project == legacyAppProject {
				require.Empty(t, result.Logs, "legacy-app is metrics-only (SPEC §7.1)")
				require.Empty(t, result.Hooks, "legacy-app is metrics-only (SPEC §7.1)")
			} else {
				require.NotEmpty(t, result.Logs)
				require.NotEmpty(t, result.Hooks)
			}
			require.NotEmpty(t, result.Metrics)

			if len(result.Logs) > 0 {
				recs := make([]*logspb.LogRecord, len(result.Logs))
				for i, e := range result.Logs {
					recs[i] = e.Rec
				}
				data := wrapLogs(result.Identity, recs)
				events, rejections := logNorm.FromOTLPLogs(data)
				require.Empty(t, rejections, "project %s: unexpected log rejections", project)
				require.Len(t, events, len(result.Logs))
				for _, e := range events {
					require.NotEqual(t, model.KindUnknown, e.Kind, "unrecognized event_name %q produced KindUnknown", e.EventName)
				}
			}

			metrics := make([]*metricspb.Metric, len(result.Metrics))
			for i, e := range result.Metrics {
				metrics[i] = e.M
			}
			mdata := wrapMetrics(result.Identity, metrics)
			mSamples, mRejections := logNorm.FromOTLPMetrics(mdata)
			require.Empty(t, mRejections, "project %s: unexpected metric rejections", project)
			// Every generated metric is a Sum with exactly one data point
			// (otel_metric_events.go's newSumMetric), so sample count is
			// 1:1 with the emitted metric count.
			require.Len(t, mSamples, len(result.Metrics))

			if len(result.Hooks) > 0 {
				payloads := make([]map[string]any, len(result.Hooks))
				for i, e := range result.Hooks {
					payloads[i] = e.Payload
				}
				body, err := EncodeHookBatch(payloads)
				require.NoError(t, err)
				events, err := hookNorm.FromHookPayload(body)
				require.NoError(t, err)
				require.Len(t, events, len(result.Hooks))
				for _, e := range events {
					require.NotEqual(t, model.KindUnknown, e.Kind, "unrecognized hook_event_name %q produced KindUnknown", e.EventName)
				}
			}
		})
	}
}
