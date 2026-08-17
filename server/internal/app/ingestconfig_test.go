package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/config"
)

// TestIngestPipelineConfig_MapsEveryConfigKey guards the config -> pipeline
// seam. Both sides of ARGUS_INGEST_WRITE_TIMEOUT were individually tested
// (config_test's TestLoadIngestWriteTimeout parses it; internal/ingest's
// TestRetry_WriteTimeoutBoundsEachAttempt proves the pipeline honours it)
// while the single assignment joining them was covered by nothing — and an
// omitted assignment here does not fail, it silently substitutes the
// pipeline's own default, so the server would quietly ignore the operator's
// configured value. That is the same shape as the two integration defects
// Phase 3 shipped.
//
// The distinct non-default values matter: mapping a field to the wrong
// source key would still produce a fully-populated struct, so the assertion
// is per-field equality, not merely non-zeroness.
func TestIngestPipelineConfig_MapsEveryConfigKey(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		IngestQueue:          11,
		IngestWorkers:        22,
		IngestBatchSize:      33,
		IngestFlush:          44 * time.Millisecond,
		IngestRetryConflict:  55,
		IngestRetryTransient: 66,
		IngestWriteTimeout:   77 * time.Second,
	}

	got := ingestPipelineConfig(cfg)

	require.Equal(t, 11, got.QueueCap)
	require.Equal(t, 22, got.Workers)
	require.Equal(t, 33, got.BatchSize)
	require.Equal(t, 44*time.Millisecond, got.FlushInterval)
	require.Equal(t, 55, got.RetryConflict)
	require.Equal(t, 66, got.RetryTransient)
	require.Equal(t, 77*time.Second, got.WriteTimeout,
		"ARGUS_INGEST_WRITE_TIMEOUT must reach the pipeline; unmapped, it silently falls back to the pipeline's 30s default")

	// A field added to PipelineConfig without a line above would be mapped
	// from nothing and default silently, which is precisely the failure this
	// test exists to catch — so fail on the shape change itself rather than
	// waiting for someone to notice the missing assertion.
	require.Zero(t, reflect.ValueOf(got).NumField()-7,
		"ingest.PipelineConfig gained or lost a field: map it in ingestPipelineConfig and assert it here")
}
