// Package db embeds Argus's SQL migrations so the binary carries its own
// schema and never depends on files present on the deploy target (SPEC
// §3.1, §3.8). go:embed cannot traverse into a parent directory from
// internal/store/postgres, so this thin package lives next to
// db/migrations and internal/store/postgres/migrations_embed.go re-exports
// its embed.FS.
package db

import "embed"

// MigrationsFS holds every server/db/migrations/*.sql file, embedded at
// build time.
//
//go:embed migrations/*.sql
var MigrationsFS embed.FS

// PricesFS holds the seeded server/db/prices/*.json price table(s),
// embedded at build time so `argusd prices import` (SPEC §3.8) never
// depends on a file being present on the deploy target, matching
// MigrationsFS's rationale above.
//
//go:embed prices/*.json
var PricesFS embed.FS
