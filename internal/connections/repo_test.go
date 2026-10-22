package connections_test

import (
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya31415/tuorps/internal/audit"
	auditcore "github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/connections"
	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
	"github.com/yahya31415/tuorps/internal/workspace"
)

func id(s string) uuid.UUID { return uuid.MustParse("0199c3f0-0000-7000-8000-" + s) }

func ownerA() uuid.UUID { return id("0000000000a1") }
func ownerB() uuid.UUID { return id("0000000000b1") }
func wsA() uuid.UUID    { return id("00000000000a") }
func wsB() uuid.UUID    { return id("00000000000b") }
func brA1() uuid.UUID   { return id("00000000ba01") }
func brA2() uuid.UUID   { return id("00000000ba02") }
func brB1() uuid.UUID   { return id("00000000bb01") }

type fixture struct {
	pool  *pgxpool.Pool
	admin *pgxpool.Pool
	w     audit.Writer
	now   time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pg := testinfra.MigratedPostgres(t)
	mk := func(url string) *pgxpool.Pool {
		p, err := postgres.NewPool(t.Context(), config.DB{URL: config.NewSecret(url), MaxConns: 4, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	f := fixture{pool: mk(pg.AppURL), admin: mk(pg.AdminURL), now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	f.w = audit.NewWriter(f.pool, ids.System(), clock.Fixed(f.now))
	for _, u := range []struct {
		id    uuid.UUID
		email string
	}{{ownerA(), "a@example.in"}, {ownerB(), "b@example.in"}} {
		if _, err := f.admin.Exec(t.Context(), `INSERT INTO tuorps.users (id, email, email_normalized, display_name, email_verified_at, created_at, updated_at) VALUES ($1, $2, $2, 'U', now(), now(), now())`, u.id, u.email); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range []struct{ ws, owner, brand uuid.UUID }{{wsA(), ownerA(), brA1()}, {wsB(), ownerB(), brB1()}} {
		if err := f.write(t, scope(b.ws, b.owner), func(tx audit.Tx) error {
			return workspace.Bootstrap(t.Context(), tx, workspace.NewWorkspace{ID: b.ws, Name: "WS", Owner: b.owner, BrandID: b.brand, BrandName: "Brand", Now: f.now})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.write(t, scope(wsA(), ownerA()), func(tx audit.Tx) error {
		return workspace.CreateBrand(t.Context(), tx, brA2(), "Second", f.now)
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func scope(ws, u uuid.UUID) postgres.Scope { return postgres.Scope{WorkspaceID: ws, UserID: u} }

func (f fixture) write(t *testing.T, s postgres.Scope, fn func(audit.Tx) error) error {
	t.Helper()
	ev, err := auditcore.NewEvent("connection.test", "connection", "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	return f.w.InWriteTx(t.Context(), s, ev, auditcore.Meta{}, fn)
}

func (f fixture) read(t *testing.T, s postgres.Scope, fn func(pgx.Tx) error) error {
	t.Helper()
	return postgres.InReadTx(t.Context(), f.pool, s, fn)
}

func (f fixture) grant(id, brand uuid.UUID) connections.NewGrant {
	return connections.NewGrant{
		ID: id, BrandID: brand, Provider: core.ProviderMeta, ProviderUserID: "10228374655566",
		Scopes: []string{"pages_show_list"}, MissingScopes: []string{"read_insights"},
		AccessTokenEnc: "v1.k1.access", RefreshTokenEnc: "",
		AccessExpiresAt: f.now.Add(60 * 24 * time.Hour), DataAccessExpiresAt: f.now.Add(90 * 24 * time.Hour),
		CreatedBy: ownerA(), Now: f.now,
	}
}

func (f fixture) conn(id, brand, grant uuid.UUID, external string) connections.NewConnection {
	return connections.NewConnection{
		ID: id, BrandID: brand, GrantID: grant, Kind: core.KindFacebookPage, ExternalID: external,
		DisplayName: "Kerala Spices", TokenEnc: "v1.k1.page", NextDueAt: f.now.Add(time.Hour), Now: f.now,
	}
}

func TestConnectionsRepository(t *testing.T) {
	f := newFixture(t)
	asA, asB := scope(wsA(), ownerA()), scope(wsB(), ownerB())
	grantA, connA := id("000000004a01"), id("000000005a01")

	if err := f.write(t, asA, func(tx audit.Tx) error {
		if err := connections.InsertGrant(t.Context(), tx, f.grant(grantA, brA1())); err != nil {
			return err
		}
		return connections.InsertConnection(t.Context(), tx, f.conn(connA, brA1(), grantA, "page-1"))
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	t.Run("a grant and its connection read back, tokens still sealed", func(t *testing.T) {
		err := f.read(t, asA, func(tx pgx.Tx) error {
			g, err := connections.GetGrant(t.Context(), tx, wsA(), brA1(), grantA)
			if err != nil {
				return err
			}
			if g.Provider != core.ProviderMeta || g.AccessTokenEnc != "v1.k1.access" || g.RefreshTokenEnc != "" ||
				!slices.Equal(g.Scopes, []string{"pages_show_list"}) || !slices.Equal(g.MissingScopes, []string{"read_insights"}) ||
				!g.DataAccessExpiresAt.Equal(f.now.Add(90*24*time.Hour)) || !g.TokenIssuedAt.IsZero() || g.CreatedBy != ownerA() {
				t.Errorf("grant = %+v", g)
			}
			c, err := connections.GetConnection(t.Context(), tx, wsA(), brA1(), connA)
			if err != nil {
				return err
			}
			if c.GrantID != grantA || c.Kind != core.KindFacebookPage || c.ExternalID != "page-1" || c.Status != core.StatusActive ||
				c.TokenEnc != "v1.k1.page" || c.TokenKeyID != "k1" || !c.NextDueAt.Equal(f.now.Add(time.Hour)) || !c.LastCheckedAt.IsZero() || c.LastError != "" {
				t.Errorf("connection = %+v", c)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("another workspace cannot see, list or disconnect it", func(t *testing.T) {
		err := f.read(t, asB, func(tx pgx.Tx) error {
			if _, err := connections.GetConnection(t.Context(), tx, wsA(), brA1(), connA); !errors.Is(err, connections.ErrNotFound) {
				t.Errorf("get from B: %v", err)
			}
			if _, err := connections.GetGrant(t.Context(), tx, wsA(), brA1(), grantA); !errors.Is(err, connections.ErrNotFound) {
				t.Errorf("grant from B: %v", err)
			}
			list, err := connections.ListConnections(t.Context(), tx, wsA(), brA1(), uuid.Nil(), 50)
			if err != nil || len(list) != 0 {
				t.Errorf("list from B = %v, %v", list, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		err = f.write(t, asB, func(tx audit.Tx) error {
			return connections.Disconnect(t.Context(), tx, brA1(), connA, f.now)
		})
		if !errors.Is(err, connections.ErrNotFound) {
			t.Errorf("disconnect from B: %v", err)
		}
	})

	t.Run("the wrong brand in the same workspace is not found", func(t *testing.T) {
		err := f.read(t, asA, func(tx pgx.Tx) error {
			_, err := connections.GetConnection(t.Context(), tx, wsA(), brA2(), connA)
			return err
		})
		if !errors.Is(err, connections.ErrNotFound) {
			t.Errorf("get via brand A2: %v", err)
		}
	})

	t.Run("a connection cannot use another brand's grant", func(t *testing.T) {
		err := f.write(t, asA, func(tx audit.Tx) error {
			return connections.InsertConnection(t.Context(), tx, f.conn(id("000000005a09"), brA2(), grantA, "page-9"))
		})
		if !errors.Is(err, connections.ErrNotFound) {
			t.Errorf("insert on brand A2 with brand A1's grant: %v", err)
		}
	})

	t.Run("the same account cannot be connected twice while live", func(t *testing.T) {
		err := f.write(t, asA, func(tx audit.Tx) error {
			return connections.InsertConnection(t.Context(), tx, f.conn(id("000000005a02"), brA1(), grantA, "page-1"))
		})
		if !errors.Is(err, connections.ErrAlreadyConnected) {
			t.Errorf("duplicate: %v", err)
		}
	})

	t.Run("keyset list pages by id", func(t *testing.T) {
		if err := f.write(t, asA, func(tx audit.Tx) error {
			for i, ext := range []string{"page-2", "page-3"} {
				if err := connections.InsertConnection(t.Context(), tx, f.conn(id("000000005a1"+string(rune('0'+i))), brA1(), grantA, ext)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		err := f.read(t, asA, func(tx pgx.Tx) error {
			first, err := connections.ListConnections(t.Context(), tx, wsA(), brA1(), uuid.Nil(), 2)
			if err != nil {
				return err
			}
			rest, err := connections.ListConnections(t.Context(), tx, wsA(), brA1(), first[len(first)-1].ID, 2)
			if err != nil {
				return err
			}
			if len(first) != 2 || len(rest) != 1 || first[0].ID != connA || rest[0].ID == first[1].ID {
				t.Errorf("pages = %d + %d", len(first), len(rest))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("disconnect wipes the token, parks maintenance, and frees the account", func(t *testing.T) {
		later := f.now.Add(time.Minute)
		if err := f.write(t, asA, func(tx audit.Tx) error {
			return connections.Disconnect(t.Context(), tx, brA1(), connA, later)
		}); err != nil {
			t.Fatal(err)
		}
		err := f.read(t, asA, func(tx pgx.Tx) error {
			c, err := connections.GetConnection(t.Context(), tx, wsA(), brA1(), connA)
			if err != nil {
				return err
			}
			if c.Status != core.StatusDisconnected || c.TokenEnc != "" || !c.NextDueAt.IsZero() {
				t.Errorf("after disconnect = %+v", c)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.write(t, asA, func(tx audit.Tx) error {
			return connections.InsertConnection(t.Context(), tx, f.conn(id("000000005a03"), brA1(), grantA, "page-1"))
		}); err != nil {
			t.Errorf("reconnecting a disconnected account: %v", err)
		}
	})

	t.Run("wiping a grant clears both tokens", func(t *testing.T) {
		if err := f.write(t, asA, func(tx audit.Tx) error {
			return connections.WipeGrantTokens(t.Context(), tx, brA1(), grantA, f.now)
		}); err != nil {
			t.Fatal(err)
		}
		err := f.read(t, asA, func(tx pgx.Tx) error {
			g, err := connections.GetGrant(t.Context(), tx, wsA(), brA1(), grantA)
			if err == nil && (g.AccessTokenEnc != "" || g.RefreshTokenEnc != "") {
				t.Errorf("tokens survive the wipe: %+v", g)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// Reconnecting moves a live connection to the new grant and resets its
// health; the old grant may be wiped once no live connection uses it.
func TestReconnectRepository(t *testing.T) {
	f := newFixture(t)
	asA := scope(wsA(), ownerA())
	oldGrant, newGrant, conn := id("000000004a01"), id("000000004a02"), id("000000005a01")
	if err := f.write(t, asA, func(tx audit.Tx) error {
		for _, g := range []uuid.UUID{oldGrant, newGrant} {
			if err := connections.InsertGrant(t.Context(), tx, f.grant(g, brA1())); err != nil {
				return err
			}
		}
		return connections.InsertConnection(t.Context(), tx, f.conn(conn, brA1(), oldGrant, "page-1"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(t.Context(), `UPDATE tuorps.connections SET status = 'needs_reauth', last_error = 'reauth_required', warned_days = 3, next_due_at = NULL WHERE id = $1`, conn); err != nil {
		t.Fatal(err)
	}

	live := func(g uuid.UUID) int64 {
		t.Helper()
		var n int64
		if err := f.read(t, asA, func(tx pgx.Tx) error {
			var err error
			n, err = connections.LiveConnectionsOnGrant(t.Context(), tx, wsA(), brA1(), g)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if live(oldGrant) != 1 || live(newGrant) != 0 {
		t.Fatalf("before: old %d, new %d", live(oldGrant), live(newGrant))
	}

	later := f.now.Add(time.Hour)
	if err := f.write(t, asA, func(tx audit.Tx) error {
		return connections.RepointConnection(t.Context(), tx, brA1(), conn, connections.Repoint{GrantID: newGrant, TokenEnc: "v1.k1.newpage", NextDueAt: later.Add(24 * time.Hour), Now: later})
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.read(t, asA, func(tx pgx.Tx) error {
		c, err := connections.GetConnection(t.Context(), tx, wsA(), brA1(), conn)
		if err != nil {
			return err
		}
		if c.GrantID != newGrant || c.Status != core.StatusActive || c.LastError != "" || c.WarnedDays != 0 ||
			c.TokenEnc != "v1.k1.newpage" || !c.LastCheckedAt.Equal(later) || !c.NextDueAt.Equal(later.Add(24*time.Hour)) {
			t.Errorf("after repoint = %+v", c)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if live(oldGrant) != 0 || live(newGrant) != 1 {
		t.Errorf("after: old %d, new %d", live(oldGrant), live(newGrant))
	}

	t.Run("a disconnected connection is not repointed", func(t *testing.T) {
		if err := f.write(t, asA, func(tx audit.Tx) error { return connections.Disconnect(t.Context(), tx, brA1(), conn, later) }); err != nil {
			t.Fatal(err)
		}
		err := f.write(t, asA, func(tx audit.Tx) error {
			return connections.RepointConnection(t.Context(), tx, brA1(), conn, connections.Repoint{GrantID: oldGrant, Now: later})
		})
		if !errors.Is(err, connections.ErrNotFound) {
			t.Errorf("repoint after disconnect: %v", err)
		}
	})

	t.Run("another workspace cannot repoint", func(t *testing.T) {
		err := f.write(t, scope(wsB(), ownerB()), func(tx audit.Tx) error {
			return connections.RepointConnection(t.Context(), tx, brA1(), conn, connections.Repoint{GrantID: newGrant, Now: later})
		})
		if !errors.Is(err, connections.ErrNotFound) {
			t.Errorf("repoint from B: %v", err)
		}
	})
}
