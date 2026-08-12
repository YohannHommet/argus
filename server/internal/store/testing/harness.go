// Package testing is the store integration-test harness (SPEC §8.4): it
// hands each test its own Postgres schema, migrated and ready, whether
// backed by an externally-provided database or a testcontainer this
// package starts itself.
package testing

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	storepg "github.com/YohannHommet/argus/server/internal/store/postgres"
)

// envTestDatabaseURL is checked before falling back to testcontainers, per
// SPEC §8.4.
const envTestDatabaseURL = "ARGUS_TEST_DATABASE_URL"

var (
	containerOnce sync.Once
	containerDSN  string
	containerErr  error

	schemaSeq int64
)

// NewPool returns a *pgxpool.Pool scoped to a freshly created, migrated
// Postgres schema, unique to this call. Two calls — even from two tests
// running in parallel against the same underlying database — get
// non-colliding schemas (SPEC §8.4's per-test-schema convention).
//
// It skips the test (t.Skip), rather than failing it, when neither
// ARGUS_TEST_DATABASE_URL nor a usable Docker daemon is available, so
// `go test ./...` stays green without either.
func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	base := baseDSN(t)
	schema := nextSchemaName()

	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("store/testing: connecting to set up schema %s: %v", schema, err)
	}
	defer admin.Close()

	if _, execErr := admin.Exec(ctx, "CREATE SCHEMA "+schema); execErr != nil {
		t.Fatalf("store/testing: creating schema %s: %v", schema, execErr)
	}
	t.Cleanup(func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		dropper, dropErr := pgxpool.New(dropCtx, base)
		if dropErr != nil {
			return
		}
		defer dropper.Close()
		_, _ = dropper.Exec(dropCtx, "DROP SCHEMA "+schema+" CASCADE")
	})

	scopedDSN := withSearchPath(base, schema)
	pool, err := storepg.NewPool(ctx, scopedDSN, 5)
	if err != nil {
		t.Fatalf("store/testing: opening pool for schema %s: %v", schema, err)
	}
	t.Cleanup(pool.Close)

	if err := storepg.New(pool).Migrate(ctx); err != nil {
		t.Fatalf("store/testing: migrating schema %s: %v", schema, err)
	}

	return pool
}

// nextSchemaName generates a Postgres-identifier-safe, process-unique
// schema name. It never needs quoting: only [a-z0-9_].
func nextSchemaName() string {
	n := atomic.AddInt64(&schemaSeq, 1)
	return fmt.Sprintf("test_%d_%d", time.Now().UnixNano(), n)
}

// withSearchPath appends a search_path query parameter to a postgres URL
// DSN. pgx treats unrecognized connection-string parameters as runtime
// parameters sent at startup, which Postgres accepts for ordinary GUCs
// such as search_path — so every connection this pool opens lands in the
// given schema without any per-query SET.
func withSearchPath(dsn, schema string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "search_path=" + schema
}

// baseDSN resolves the database this test run should use: the operator-
// provided ARGUS_TEST_DATABASE_URL if set, else a shared postgres:18-alpine
// testcontainer started once per test binary. Skips the calling test if
// neither is available.
func baseDSN(t *testing.T) string {
	t.Helper()

	if dsn := os.Getenv(envTestDatabaseURL); dsn != "" {
		return dsn
	}

	containerOnce.Do(func() {
		containerDSN, containerErr = startContainer()
	})
	if containerErr != nil {
		t.Skipf("store/testing: skipping integration test: neither %s is set nor a Postgres testcontainer could be started: %v", envTestDatabaseURL, containerErr)
	}
	return containerDSN
}

// startContainer boots a postgres:18-alpine container (SPEC §2's target
// version, verified to run 18.4) for the lifetime of the test binary.
func startContainer() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	container, err := postgres.Run(ctx, "postgres:18-alpine",
		postgres.WithDatabase("argus"),
		postgres.WithUsername("argus"),
		postgres.WithPassword("argus"),
		// Run's default wait strategy only waits for the container to
		// start, not for Postgres to actually accept connections (it
		// restarts once mid-initdb). BasicWaitStrategies waits for the
		// "ready to accept connections" log line twice, then the port.
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return "", fmt.Errorf("starting postgres container: %w", err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return "", fmt.Errorf("reading container connection string: %w", err)
	}
	return dsn, nil
}
