package migrations_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

// Connections fixture on top of seed(): grant gA and connections cA (active,
// due) and cA2 (disconnected) in workspace A / brand A; grant gB and
// connection cB in B. cA and cB are the same Facebook Page, connected by two
// workspaces (an agency and its client).
const (
	gA      = "0199c3f0-0000-7000-8000-000000004a01"
	gB      = "0199c3f0-0000-7000-8000-000000004b01"
	cA      = "0199c3f0-0000-7000-8000-000000005a01"
	cA2     = "0199c3f0-0000-7000-8000-000000005a02"
	cB      = "0199c3f0-0000-7000-8000-000000005b01"
	brA2    = "0199c3f0-0000-7000-8000-00000000ba02"
	page    = "104857600000001"
	metaUID = "10228374655566"
)

var connNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func seedConnections(t *testing.T, admin *sql.DB) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := admin.ExecContext(t.Context(), q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO tuorps.brands (id, workspace_id, name, created_at, updated_at) VALUES ($1, $2, 'Brand A2', $3, $3)`, brA2, wsA, connNow)
	exec(`INSERT INTO tuorps.oauth_grants (id, workspace_id, brand_id, provider, provider_user_id, access_token_enc, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, 'meta', $4, 'v1.k1.AAAA', $5, $6, $6), ($7, $8, $9, 'meta', $4, 'v1.k0.BBBB', $10, $6, $6)`,
		gA, wsA, brA, metaUID, uA, connNow, gB, wsB, brB, uB)
	exec(`INSERT INTO tuorps.connections (id, workspace_id, brand_id, grant_id, kind, external_id, display_name, token_enc, status, next_due_at, created_at, updated_at) VALUES
		($1, $2, $3, $4, 'facebook_page', $5, 'Page', 'v1.k1.CCCC', 'active', $6, $7, $7),
		($8, $2, $3, $4, 'facebook_page', $5, 'Page', NULL, 'disconnected', NULL, $7, $7),
		($9, $10, $11, $12, 'facebook_page', $5, 'Page', 'v1.k1.DDDD', 'active', $13, $7, $7)`,
		cA, wsA, brA, gA, page, connNow.Add(-time.Minute), connNow, cA2, cB, wsB, brB, gB, connNow.Add(time.Hour))
}

func TestConnectionsMigration(t *testing.T) {
	pg := testinfra.StartPostgres(t)
	migrator, app, admin := open(t, pg.MigratorURL), open(t, pg.AppURL), open(t, pg.AdminURL)
	up(t, migrator)
	seed(t, admin)
	seedConnections(t, admin)

	t.Run("no tenant context sees no grants or connections (fail closed)", func(t *testing.T) {
		asApp(t, app, "", "", func(tx *sql.Tx) {
			for _, tbl := range []string{"oauth_grants", "connections"} {
				var n int
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+tbl).Scan(&n); err != nil || n != 0 {
					t.Errorf("%s: %d rows visible without tenant context (err %v)", tbl, n, err)
				}
			}
		})
	})

	t.Run("a workspace sees only its own grants and connections", func(t *testing.T) {
		asApp(t, app, wsA, uA, func(tx *sql.Tx) {
			if got := ids(t, tx, "SELECT id FROM oauth_grants"); !sameSet(got, gA) {
				t.Errorf("grants = %v", got)
			}
			if got := ids(t, tx, "SELECT id FROM connections"); !sameSet(got, cA, cA2) {
				t.Errorf("connections = %v", got)
			}
		})
	})

	t.Run("cross-tenant writes are refused or affect nothing", func(t *testing.T) {
		asApp(t, app, wsA, uA, func(tx *sql.Tx) {
			_, err := tx.ExecContext(t.Context(), `INSERT INTO connections (id, workspace_id, brand_id, grant_id, kind, external_id, display_name, status, created_at, updated_at)
				VALUES ('0199c3f0-0000-7000-8000-000000005a09', $1, $2, $3, 'facebook_page', 'x', 'x', 'active', now(), now())`, wsB, brB, gB)
			if sqlState(err) != "42501" {
				t.Errorf("insert into B from A: %v", err)
			}
		})
		// A refused statement aborts its transaction, so the rest run in a fresh one.
		asApp(t, app, wsA, uA, func(tx *sql.Tx) {
			for _, q := range []string{`UPDATE connections SET status = 'disconnected' WHERE id = '` + cB + `'`, `UPDATE oauth_grants SET access_token_enc = NULL WHERE id = '` + gB + `'`, `DELETE FROM connections WHERE id = '` + cB + `'`} {
				res, err := tx.ExecContext(t.Context(), q)
				if err != nil {
					t.Fatalf("%s: %v", q, err)
				}
				if n, _ := res.RowsAffected(); n != 0 {
					t.Errorf("%s affected %d rows in another workspace", q, n)
				}
			}
		})
	})

	t.Run("a connection must sit on its grant's brand and workspace, even for the superuser", func(t *testing.T) {
		for name, args := range map[string][]any{
			"grant from another brand":     {wsA, brA2, gA},
			"grant from another workspace": {wsA, brA, gB},
		} {
			_, err := admin.ExecContext(t.Context(), `INSERT INTO tuorps.connections (id, workspace_id, brand_id, grant_id, kind, external_id, display_name, status, created_at, updated_at)
				VALUES ('0199c3f0-0000-7000-8000-000000005a0f', $1, $2, $3, 'facebook_page', 'y', 'y', 'active', now(), now())`, args...)
			if sqlState(err) != "23503" {
				t.Errorf("%s: %v, want foreign key violation", name, err)
			}
		}
	})

	t.Run("one live connection per account per workspace; other workspaces may share it", func(t *testing.T) {
		_, err := admin.ExecContext(t.Context(), `INSERT INTO tuorps.connections (id, workspace_id, brand_id, grant_id, kind, external_id, display_name, status, created_at, updated_at)
			VALUES ('0199c3f0-0000-7000-8000-000000005a0e', $1, $2, $3, 'facebook_page', $4, 'Page', 'active', now(), now())`, wsA, brA, gA, page)
		if sqlState(err) != "23505" {
			t.Errorf("second live connection of the same page in A: %v, want unique violation", err)
		}
	})

	t.Run("constraints reject unknown providers, kinds and statuses", func(t *testing.T) {
		for name, q := range map[string]string{
			"provider": `UPDATE tuorps.oauth_grants SET provider = 'myspace' WHERE id = '` + gA + `'`,
			"kind":     `UPDATE tuorps.connections SET kind = 'page' WHERE id = '` + cA + `'`,
			"status":   `UPDATE tuorps.connections SET status = 'expiring' WHERE id = '` + cA + `'`,
		} {
			if _, err := admin.ExecContext(t.Context(), q); sqlState(err) != "23514" {
				t.Errorf("%s: %v, want check violation", name, err)
			}
		}
	})

	t.Run("token key ids are derived from the ciphertext", func(t *testing.T) {
		var grantKey, connKey string
		if err := admin.QueryRowContext(t.Context(), `SELECT access_token_key_id FROM tuorps.oauth_grants WHERE id = $1`, gB).Scan(&grantKey); err != nil || grantKey != "k0" {
			t.Errorf("grant key id = %q, %v", grantKey, err)
		}
		if err := admin.QueryRowContext(t.Context(), `SELECT token_key_id FROM tuorps.connections WHERE id = $1`, cA).Scan(&connKey); err != nil || connKey != "k1" {
			t.Errorf("connection key id = %q, %v", connKey, err)
		}
	})

	t.Run("webhook receipts: insert-only for the runtime role, duplicates detected", func(t *testing.T) {
		insert := `INSERT INTO webhook_receipts (provider, dedupe_key, received_at) VALUES ('meta', 'k-1', $1) ON CONFLICT DO NOTHING`
		for i, want := range []int64{1, 0} {
			res, err := app.ExecContext(t.Context(), insert, connNow)
			if err != nil {
				t.Fatal(err)
			}
			if n, _ := res.RowsAffected(); n != want {
				t.Errorf("insert #%d affected %d rows, want %d", i+1, n, want)
			}
		}
		for _, q := range []string{`SELECT count(*) FROM webhook_receipts`, `UPDATE webhook_receipts SET dedupe_key = 'x'`, `DELETE FROM webhook_receipts`} {
			if _, err := app.ExecContext(t.Context(), q); sqlState(err) != "42501" {
				t.Errorf("%s as tuorps_app: %v, want permission denied", q, err)
			}
		}
	})

	t.Run("data deletion requests: insert-only, status through a function", func(t *testing.T) {
		if _, err := app.ExecContext(t.Context(), `INSERT INTO data_deletion_requests (confirmation_code, provider, provider_user_id, status, requested_at) VALUES ('ddr-1', 'meta', $1, 'pending', $2)`, metaUID, connNow); err != nil {
			t.Fatal(err)
		}
		if _, err := app.ExecContext(t.Context(), `SELECT * FROM data_deletion_requests`); sqlState(err) != "42501" {
			t.Errorf("direct read: %v, want permission denied", err)
		}
		var status string
		if err := app.QueryRowContext(t.Context(), `SELECT data_deletion_status('ddr-1')`).Scan(&status); err != nil || status != "pending" {
			t.Errorf("status = %q, %v", status, err)
		}
		var done bool
		if err := app.QueryRowContext(t.Context(), `SELECT data_deletion_complete('ddr-1', $1)`, connNow).Scan(&done); err != nil || !done {
			t.Errorf("complete = %v, %v", done, err)
		}
		if err := app.QueryRowContext(t.Context(), `SELECT data_deletion_status('ddr-1')`).Scan(&status); err != nil || status != "done" {
			t.Errorf("status after completion = %q, %v", status, err)
		}
		var missing sql.NullString
		if err := app.QueryRowContext(t.Context(), `SELECT data_deletion_status('nope')`).Scan(&missing); err != nil || missing.Valid {
			t.Errorf("unknown code = %v, %v; want NULL", missing, err)
		}
	})

	t.Run("cross-tenant discovery functions return only ids", func(t *testing.T) {
		rows := func(q string, args ...any) []string {
			t.Helper()
			r, err := app.QueryContext(t.Context(), q, args...)
			if err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			defer func() { _ = r.Close() }()
			var out []string
			for r.Next() {
				var id, ws string
				if err := r.Scan(&id, &ws); err != nil {
					t.Fatal(err)
				}
				out = append(out, id+"@"+ws)
			}
			return out
		}
		if got := rows(`SELECT id, workspace_id FROM connections_due($1, 100)`, connNow); !sameSet(got, cA+"@"+wsA) {
			t.Errorf("due now = %v, want only cA (cB is due later, cA2 is disconnected)", got)
		}
		if got := rows(`SELECT id, workspace_id FROM connections_due($1, 100)`, connNow.Add(2*time.Hour)); !sameSet(got, cA+"@"+wsA, cB+"@"+wsB) {
			t.Errorf("due later = %v", got)
		}
		if got := rows(`SELECT id, workspace_id FROM connections_by_external('facebook_page', $1)`, page); !sameSet(got, cA+"@"+wsA, cB+"@"+wsB) {
			t.Errorf("by external = %v, want both workspaces' live connections", got)
		}
		if got := rows(`SELECT id, workspace_id FROM grants_by_provider_user('meta', $1)`, metaUID); !sameSet(got, gA+"@"+wsA, gB+"@"+wsB) {
			t.Errorf("grants by provider user = %v", got)
		}
		if _, err := admin.ExecContext(t.Context(), `UPDATE tuorps.brands SET archived_at = $1 WHERE id = $2`, connNow, brA); err != nil {
			t.Fatal(err)
		}
		if got := rows(`SELECT id, workspace_id FROM connections_due($1, 100)`, connNow.Add(2*time.Hour)); !sameSet(got, cB+"@"+wsB) {
			t.Errorf("due after archiving brand A = %v, want only cB", got)
		}
	})

	t.Run("webhook receipts are pruned by age through a function", func(t *testing.T) {
		if _, err := admin.ExecContext(t.Context(), `INSERT INTO tuorps.webhook_receipts (provider, dedupe_key, received_at) VALUES ('linkedin', 'old', $1), ('linkedin', 'new', $2)`, connNow.Add(-8*24*time.Hour), connNow); err != nil {
			t.Fatal(err)
		}
		var pruned int64
		if err := app.QueryRowContext(t.Context(), `SELECT webhook_receipts_prune($1)`, connNow.Add(-7*24*time.Hour)).Scan(&pruned); err != nil || pruned != 1 {
			t.Errorf("pruned = %d, %v; want 1", pruned, err)
		}
	})

	t.Run("connection queries are index-backed", func(t *testing.T) {
		queries := map[string]struct{ sql, index string }{
			"brand list":     {"SELECT id FROM connections WHERE workspace_id = $1 AND brand_id = $2 AND id > $3 ORDER BY id LIMIT 50", "connections_brand"},
			"due sweep":      {"SELECT id FROM connections WHERE next_due_at <= $1 AND status <> 'disconnected' ORDER BY next_due_at, id LIMIT 100", "connections_due"},
			"webhook route":  {"SELECT id FROM connections WHERE kind = 'facebook_page' AND external_id = $1 AND status <> 'disconnected'", "connections_external"},
			"provider user":  {"SELECT id FROM oauth_grants WHERE provider = 'meta' AND provider_user_id = $1", "oauth_grants_provider_user"},
			"receipts prune": {"SELECT 1 FROM webhook_receipts WHERE received_at < $1", "webhook_receipts_received"},
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
				args := []any{wsA, brA, "ffffffff-ffff-7fff-bfff-ffffffffffff"}[:strings.Count(q.sql, "$")]
				if name == "webhook route" || name == "provider user" {
					args = []any{"x"}
				}
				if name == "due sweep" || name == "receipts prune" {
					args = []any{connNow}
				}
				rows, err := conn.QueryContext(t.Context(), "EXPLAIN "+q.sql, args...)
				if err != nil {
					t.Fatal(err)
				}
				var plan strings.Builder
				for rows.Next() {
					var line string
					_ = rows.Scan(&line)
					plan.WriteString(line + "\n")
				}
				_ = rows.Close()
				p := plan.String()
				if strings.Contains(p, "Seq Scan") || !(strings.Contains(p, " using "+q.index+" ") || strings.Contains(p, " on "+q.index)) {
					t.Errorf("want an index scan using %s:\n%s", q.index, p)
				}
			})
		}
	})
}
