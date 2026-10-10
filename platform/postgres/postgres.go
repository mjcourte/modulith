// Package postgres opens the shared PostgreSQL connection pool with the
// settings every Gyoza service relies on.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pool for url and checks the database is reachable.
// appName shows up in pg_stat_activity, so connections can be traced to the
// binary that opened them. The caller must Close the pool.
func Connect(ctx context.Context, url, appName string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	params := cfg.ConnConfig.RuntimeParams
	params["application_name"] = appName
	// pgx sends queries as prepared statements, and Postgres may switch them to
	// a cached generic plan that ignores the actual parameter values. For the
	// keyset-paginated List queries that turns an index scan that stops at the
	// page limit into reading and sorting the whole scope. Planning with the
	// real values each time costs microseconds. See
	// services/storagecatalogue/OPTIMIZATIONS.md.
	params["plan_cache_mode"] = "force_custom_plan"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	// NewWithConfig connects lazily; ping so a bad URL or a database that is
	// down fails at startup rather than on the first request.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
