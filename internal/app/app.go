// Package app is the composition root shared by the api and worker
// binaries: it opens infrastructure and assembles the standard HTTP stack.
package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/yahya31415/tuorps/internal/health"
	"github.com/yahya31415/tuorps/internal/health/adapters"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/redisx"
)

// Deps are the process-wide infrastructure handles.
type Deps struct {
	Pool  *pgxpool.Pool
	Redis *redis.Client
}

// Open connects to Postgres and Redis, failing fast if either is unreachable.
func Open(ctx context.Context, cfg config.Config) (*Deps, error) {
	pool, err := postgres.NewPool(ctx, cfg.DB)
	if err != nil {
		return nil, err
	}
	rdb, err := redisx.NewClient(ctx, cfg.Redis)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Deps{Pool: pool, Redis: rdb}, nil
}

// Close releases every handle.
func (d *Deps) Close() error {
	d.Pool.Close()
	return errors.Join(d.Redis.Close())
}

// Checks are the readiness checks for these dependencies.
func (d *Deps) Checks() []health.NamedCheck {
	return []health.NamedCheck{
		{Name: "postgres", Critical: true, Run: adapters.PostgresCheck(d.Pool)},
		{Name: "redis", Critical: true, Run: adapters.RedisCheck(d.Redis)},
	}
}

// Middleware is the standard chain, outermost first: request ID, access
// log, panic recovery, security headers, body limit.
func Middleware(cfg config.Config, log *slog.Logger) []httpx.Middleware {
	return []httpx.Middleware{
		httpx.RequestID(ids.System()),
		httpx.AccessLog(log, clock.System()),
		httpx.Recover(log),
		httpx.SecurityHeaders(cfg.Env == config.EnvProd),
		httpx.BodyLimit(cfg.HTTP.MaxBodyBytes),
	}
}
