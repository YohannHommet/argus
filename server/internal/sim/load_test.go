package sim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLoadMode_ThroughputWithinTolerance asserts load-mode AC: --rate=200
// --duration=10s reports throughput within 15% of target (measures rate
// control against httptest, not ingestion latency).
func TestLoadMode_ThroughputWithinTolerance(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping timing-sensitive load test")
	}

	var received atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const targetRate = 200.0 // events/s — the AC's rate, not a substituted one
	const duration = 10 * time.Second

	cfg := DefaultConfig()
	cfg.Mode = ModeLoad
	cfg.Rate = targetRate
	cfg.Concurrency = 4
	cfg.Duration = duration
	cfg.FlushImmediately = true

	transport := &HTTPTransport{Client: srv.Client(), Target: srv.URL}
	runner := NewRunner(cfg, transport)

	start := time.Now()
	err := runner.RunLoad(context.Background())
	elapsed := time.Since(start)
	require.NoError(t, err)

	runner.Report.Finish()
	measuredRate := float64(received.Load()) / elapsed.Seconds()

	t.Logf("target=%.1f events/s measured=%.1f events/s (elapsed=%s, requests=%d)", targetRate, measuredRate, elapsed, received.Load())

	tolerance := 0.15 // the AC's tolerance, verbatim
	lower := targetRate * (1 - tolerance)
	upper := targetRate * (1 + tolerance)
	require.GreaterOrEqualf(t, measuredRate, lower, "measured throughput %.1f events/s below tolerance floor %.1f (target %.1f)", measuredRate, lower, targetRate)
	require.LessOrEqualf(t, measuredRate, upper, "measured throughput %.1f events/s above tolerance ceiling %.1f (target %.1f)", measuredRate, upper, targetRate)
}
