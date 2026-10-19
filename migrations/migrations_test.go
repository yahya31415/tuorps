package migrations_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yahya31415/tuorps/internal/platform/testinfra"
	"github.com/yahya31415/tuorps/migrations"
)

func open(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := migrations.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func up(t *testing.T, db *sql.DB) {
	t.Helper()
	p, err := migrations.NewProvider(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(t.Context()); err != nil {
		t.Fatalf("Up: %v", err)
	}
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func TestMigrationsAgainstPostgres(t *testing.T) {
	pg := testinfra.StartPostgres(t)
	migrator := open(t, pg.MigratorURL)
	app := open(t, pg.AppURL)

	t.Run("embedded sources are found", func(t *testing.T) {
		p, err := migrations.NewProvider(migrator, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		if len(p.ListSources()) == 0 {
			t.Fatal("no embedded migrations")
		}
	})

	t.Run("runtime role cannot migrate", func(t *testing.T) {
		p, err := migrations.NewProvider(app, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Up(t.Context()); err == nil {
			t.Fatal("tuorps_app was able to run migrations")
		}
	})

	t.Run("concurrent up applies once", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make([]error, 3)
		for i := range errs {
			wg.Go(func() {
				p, err := migrations.NewProvider(open(t, pg.MigratorURL), slog.New(slog.NewTextHandler(io.Discard, nil)))
				if err == nil {
					_, err = p.Up(context.Background())
				}
				errs[i] = err
			})
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatalf("concurrent Up: %v", err)
		}
	})

	t.Run("down to zero and up again", func(t *testing.T) {
		p, err := migrations.NewProvider(migrator, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.DownTo(t.Context(), 0); err != nil {
			t.Fatalf("DownTo(0): %v", err)
		}
		if v, _ := p.GetDBVersion(t.Context()); v != 0 {
			t.Fatalf("version after down = %d", v)
		}
		up(t, migrator)
		current, target, err := p.GetVersions(t.Context())
		if err != nil || current != target || current == 0 {
			t.Fatalf("versions current=%d target=%d err=%v", current, target, err)
		}
	})

	t.Run("runtime role cannot create objects", func(t *testing.T) {
		_, err := app.ExecContext(t.Context(), "CREATE TABLE sneaky (id int)")
		if sqlState(err) != "42501" { // insufficient_privilege
			t.Fatalf("CREATE TABLE as tuorps_app: %v (state %q), want permission denied", err, sqlState(err))
		}
		_, err = app.ExecContext(t.Context(), "CREATE TABLE public.sneaky (id int)")
		if sqlState(err) != "42501" {
			t.Fatalf("CREATE TABLE in public as tuorps_app: %v, want permission denied", err)
		}
	})

	t.Run("tenant context helper", func(t *testing.T) {
		ctx := t.Context()
		tx, err := app.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()

		var got sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT current_workspace_id()").Scan(&got); err != nil || got.Valid {
			t.Fatalf("unset tenant: %v %v, want NULL (fail closed)", got, err)
		}
		const ws = "0199c3f0-7a2b-7c3d-8e4f-5a6b7c8d9e0f"
		if _, err := tx.ExecContext(ctx, "SELECT set_config('app.workspace_id', $1, true)", ws); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRowContext(ctx, "SELECT current_workspace_id()").Scan(&got); err != nil || got.String != ws {
			t.Fatalf("set tenant: %v %v", got, err)
		}
		if _, err := tx.ExecContext(ctx, "SELECT set_config('app.workspace_id', 'not-a-uuid', true)"); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRowContext(ctx, "SELECT current_workspace_id()").Scan(&got); sqlState(err) != "22P02" {
			t.Fatalf("garbage tenant id: %v %v, want invalid_text_representation", got, err)
		}
	})
}
