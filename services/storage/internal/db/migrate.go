// Package db is storage's Postgres schema: its migrations, the queries of the
// ledger (ledger.sql) and the manager (manager.sql), and the code sqlc
// generates from them.
package db

//go:generate go tool sqlc generate

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// The migrations are compiled into the binary, so a deployed service
// needs no files alongside it.
//
//go:embed migrations/*.sql
var migrations embed.FS

// Migrate brings the storage schema up to date. It is safe to call on
// every startup, and from several replicas at once.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// goose keeps its version table inside the storage schema, so the
	// schema has to exist before goose runs.
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS storage`); err != nil {
		return fmt.Errorf("create storage schema: %w", err)
	}

	sqlDB := stdlib.OpenDBFromPool(pool) // goose needs database/sql; closing this leaves the pool open
	defer sqlDB.Close()

	fsys, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys,
		goose.WithTableName("storage.goose_db_version"),
		goose.WithSessionLocker(locker),
	)
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate storage: %w", err)
	}
	return nil
}
