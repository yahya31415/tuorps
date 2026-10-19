// Package redisx builds the go-redis client used for sessions, rate limits,
// caches and collision detection.
package redisx

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/yahya31415/tuorps/internal/platform/config"
)

// ErrInvalidURL reports an unparseable REDIS_URL without echoing it.
var ErrInvalidURL = errors.New("redisx: invalid redis URL")

// NewClient parses cfg, applies pool size and timeouts (honouring context
// deadlines), and pings so misconfiguration fails at startup.
func NewClient(ctx context.Context, cfg config.Redis) (*redis.Client, error) {
	opts, err := redis.ParseURL(cfg.URL.Reveal())
	if err != nil {
		return nil, ErrInvalidURL
	}
	opts.PoolSize = cfg.PoolSize
	opts.DialTimeout = cfg.DialTimeout
	opts.ReadTimeout = cfg.ReadTimeout
	opts.WriteTimeout = cfg.WriteTimeout
	opts.ContextTimeoutEnabled = true

	c := redis.NewClient(opts)
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("redisx: ping: %w", err)
	}
	return c, nil
}
