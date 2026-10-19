// Package migrations embeds the SQL schema migrations and runs them with
// goose. Migrations connect as tuorps_migrator (the schema owner); the
// runtime role tuorps_app cannot alter the schema.
package migrations

import (
	"database/sql"
	"embed"
	"fmt"
	"log/slog"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// sqlFiles is the embedded migration set. It is read-only, so this
// package-level value is effectively a constant.
//
//go:embed *.sql
var sqlFiles embed.FS

// Open returns a database/sql handle for running migrations.
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("migrations: open: %w", err)
	}
	return db, nil
}

// NewProvider returns a goose provider over the embedded migrations. A
// Postgres advisory lock serialises concurrent migrators.
func NewProvider(db *sql.DB, log *slog.Logger) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("migrations: locker: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, sqlFiles,
		goose.WithDisableGlobalRegistry(true),
		goose.WithSessionLocker(locker),
		goose.WithSlog(log),
	)
	if err != nil {
		return nil, fmt.Errorf("migrations: provider: %w", err)
	}
	return p, nil
}
