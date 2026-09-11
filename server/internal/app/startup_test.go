package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWaitForDB_SucceedsAfterTransientFailures pins the D-34 repro: a ping
// that fails a few times (the Postgres first-init restart beat) then
// succeeds must not be treated as fatal — waitForDB must retry and return
// nil once the fake ping starts succeeding, having actually retried rather
// than returning after one lucky call.
func TestWaitForDB_SucceedsAfterTransientFailures(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const failCount = 3
	calls := 0
	ping := func(context.Context) error {
		calls++
		if calls <= failCount {
			return errors.New("connect: connection refused")
		}
		return nil
	}

	err := waitForDB(context.Background(), ping, time.Second, time.Millisecond, logger)

	require.NoError(t, err)
	require.Equal(t, failCount+1, calls, "waitForDB must have actually retried, not just called ping once")
}

// TestWaitForDB_BudgetExhaustionReturnsWrappedLastError pins the "genuinely
// down DB still fails" half of D-34's fix: a ping that never succeeds must
// make waitForDB give up once maxWait elapses, wrapping the last ping error
// rather than hanging forever or swallowing it.
func TestWaitForDB_BudgetExhaustionReturnsWrappedLastError(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sentinel := errors.New("connect: connection refused")
	calls := 0
	ping := func(context.Context) error {
		calls++
		return sentinel
	}

	const maxWait = 20 * time.Millisecond
	const interval = 5 * time.Millisecond

	start := time.Now()
	err := waitForDB(context.Background(), ping, maxWait, interval, logger)
	elapsed := time.Since(start)

	require.Error(t, err)
	require.ErrorIs(t, err, sentinel, "the budget-exhaustion error must wrap the last ping failure")
	require.Greater(t, calls, 1, "must have retried at least once within the budget")
	require.GreaterOrEqual(t, elapsed, maxWait, "must not give up before the budget elapses")
	require.Less(t, elapsed, maxWait+500*time.Millisecond, "must not overrun the budget by more than test scheduling slack")
}

// TestWaitForDB_ContextCancellationAbortsPromptly pins the Ctrl+C/SIGTERM
// case: a context cancelled mid-wait must make waitForDB return promptly
// with the context error wrapped, without exhausting the full budget — the
// fake ping cancels the context itself so this needs no wall-clock sleep to
// synchronize.
func TestWaitForDB_ContextCancellationAbortsPromptly(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	ping := func(context.Context) error {
		calls++
		if calls == 2 {
			cancel()
		}
		return errors.New("connect: connection refused")
	}

	const maxWait = 10 * time.Second // large: proves the abort is cancellation, not budget exhaustion
	const interval = time.Millisecond

	start := time.Now()
	err := waitForDB(ctx, ping, maxWait, interval, logger)
	elapsed := time.Since(start)

	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, elapsed, maxWait, "cancellation must abort well before the budget elapses")
}

// TestWaitForDB_TableDriven exercises the three scenarios above together as
// a table, asserting only the pass/fail shape — the dedicated tests above
// cover the finer-grained assertions (call counts, wrapped errors, timing).
func TestWaitForDB_TableDriven(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sentinel := errors.New("connect: connection refused")

	tests := []struct {
		name    string
		ping    func() func(context.Context) error
		ctx     func() (context.Context, context.CancelFunc)
		wantErr bool
	}{
		{
			name: "recovers after transient failures",
			ping: func() func(context.Context) error {
				calls := 0
				return func(context.Context) error {
					calls++
					if calls <= 2 {
						return sentinel
					}
					return nil
				}
			},
			ctx:     func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: false,
		},
		{
			name: "always failing exhausts the budget",
			ping: func() func(context.Context) error {
				return func(context.Context) error { return sentinel }
			},
			ctx:     func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := tt.ctx()
			defer cancel()

			err := waitForDB(ctx, tt.ping(), 30*time.Millisecond, time.Millisecond, logger)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
