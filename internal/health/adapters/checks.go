// Package adapters implements health.Check against real dependencies.
package adapters

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/yahya31415/tuorps/internal/health"
)

// PostgresCheck runs a trivial query, proving a pooled connection works end to end.
func PostgresCheck(pool *pgxpool.Pool) health.Check {
	return func(ctx context.Context) error {
		var one int
		return pool.QueryRow(ctx, "SELECT 1").Scan(&one)
	}
}

// RedisCheck sends PING.
func RedisCheck(c *redis.Client) health.Check {
	return func(ctx context.Context) error { return c.Ping(ctx).Err() }
}
