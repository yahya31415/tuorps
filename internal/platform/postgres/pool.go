// Package postgres builds the pgx connection pool used by every adapter.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya31415/tuorps/internal/platform/config"
)

// Server-side guards applied to every session, so a runaway query or a
// forgotten transaction cannot hold resources indefinitely.
const (
	statementTimeout       = "15s"
	idleInTxSessionTimeout = "30s"
	applicationName        = "tuorps"
)

// ErrInvalidURL reports an unparseable DATABASE_URL without echoing it.
var ErrInvalidURL = errors.New("postgres: invalid database URL")

// NewPool parses cfg, applies pool limits and session guards, and pings the
// server so misconfiguration fails at startup. Errors never include the URL.
func NewPool(ctx context.Context, cfg config.DB) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL.Reveal())
	if err != nil {
		return nil, ErrInvalidURL
	}
	pc.MaxConns = cfg.MaxConns
	pc.MinConns = cfg.MinConns
	pc.MaxConnLifetime = cfg.MaxConnLifetime
	pc.MaxConnIdleTime = cfg.MaxConnIdleTime
	pc.ConnConfig.RuntimeParams["statement_timeout"] = statementTimeout
	pc.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = idleInTxSessionTimeout
	pc.ConnConfig.RuntimeParams["application_name"] = applicationName

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}
