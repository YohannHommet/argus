package ingest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/YohannHommet/argus/server/internal/ingest"
)

func TestClassifyError_Table(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ingest.RetryClass
	}{
		{"nil", nil, ingest.ClassNone},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, ingest.ClassConflict},
		{"serialization_failure", &pgconn.PgError{Code: "40001"}, ingest.ClassConflict},
		{"connection_exception", &pgconn.PgError{Code: "08006"}, ingest.ClassTransient},
		{"connection_does_not_exist", &pgconn.PgError{Code: "08003"}, ingest.ClassTransient},
		{"admin_shutdown", &pgconn.PgError{Code: "57P01"}, ingest.ClassTransient},
		{"context_deadline_exceeded", context.DeadlineExceeded, ingest.ClassTransient},
		{"context_canceled", context.Canceled, ingest.ClassTransient},
		{"unique_violation", &pgconn.PgError{Code: "23505"}, ingest.ClassPermanent},
		{"foreign_key_violation", &pgconn.PgError{Code: "23503"}, ingest.ClassPermanent},
		{"syntax_error", &pgconn.PgError{Code: "42601"}, ingest.ClassPermanent},
		{"undefined_column", &pgconn.PgError{Code: "42703"}, ingest.ClassPermanent},
		{"unrecognized_generic_error", errors.New("boom"), ingest.ClassTransient},
		{"unrecognized_sqlstate_class", &pgconn.PgError{Code: "53300"}, ingest.ClassTransient}, // too_many_connections: not named by SPEC §3.6
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ingest.ClassifyError(tc.err))
		})
	}
}

func TestRetryClass_String(t *testing.T) {
	require.Equal(t, "none", ingest.ClassNone.String())
	require.Equal(t, "conflict", ingest.ClassConflict.String())
	require.Equal(t, "transient", ingest.ClassTransient.String())
	require.Equal(t, "permanent", ingest.ClassPermanent.String())
}
