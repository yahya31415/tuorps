package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

const (
	txWS   = "0199c3f0-0000-7000-8000-00000000000a"
	txUser = "0199c3f0-0000-7000-8000-0000000000a1"
)

func appPool(t *testing.T, pg *testinfra.Postgres, maxConns int32) *pgxpool.Pool {
	t.Helper()
	pool, err := postgres.NewPool(t.Context(), config.DB{URL: config.NewSecret(pg.AppURL), MaxConns: maxConns, MinConns: 0, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func scope() postgres.Scope {
	return postgres.Scope{WorkspaceID: uuid.MustParse(txWS), UserID: uuid.MustParse(txUser)}
}

func guc(ctx context.Context, tx pgx.Tx) (ws, user *string, err error) {
	err = tx.QueryRow(ctx, "SELECT current_workspace_id()::text, current_user_id()::text").Scan(&ws, &user)
	return ws, user, err
}

func TestTenantTransactions(t *testing.T) {
	pg := testinfra.MigratedPostgres(t)
	// One connection, so any GUC leak would show up in the next transaction.
	pool := appPool(t, pg, 1)

	t.Run("InTx sets both GUCs for the transaction", func(t *testing.T) {
		err := postgres.InTx(t.Context(), pool, scope(), func(tx pgx.Tx) error {
			ws, user, err := guc(t.Context(), tx)
			if err != nil || ws == nil || *ws != txWS || user == nil || *user != txUser {
				t.Errorf("GUCs = %v %v, %v", ws, user, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("GUCs do not leak to the next transaction on the same connection", func(t *testing.T) {
		err := postgres.InTx(t.Context(), pool, postgres.Scope{}, func(tx pgx.Tx) error {
			ws, user, err := guc(t.Context(), tx)
			if err != nil || ws != nil || user != nil {
				t.Errorf("leaked GUCs = %v %v, %v", ws, user, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a partial scope leaves the other GUC unset", func(t *testing.T) {
		_ = postgres.InTx(t.Context(), pool, postgres.Scope{UserID: uuid.MustParse(txUser)}, func(tx pgx.Tx) error {
			ws, user, _ := guc(t.Context(), tx)
			if ws != nil || user == nil {
				t.Errorf("GUCs = %v %v", ws, user)
			}
			return nil
		})
	})

	t.Run("an error from fn rolls back and is returned", func(t *testing.T) {
		boom := errors.New("boom")
		err := postgres.InTx(t.Context(), pool, postgres.Scope{}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), "CREATE TEMP TABLE probe (x int)"); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
		_ = postgres.InTx(t.Context(), pool, postgres.Scope{}, func(tx pgx.Tx) error {
			var n int
			_ = tx.QueryRow(t.Context(), "SELECT count(*) FROM pg_tables WHERE tablename = 'probe'").Scan(&n)
			if n != 0 {
				t.Error("work from the failed transaction was committed")
			}
			return nil
		})
	})

	t.Run("InReadTx refuses writes", func(t *testing.T) {
		err := postgres.InReadTx(t.Context(), pool, scope(), func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), "CREATE TEMP TABLE nope (x int)")
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "25006" { // read_only_sql_transaction
			t.Fatalf("err = %v, want read-only violation", err)
		}
	})

	t.Run("a cancelled context aborts before running fn", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		ran := false
		err := postgres.InTx(ctx, pool, scope(), func(pgx.Tx) error { ran = true; return nil })
		if err == nil || ran {
			t.Fatalf("err = %v, ran = %v", err, ran)
		}
	})
}
