package postgres

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Scope is the tenant context of a transaction. A zero field leaves the
// corresponding GUC unset, which row-level security treats as "no access".
type Scope struct {
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
}

// InTx runs fn in a read-write transaction scoped to s and commits if fn
// returns nil. The GUCs are transaction-local (set_config(..., true)), so
// they never leak to the next user of a pooled connection.
func InTx(ctx context.Context, pool *pgxpool.Pool, s Scope, fn func(pgx.Tx) error) error {
	return inTx(ctx, pool, pgx.TxOptions{}, s, fn)
}

// InReadTx is InTx in a READ ONLY transaction, for queries.
func InReadTx(ctx context.Context, pool *pgxpool.Pool, s Scope, fn func(pgx.Tx) error) error {
	return inTx(ctx, pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, s, fn)
}

func inTx(ctx context.Context, pool *pgxpool.Pool, opts pgx.TxOptions, s Scope, fn func(pgx.Tx) error) (err error) {
	tx, err := pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, ignoreClosed(tx.Rollback(context.WithoutCancel(ctx))))
		}
	}()
	if _, err = tx.Exec(ctx, "SELECT set_config('app.workspace_id', $1, true), set_config('app.user_id', $2, true)",
		gucValue(s.WorkspaceID), gucValue(s.UserID)); err != nil {
		return fmt.Errorf("postgres: set tenant scope: %w", err)
	}
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

func gucValue(id uuid.UUID) string {
	if id == uuid.Nil() {
		return ""
	}
	return id.String()
}

func ignoreClosed(err error) error {
	if errors.Is(err, pgx.ErrTxClosed) {
		return nil
	}
	return err
}
