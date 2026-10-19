package audit_test

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/ids/idstest"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

const (
	wsA = "0199c3f0-0000-7000-8000-00000000000a"
	wsB = "0199c3f0-0000-7000-8000-00000000000b"
	uA  = "0199c3f0-0000-7000-8000-0000000000a1"
	uB  = "0199c3f0-0000-7000-8000-0000000000b1"
)

func u(s string) uuid.UUID { return uuid.MustParse(s) }

// recID returns the n-th ascending record ID.
func recID(n int) uuid.UUID {
	return uuid.MustParse("0199c3f0-0000-7000-8000-" + leftPad(n))
}

func leftPad(n int) string {
	const digits = "0123456789"
	b := []byte("000000000000")
	for i := len(b) - 1; n > 0; i-- {
		b[i] = digits[n%10]
		n /= 10
	}
	return string(b)
}

func setup(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	pg := testinfra.MigratedPostgres(t)
	mk := func(url string, max int32) *pgxpool.Pool {
		p, err := postgres.NewPool(t.Context(), config.DB{URL: config.NewSecret(url), MaxConns: max, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	app, admin := mk(pg.AppURL, 1), mk(pg.AdminURL, 2)
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tuorps.users (id, email, email_normalized, display_name, created_at, updated_at) VALUES ($1, 'a@x.in', 'a@x.in', 'A', $3, $3), ($2, 'b@x.in', 'b@x.in', 'B', $3, $3)`, []any{uA, uB, now}},
		{`INSERT INTO tuorps.workspaces (id, name, created_by, created_at, updated_at) VALUES ($1, 'A', $3, $5, $5), ($2, 'B', $4, $5, $5)`, []any{wsA, wsB, uA, uB, now}},
	} {
		if _, err := admin.Exec(t.Context(), q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	return app, admin
}

func writer(t *testing.T, app *pgxpool.Pool, idsInOrder ...uuid.UUID) audit.Writer {
	t.Helper()
	return audit.NewWriter(app, idstest.Sequence(t, idsInOrder...), clock.Fixed(time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)))
}

func event(t *testing.T, action, target string) core.Event {
	t.Helper()
	ev, err := core.NewEvent(action, "brand", target, map[string]string{"name": "Diwali"})
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func insertBrand(ctx context.Context, tx audit.Tx, id, ws string) error {
	_, err := tx.Pgx().Exec(ctx, `INSERT INTO brands (id, workspace_id, name, created_at, updated_at) VALUES ($1, $2, $3, now(), now())`, id, ws, "brand-"+id)
	return err
}

func count(t *testing.T, admin *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(t.Context(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInWriteTx(t *testing.T) {
	app, admin := setup(t)
	scopeA := postgres.Scope{WorkspaceID: u(wsA), UserID: u(uA)}
	meta := core.Meta{RequestID: "req-1", IPHash: "iphash"}

	t.Run("commits the write and its audit record together", func(t *testing.T) {
		w := writer(t, app, recID(1))
		brand := "0199c3f0-0000-7000-8000-00000000ba01"
		err := w.InWriteTx(t.Context(), scopeA, event(t, "brand.create", brand), meta, func(tx audit.Tx) error {
			if tx.Scope() != scopeA {
				t.Errorf("Scope() = %+v", tx.Scope())
			}
			return insertBrand(t.Context(), tx, brand, wsA)
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := count(t, admin, `SELECT count(*) FROM tuorps.brands WHERE id = $1`, brand); n != 1 {
			t.Errorf("brand rows = %d", n)
		}
		var ws, actor, action, target, reqID, ipHash string
		var details map[string]string
		err = admin.QueryRow(t.Context(), `SELECT workspace_id::text, actor_user_id::text, action, target_id, request_id, ip_hash, details FROM tuorps.audit_log WHERE id = $1`, recID(1)).
			Scan(&ws, &actor, &action, &target, &reqID, &ipHash, &details)
		if err != nil || ws != wsA || actor != uA || action != "brand.create" || target != brand || reqID != "req-1" || ipHash != "iphash" ||
			!maps.Equal(details, map[string]string{"name": "Diwali"}) {
			t.Errorf("audit row = %s %s %s %s %s %s %v, %v", ws, actor, action, target, reqID, ipHash, details, err)
		}
	})

	t.Run("an error from fn leaves neither the write nor the audit record", func(t *testing.T) {
		w := writer(t, app, recID(2))
		brand := "0199c3f0-0000-7000-8000-00000000ba02"
		boom := errors.New("boom")
		err := w.InWriteTx(t.Context(), scopeA, event(t, "brand.create", brand), meta, func(tx audit.Tx) error {
			if err := insertBrand(t.Context(), tx, brand, wsA); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if count(t, admin, `SELECT count(*) FROM tuorps.brands WHERE id = $1`, brand) != 0 || count(t, admin, `SELECT count(*) FROM tuorps.audit_log WHERE id = $1`, recID(2)) != 0 {
			t.Error("partial commit")
		}
	})

	t.Run("an unrecordable event rolls the write back", func(t *testing.T) {
		w := writer(t, app, recID(3))
		ran := false
		err := w.InWriteTx(t.Context(), postgres.Scope{}, event(t, "brand.create", "x"), meta, func(tx audit.Tx) error {
			ran = true
			_, err := tx.Pgx().Exec(t.Context(), "CREATE TEMP TABLE should_vanish (x int)")
			return err
		})
		if !errors.Is(err, core.ErrInvalidRecord) {
			t.Fatalf("err = %v, want ErrInvalidRecord", err)
		}
		if ran {
			t.Error("fn ran although the event could not be recorded")
		}
	})

	t.Run("account-level events have no workspace", func(t *testing.T) {
		w := writer(t, app, recID(4))
		ev, _ := core.NewEvent("auth.password_change", "user", uA, nil)
		if err := w.InWriteTx(t.Context(), postgres.Scope{UserID: u(uA)}, ev, meta, func(audit.Tx) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if n := count(t, admin, `SELECT count(*) FROM tuorps.audit_log WHERE id = $1 AND workspace_id IS NULL AND actor_user_id = $2`, recID(4), uA); n != 1 {
			t.Errorf("account-level rows = %d", n)
		}
	})
}

func TestListKeysetPagesAndTenantIsolation(t *testing.T) {
	app, _ := setup(t)
	scopeA := postgres.Scope{WorkspaceID: u(wsA), UserID: u(uA)}
	scopeB := postgres.Scope{WorkspaceID: u(wsB), UserID: u(uB)}
	ids := []uuid.UUID{recID(11), recID(12), recID(13), recID(14), recID(15), recID(16), recID(17)}
	w := writer(t, app, ids...)
	for i := range 5 {
		if err := w.InWriteTx(t.Context(), scopeA, event(t, "brand.update", "b"), core.Meta{}, func(audit.Tx) error { return nil }); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := w.InWriteTx(t.Context(), scopeB, event(t, "brand.update", "b"), core.Meta{}, func(audit.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	pwd, _ := core.NewEvent("auth.password_change", "user", uA, nil)
	if err := w.InWriteTx(t.Context(), postgres.Scope{UserID: u(uA)}, pwd, core.Meta{}, func(audit.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}

	page := func(s postgres.Scope, before uuid.UUID, limit int32) []uuid.UUID {
		t.Helper()
		recs, err := audit.ListWorkspace(t.Context(), app, s, before, limit)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]uuid.UUID, len(recs))
		for i, r := range recs {
			out[i] = r.ID
		}
		return out
	}
	first := page(scopeA, uuid.Nil(), 2)
	if len(first) != 2 || first[0] != recID(15) || first[1] != recID(14) {
		t.Fatalf("first page = %v", first)
	}
	second := page(scopeA, first[1], 2)
	if len(second) != 2 || second[0] != recID(13) || second[1] != recID(12) {
		t.Fatalf("second page = %v", second)
	}
	if last := page(scopeA, second[1], 2); len(last) != 1 || last[0] != recID(11) {
		t.Fatalf("last page = %v", last)
	}
	if b := page(scopeB, uuid.Nil(), 50); len(b) != 1 || b[0] != recID(16) {
		t.Fatalf("workspace B sees %v", b)
	}
	acct, err := audit.ListAccount(t.Context(), app, postgres.Scope{UserID: u(uA)}, uuid.Nil(), 50)
	if err != nil || len(acct) != 1 || acct[0].ID != recID(17) || acct[0].Action != "auth.password_change" {
		t.Fatalf("account events = %+v, %v", acct, err)
	}
	if other, _ := audit.ListAccount(t.Context(), app, postgres.Scope{UserID: u(uB)}, uuid.Nil(), 50); len(other) != 0 {
		t.Errorf("another user sees %d account events", len(other))
	}

	for _, bad := range []int32{0, -1, 101} {
		if _, err := audit.ListWorkspace(t.Context(), app, scopeA, uuid.Nil(), bad); !errors.Is(err, audit.ErrPageSize) {
			t.Errorf("limit %d: err = %v", bad, err)
		}
	}
}

func TestTxIsOnlyObtainableFromInWriteTx(t *testing.T) {
	var zero audit.Tx
	if zero.Pgx() != nil {
		t.Fatal("zero Tx exposes a transaction")
	}
	var _ func(audit.Tx) error = func(tx audit.Tx) error { _ = pgx.Tx(tx.Pgx()); return nil }
}

func TestInWriteTxResolving(t *testing.T) {
	app, admin := setup(t)
	pwdEvent := func(s postgres.Scope) (core.Event, error) {
		return core.NewEvent("auth.password_reset", "user", s.UserID.String(), nil)
	}

	t.Run("scope resolved inside the transaction governs the write and the record", func(t *testing.T) {
		w := writer(t, app, recID(21))
		var seen postgres.Scope
		err := w.InWriteTxResolving(t.Context(),
			func(tx pgx.Tx) (postgres.Scope, error) {
				var id uuid.UUID // stands in for redeeming a one-time token
				if err := tx.QueryRow(t.Context(), "SELECT $1::uuid", uA).Scan(&id); err != nil {
					return postgres.Scope{}, err
				}
				return postgres.Scope{UserID: id}, nil
			},
			pwdEvent, core.Meta{RequestID: "r"},
			func(tx audit.Tx) error {
				seen = tx.Scope()
				var user *string
				return tx.Pgx().QueryRow(t.Context(), "SELECT current_user_id()::text").Scan(&user)
			})
		if err != nil {
			t.Fatal(err)
		}
		if seen.UserID != u(uA) {
			t.Errorf("fn saw scope %+v", seen)
		}
		if n := count(t, admin, `SELECT count(*) FROM tuorps.audit_log WHERE id = $1 AND actor_user_id = $2 AND target_id = $2::text`, recID(21), uA); n != 1 {
			t.Errorf("audit rows = %d", n)
		}
	})

	t.Run("a failed resolution writes nothing", func(t *testing.T) {
		w := writer(t, app, recID(22))
		bad := errors.New("token not found")
		ran := false
		err := w.InWriteTxResolving(t.Context(),
			func(pgx.Tx) (postgres.Scope, error) { return postgres.Scope{}, bad },
			pwdEvent, core.Meta{}, func(audit.Tx) error { ran = true; return nil })
		if !errors.Is(err, bad) || ran {
			t.Fatalf("err = %v, ran = %v", err, ran)
		}
	})

	t.Run("work done during resolution rolls back if the action fails", func(t *testing.T) {
		w := writer(t, app, recID(23))
		boom := errors.New("boom")
		err := w.InWriteTxResolving(t.Context(),
			func(tx pgx.Tx) (postgres.Scope, error) {
				_, err := tx.Exec(t.Context(), "CREATE TEMP TABLE resolved_marker (x int)")
				return postgres.Scope{UserID: u(uA)}, err
			},
			pwdEvent, core.Meta{}, func(audit.Tx) error { return boom })
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if n := count(t, admin, `SELECT count(*) FROM tuorps.audit_log WHERE id = $1`, recID(23)); n != 0 {
			t.Errorf("audit row written for a failed action")
		}
	})
}
