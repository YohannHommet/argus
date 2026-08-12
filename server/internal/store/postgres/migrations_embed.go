package postgres

import (
	"io/fs"

	argusdb "github.com/YohannHommet/argus/server/db"
)

// migrationsFS is the embedded db/migrations/*.sql tree (SPEC §3.8). It is
// re-exported here, scoped to the migrations subdirectory, rather than
// embedded directly: go:embed cannot reach outside its own package
// directory, and db/migrations lives two levels up from
// internal/store/postgres.
var migrationsFS = mustSub(argusdb.MigrationsFS, "migrations")

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic("postgres: embedded migrations: " + err.Error())
	}
	return sub
}
