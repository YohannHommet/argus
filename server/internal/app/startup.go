package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// dbConnectMaxWait bounds how long New waits for the DB to accept TCP after NewPool succeeds — Postgres's first-init restart briefly refuses connections despite passing its healthcheck (D-34).
const dbConnectMaxWait = 15 * time.Second

// dbConnectRetryInterval is waitForDB's fixed poll cadence within dbConnectMaxWait (D-34).
const dbConnectRetryInterval = 1 * time.Second

// waitForDB polls ping until it succeeds, ctx is cancelled, or maxWait
// elapses — absorbing the D-34 restart beat instead of letting the first
// real connection attempt (Store.Migrate's pool.Acquire) fail the whole
// startup. ping is a parameter rather than a *pgxpool.Pool so this is
// testable without a live database.
func waitForDB(ctx context.Context, ping func(context.Context) error, maxWait, interval time.Duration, logger *slog.Logger) error {
	deadline := time.Now().Add(maxWait)
	attempt := 0
	var lastErr error

	for {
		attempt++
		lastErr = ping(ctx)
		if lastErr == nil {
			if attempt > 1 {
				logger.Info("app: database became reachable", "attempt", attempt)
			}
			return nil
		}
		logger.Warn("app: database not yet reachable", "attempt", attempt, "error", lastErr)

		if ctx.Err() != nil {
			return fmt.Errorf("app: waiting for database: %w", ctx.Err())
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("app: database not reachable after %s: %w", maxWait, lastErr)
		}

		wait := interval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("app: waiting for database: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
