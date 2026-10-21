// Package postgres opens the shared PostgreSQL connection pool with the
// settings every Modulith service relies on.
package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/exaring/otelpgx"
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
	// services/storage/OPTIMIZATIONS.md.
	params["plan_cache_mode"] = "force_custom_plan"
	// One span per query (a no-op until telemetry is set up). The SQL is kept
	// as an attribute with its $1 placeholders, never the values.
	cfg.ConnConfig.Tracer = otelpgx.NewTracer(otelpgx.WithSpanNameFunc(spanName))

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

// spanName names a query's span after sqlc's "-- name: GetShare :one"
// comment, or else its first word (SELECT, BEGIN, ...): the SQL itself is
// too long and varied to name spans by.
func spanName(sql string) string {
	sql = strings.TrimSpace(sql)
	if rest, ok := strings.CutPrefix(sql, "-- name: "); ok {
		if name, _, ok := strings.Cut(rest, " "); ok && name != "" {
			return name
		}
	}
	if fields := strings.Fields(sql); len(fields) > 0 {
		return strings.ToUpper(fields[0])
	}
	return "query"
}
