package migrations_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

// Fixed IDs: two workspaces A and B. uA belongs to A only, uB to B only,
// uAB to both.
const (
	wsA   = "0199c3f0-0000-7000-8000-00000000000a"
	wsB   = "0199c3f0-0000-7000-8000-00000000000b"
	uA    = "0199c3f0-0000-7000-8000-0000000000a1"
	uB    = "0199c3f0-0000-7000-8000-0000000000b1"
	uAB   = "0199c3f0-0000-7000-8000-0000000000ab"
	brA   = "0199c3f0-0000-7000-8000-00000000ba01"
	brB   = "0199c3f0-0000-7000-8000-00000000bb01"
	invA  = "0199c3f0-0000-7000-8000-0000000001a1"
	invB  = "0199c3f0-0000-7000-8000-0000000001b1"
	tokA  = "0199c3f0-0000-7000-8000-0000000002a1"
	tokB  = "0199c3f0-0000-7000-8000-0000000002b1"
	audA  = "0199c3f0-0000-7000-8000-0000000003a1"
	audB  = "0199c3f0-0000-7000-8000-0000000003b1"
	audUA = "0199c3f0-0000-7000-8000-0000000003a2"
)

func hashOf(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

// seed inserts the fixture as the superuser (bypassing RLS).
func seed(t *testing.T, admin *sql.DB) {
	t.Helper()
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := admin.ExecContext(t.Context(), q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	for _, u := range []struct{ id, email string }{{uA, "a@example.in"}, {uB, "b@example.in"}, {uAB, "ab@example.in"}} {
		exec(`INSERT INTO tuorps.users (id, email, email_normalized, display_name, created_at, updated_at) VALUES ($1, $2, $2, 'User', $3, $3)`, u.id, u.email, now)
		exec(`INSERT INTO tuorps.user_credentials (user_id, password_hash, updated_at) VALUES ($1, $2, $3)`, u.id, "$argon2id$hash-of-"+u.id, now)
		exec(`INSERT INTO tuorps.recovery_codes (user_id, code_hash) VALUES ($1, $2)`, u.id, hashOf("rc-"+u.id))
	}
	for _, w := range []struct{ id, owner string }{{wsA, uA}, {wsB, uB}} {
		exec(`INSERT INTO tuorps.workspaces (id, name, created_by, created_at, updated_at) VALUES ($1, 'WS', $2, $3, $3)`, w.id, w.owner, now)
		exec(`INSERT INTO tuorps.workspace_members (workspace_id, user_id, is_owner, created_at) VALUES ($1, $2, true, $3)`, w.id, w.owner, now)
		exec(`INSERT INTO tuorps.workspace_members (workspace_id, user_id, is_owner, created_at) VALUES ($1, $2, false, $3)`, w.id, uAB, now)
	}
	exec(`INSERT INTO tuorps.brands (id, workspace_id, name, created_at, updated_at) VALUES ($1, $2, 'Brand A', $3, $3), ($4, $5, 'Brand B', $3, $3)`, brA, wsA, now, brB, wsB)
	exec(`INSERT INTO tuorps.brand_roles (workspace_id, brand_id, user_id, role, created_at) VALUES ($1, $2, $3, 'publisher', $4), ($5, $6, $3, 'viewer', $4)`, wsA, brA, uAB, now, wsB, brB)
	exec(`INSERT INTO tuorps.invitations (id, workspace_id, email_normalized, brand_roles, invited_by, token_hash, expires_at, created_at)
		VALUES ($1, $2, 'new@example.in', '[]', $3, $4, $5, $6), ($7, $8, 'new@example.in', '[]', $9, $10, $5, $6)`,
		invA, wsA, uA, hashOf("invite-a"), now.Add(72*time.Hour), now, invB, wsB, uB, hashOf("invite-b"))
	exec(`INSERT INTO tuorps.email_tokens (id, user_id, purpose, token_hash, expires_at, created_at) VALUES
		($1, $2, 'verify_email', $3, $4, $5), ($6, $7, 'reset_password', $8, $4, $5)`,
		tokA, uA, hashOf("verify-a"), now.Add(time.Hour), now, tokB, uB, hashOf("reset-b"))
	exec(`INSERT INTO tuorps.audit_log (id, workspace_id, actor_user_id, action, target_type, target_id, at) VALUES
		($1, $2, $3, 'brand.create', 'brand', $4, $5), ($6, $7, $8, 'brand.create', 'brand', $9, $5), ($10, NULL, $3, 'auth.password_change', 'user', $3, $5)`,
		audA, wsA, uA, brA, now, audB, wsB, uB, brB, audUA)
}

// asApp runs fn in a tuorps_app transaction with the given tenant GUCs
// ("" leaves one unset) and rolls back.
func asApp(t *testing.T, app *sql.DB, ws, user string, fn func(tx *sql.Tx)) {
	t.Helper()
	tx, err := app.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.workspace_id', $1, true), set_config('app.user_id', $2, true)`, ws, user); err != nil {
		t.Fatal(err)
	}
	fn(tx)
}

func ids(t *testing.T, tx *sql.Tx, q string, args ...any) []string {
	t.Helper()
	rows, err := tx.QueryContext(t.Context(), q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}

func TestIdentityTenancyMigration(t *testing.T) {
	pg := testinfra.StartPostgres(t)
	migrator, app, admin := open(t, pg.MigratorURL), open(t, pg.AppURL), open(t, pg.AdminURL)
	up(t, migrator)
	seed(t, admin)

	tables := []string{"users", "user_credentials", "recovery_codes", "email_tokens", "workspaces", "workspace_members", "brands", "brand_roles", "invitations", "audit_log"}

	t.Run("no tenant context sees nothing (fail closed)", func(t *testing.T) {
		asApp(t, app, "", "", func(tx *sql.Tx) {
			for _, tbl := range tables {
				var n int
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+tbl).Scan(&n); err != nil || n != 0 {
					t.Errorf("%s: %d rows visible without tenant context (err %v)", tbl, n, err)
				}
			}
		})
	})

	t.Run("workspace A as its owner sees only A", func(t *testing.T) {
		asApp(t, app, wsA, uA, func(tx *sql.Tx) {
			checks := []struct {
				q    string
				want []string
			}{
				{"SELECT id::text FROM brands", []string{brA}},
				{"SELECT brand_id::text FROM brand_roles", []string{brA}},
				{"SELECT id::text FROM invitations", []string{invA}},
				{"SELECT id::text FROM workspaces", []string{wsA}},
				{"SELECT user_id::text FROM workspace_members", []string{uA, uAB}},
				{"SELECT id::text FROM users", []string{uA, uAB}},
				{"SELECT user_id::text FROM user_credentials", []string{uA}},
				{"SELECT user_id::text FROM recovery_codes", []string{uA}},
				{"SELECT id::text FROM email_tokens", []string{tokA}},
				{"SELECT id::text FROM audit_log", []string{audA, audUA}},
			}
			for _, c := range checks {
				if got := ids(t, tx, c.q); !sameSet(got, c.want...) {
					t.Errorf("%s = %v, want %v", c.q, got, c.want)
				}
			}
		})
	})

	t.Run("a member of both sees both workspaces in its list, but only the current one's data", func(t *testing.T) {
		asApp(t, app, wsB, uAB, func(tx *sql.Tx) {
			if got := ids(t, tx, "SELECT id::text FROM workspaces"); !sameSet(got, wsA, wsB) {
				t.Errorf("workspaces = %v", got)
			}
			if got := ids(t, tx, "SELECT id::text FROM brands"); !sameSet(got, brB) {
				t.Errorf("brands = %v", got)
			}
			if got := ids(t, tx, "SELECT user_id::text FROM user_credentials"); !sameSet(got, uAB) {
				t.Errorf("credentials = %v", got)
			}
		})
	})

	t.Run("cross-tenant writes are refused or affect nothing", func(t *testing.T) {
		asApp(t, app, wsA, uA, func(tx *sql.Tx) {
			_, err := tx.ExecContext(t.Context(), `INSERT INTO brands (id, workspace_id, name, created_at, updated_at) VALUES ('0199c3f0-0000-7000-8000-00000000ba99', $1, 'x', now(), now())`, wsB)
			if sqlState(err) != "42501" {
				t.Errorf("insert into B: %v, want RLS violation", err)
			}
		})
		asApp(t, app, wsA, uA, func(tx *sql.Tx) {
			for _, q := range []string{
				"UPDATE brands SET name = 'pwned' WHERE id = '" + brB + "'",
				"DELETE FROM brands WHERE id = '" + brB + "'",
				"UPDATE user_credentials SET password_hash = 'x' WHERE user_id = '" + uB + "'",
				"DELETE FROM workspace_members WHERE workspace_id = '" + wsB + "'",
			} {
				res, err := tx.ExecContext(t.Context(), q)
				if err != nil {
					t.Errorf("%s: %v", q, err)
					continue
				}
				if n, _ := res.RowsAffected(); n != 0 {
					t.Errorf("%s affected %d rows", q, n)
				}
			}
		})
	})

	t.Run("composite keys forbid cross-tenant references even for the superuser", func(t *testing.T) {
		_, err := admin.ExecContext(t.Context(), `INSERT INTO tuorps.brand_roles (workspace_id, brand_id, user_id, role, created_at) VALUES ($1, $2, $3, 'viewer', now())`, wsA, brB, uA)
		if sqlState(err) != "23503" {
			t.Errorf("brand role pointing at another workspace's brand: %v, want FK violation", err)
		}
		_, err = admin.ExecContext(t.Context(), `INSERT INTO tuorps.brand_roles (workspace_id, brand_id, user_id, role, created_at) VALUES ($1, $2, $3, 'viewer', now())`, wsA, brA, uB)
		if sqlState(err) != "23503" {
			t.Errorf("brand role for a non-member: %v, want FK violation", err)
		}
		_, err = admin.ExecContext(t.Context(), `INSERT INTO tuorps.brand_roles (workspace_id, brand_id, user_id, role, created_at) VALUES ($1, $2, $3, 'superuser', now())`, wsA, brA, uA)
		if sqlState(err) != "23514" {
			t.Errorf("unknown role: %v, want check violation", err)
		}
	})

	t.Run("audit log is append-only for the runtime role", func(t *testing.T) {
		for _, q := range []string{"UPDATE audit_log SET action = 'x'", "DELETE FROM audit_log", "TRUNCATE audit_log"} {
			asApp(t, app, wsA, uA, func(tx *sql.Tx) {
				if _, err := tx.ExecContext(t.Context(), q); sqlState(err) != "42501" {
					t.Errorf("%s: %v, want permission denied", q, err)
				}
			})
		}
		asApp(t, app, wsA, uA, func(tx *sql.Tx) {
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO audit_log (id, workspace_id, actor_user_id, action, target_type, target_id, at) VALUES ('0199c3f0-0000-7000-8000-0000000003a9', $1, $2, 'x', 'y', 'z', now())`, wsA, uA); err != nil {
				t.Errorf("insert own-workspace audit row: %v", err)
			}
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO audit_log (id, workspace_id, actor_user_id, action, target_type, target_id, at) VALUES ('0199c3f0-0000-7000-8000-0000000003b9', $1, $2, 'x', 'y', 'z', now())`, wsB, uA); sqlState(err) != "42501" {
				t.Errorf("insert audit row into B: %v, want RLS violation", err)
			}
		})
	})

	t.Run("pre-auth login lookup returns exactly one account", func(t *testing.T) {
		asApp(t, app, "", "", func(tx *sql.Tx) {
			var id, hash string
			var verified, totp, disabled bool
			err := tx.QueryRowContext(t.Context(), `SELECT id::text, password_hash, email_verified, totp_enabled, disabled FROM auth_lookup_login('b@example.in')`).
				Scan(&id, &hash, &verified, &totp, &disabled)
			if err != nil || id != uB || !strings.Contains(hash, uB) || verified || totp || disabled {
				t.Fatalf("lookup = %s %s %v %v %v, %v", id, hash, verified, totp, disabled, err)
			}
			var n int
			_ = tx.QueryRowContext(t.Context(), `SELECT count(*) FROM auth_lookup_login('nobody@example.in')`).Scan(&n)
			if n != 0 {
				t.Errorf("unknown email returned %d rows", n)
			}
		})
	})

	t.Run("email tokens redeem once, for the right purpose, before expiry", func(t *testing.T) {
		now := time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC)
		redeem := func(token, purpose string, at time.Time) sql.NullString {
			var uid sql.NullString
			asApp(t, app, "", "", func(tx *sql.Tx) {
				if err := tx.QueryRowContext(t.Context(), `SELECT auth_redeem_email_token($1, $2, $3)::text`, hashOf(token), purpose, at).Scan(&uid); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			})
			return uid
		}
		if got := redeem("verify-a", "reset_password", now); got.Valid {
			t.Error("token redeemed for the wrong purpose")
		}
		if got := redeem("verify-a", "verify_email", now.Add(2*time.Hour)); got.Valid {
			t.Error("expired token redeemed")
		}
		if got := redeem("verify-a", "verify_email", now); got.String != uA {
			t.Errorf("first redemption = %v, want %s", got, uA)
		}
		if got := redeem("verify-a", "verify_email", now); got.Valid {
			t.Error("token redeemed twice")
		}
	})

	t.Run("pending invitation lookup by token", func(t *testing.T) {
		asApp(t, app, "", "", func(tx *sql.Tx) {
			var id, ws string
			err := tx.QueryRowContext(t.Context(), `SELECT id::text, workspace_id::text FROM invitation_by_token($1, $2)`, hashOf("invite-b"), time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)).Scan(&id, &ws)
			if err != nil || id != invB || ws != wsB {
				t.Fatalf("invitation = %s %s, %v", id, ws, err)
			}
			var n int
			_ = tx.QueryRowContext(t.Context(), `SELECT count(*) FROM invitation_by_token($1, $2)`, hashOf("invite-b"), time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)).Scan(&n)
			if n != 0 {
				t.Error("expired invitation returned")
			}
		})
	})

	t.Run("keyset list queries are index-backed", func(t *testing.T) {
		queries := map[string]struct{ sql, index string }{
			"audit log":        {"SELECT id FROM audit_log WHERE workspace_id = $1 AND id < $2 ORDER BY id DESC LIMIT 50", "audit_log_workspace"},
			"brands":           {"SELECT id FROM brands WHERE workspace_id = $1 AND id > $2 ORDER BY id LIMIT 50", "brands_workspace_id"},
			"members":          {"SELECT user_id FROM workspace_members WHERE workspace_id = $1 AND user_id > $2 ORDER BY user_id LIMIT 50", "workspace_members_pkey"},
			"my workspaces":    {"SELECT workspace_id FROM workspace_members WHERE user_id = $1 AND workspace_id > $2 ORDER BY workspace_id LIMIT 50", "workspace_members_user"},
			"invitations":      {"SELECT id FROM invitations WHERE workspace_id = $1 AND id < $2 ORDER BY id DESC LIMIT 50", "invitations_workspace"},
			"own security log": {"SELECT id FROM audit_log WHERE workspace_id IS NULL AND actor_user_id = $1 AND id < $2 ORDER BY id DESC LIMIT 50", "audit_log_account"},
		}
		for name, q := range queries {
			t.Run(name, func(t *testing.T) {
				conn, err := admin.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = conn.Close() }()
				if _, err := conn.ExecContext(t.Context(), "SET search_path = tuorps; SET enable_seqscan = off"); err != nil {
					t.Fatal(err)
				}
				var plan strings.Builder
				rows, err := conn.QueryContext(t.Context(), "EXPLAIN "+q.sql, wsA, "ffffffff-ffff-7fff-bfff-ffffffffffff")
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					var line string
					_ = rows.Scan(&line)
					plan.WriteString(line + "\n")
				}
				_ = rows.Close()
				usesIndex := strings.Contains(plan.String(), " using "+q.index+" ") || strings.Contains(plan.String(), "Index Scan on "+q.index+" ")
				if strings.Contains(plan.String(), "Seq Scan") || !usesIndex {
					t.Errorf("want an index scan using %s:\n%s", q.index, plan.String())
				}
			})
		}
	})
}
