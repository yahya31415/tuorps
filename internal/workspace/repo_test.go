package workspace_test

import (
	"crypto/sha256"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya31415/tuorps/internal/audit"
	auditcore "github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
	"github.com/yahya31415/tuorps/internal/workspace"
	"github.com/yahya31415/tuorps/internal/workspace/core"
)

func id(s string) uuid.UUID { return uuid.MustParse("0199c3f0-0000-7000-8000-" + s) }

// Fixture IDs.
func owner() uuid.UUID    { return id("0000000000a1") }
func member() uuid.UUID   { return id("0000000000a2") }
func outsider() uuid.UUID { return id("0000000000b1") }
func wsA() uuid.UUID      { return id("00000000000a") }
func wsB() uuid.UUID      { return id("00000000000b") }
func brandA1() uuid.UUID  { return id("00000000ba01") }
func brandA2() uuid.UUID  { return id("00000000ba02") }
func brandB1() uuid.UUID  { return id("00000000bb01") }

type fixture struct {
	pool  *pgxpool.Pool
	w     audit.Writer
	admin *pgxpool.Pool
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
	f := fixture{pool: mk(pg.AppURL), admin: mk(pg.AdminURL), now: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	f.w = audit.NewWriter(f.pool, ids.System(), clock.Fixed(f.now))
	for _, u := range []struct {
		id    uuid.UUID
		email string
	}{{owner(), "owner@example.in"}, {member(), "member@example.in"}, {outsider(), "outsider@example.in"}} {
		if _, err := f.admin.Exec(t.Context(), `INSERT INTO tuorps.users (id, email, email_normalized, display_name, email_verified_at, created_at, updated_at) VALUES ($1, $2, $2, 'U', now(), now(), now())`, u.id, u.email); err != nil {
			t.Fatal(err)
		}
	}
	f.bootstrap(t, wsA(), owner(), "Kerala Spices", brandA1())
	f.bootstrap(t, wsB(), outsider(), "Other Co", brandB1())
	return f
}

func (f fixture) event(t *testing.T, action string) auditcore.Event {
	t.Helper()
	e, err := auditcore.NewEvent(action, "workspace", "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (f fixture) write(t *testing.T, s postgres.Scope, action string, fn func(audit.Tx) error) error {
	t.Helper()
	return f.w.InWriteTx(t.Context(), s, f.event(t, action), auditcore.Meta{}, fn)
}

func (f fixture) read(t *testing.T, s postgres.Scope, fn func(pgx.Tx) error) {
	t.Helper()
	if err := postgres.InReadTx(t.Context(), f.pool, s, fn); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) bootstrap(t *testing.T, ws, user uuid.UUID, name string, brand uuid.UUID) {
	t.Helper()
	err := f.write(t, postgres.Scope{WorkspaceID: ws, UserID: user}, "workspace.create", func(tx audit.Tx) error {
		return workspace.Bootstrap(t.Context(), tx, workspace.NewWorkspace{ID: ws, Name: name, Owner: user, BrandID: brand, BrandName: name, Now: f.now})
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
}

func scope(ws, u uuid.UUID) postgres.Scope { return postgres.Scope{WorkspaceID: ws, UserID: u} }

func TestWorkspaceRepository(t *testing.T) {
	f := newFixture(t)
	asOwner := scope(wsA(), owner())

	t.Run("bootstrap makes the creator owner of the workspace and its first brand", func(t *testing.T) {
		f.read(t, asOwner, func(tx pgx.Tx) error {
			p, err := workspace.LoadPrincipal(t.Context(), tx, asOwner)
			if err != nil || !core.Can(p, core.PermWorkspaceManage, uuid.Nil()) {
				t.Errorf("principal = %+v, %v", p, err)
			}
			b, err := workspace.GetBrand(t.Context(), tx, wsA(), brandA1())
			if err != nil || b.Name != "Kerala Spices" || b.Archived {
				t.Errorf("first brand = %+v, %v", b, err)
			}
			return nil
		})
	})

	t.Run("non-members have no principal anywhere", func(t *testing.T) {
		f.read(t, scope(wsA(), outsider()), func(tx pgx.Tx) error {
			if _, err := workspace.LoadPrincipal(t.Context(), tx, scope(wsA(), outsider())); !errors.Is(err, workspace.ErrNotFound) {
				t.Errorf("outsider principal err = %v", err)
			}
			return nil
		})
	})

	t.Run("brands: create, duplicate name, rename, archive", func(t *testing.T) {
		if err := f.write(t, asOwner, "brand.create", func(tx audit.Tx) error {
			return workspace.CreateBrand(t.Context(), tx, brandA2(), "Spice Exports", f.now)
		}); err != nil {
			t.Fatal(err)
		}
		err := f.write(t, asOwner, "brand.create", func(tx audit.Tx) error {
			return workspace.CreateBrand(t.Context(), tx, id("00000000ba99"), "spice exports", f.now)
		})
		if !errors.Is(err, workspace.ErrNameTaken) {
			t.Errorf("duplicate brand name: %v", err)
		}
		if err := f.write(t, asOwner, "brand.update", func(tx audit.Tx) error {
			return workspace.RenameBrand(t.Context(), tx, brandA2(), "Spice Exports Ltd", f.now)
		}); err != nil {
			t.Fatal(err)
		}
		f.read(t, asOwner, func(tx pgx.Tx) error {
			b, _ := workspace.GetBrand(t.Context(), tx, wsA(), brandA2())
			if b.Name != "Spice Exports Ltd" {
				t.Errorf("renamed brand = %+v", b)
			}
			return nil
		})
	})

	t.Run("cross-tenant brand access is not found", func(t *testing.T) {
		f.read(t, asOwner, func(tx pgx.Tx) error {
			if _, err := workspace.GetBrand(t.Context(), tx, wsA(), brandB1()); !errors.Is(err, workspace.ErrNotFound) {
				t.Errorf("other workspace's brand: %v", err)
			}
			if _, err := workspace.GetBrand(t.Context(), tx, wsB(), brandB1()); !errors.Is(err, workspace.ErrNotFound) {
				t.Errorf("explicit other workspace id: %v", err)
			}
			return nil
		})
		err := f.write(t, asOwner, "brand.update", func(tx audit.Tx) error {
			return workspace.RenameBrand(t.Context(), tx, brandB1(), "pwned", f.now)
		})
		if !errors.Is(err, workspace.ErrNotFound) {
			t.Errorf("renaming another workspace's brand: %v", err)
		}
	})

	t.Run("members and brand roles", func(t *testing.T) {
		if _, err := f.admin.Exec(t.Context(), `INSERT INTO tuorps.workspace_members (workspace_id, user_id, is_owner, created_at) VALUES ($1, $2, false, now())`, wsA(), member()); err != nil {
			t.Fatal(err)
		}
		if err := f.write(t, asOwner, "member.role_set", func(tx audit.Tx) error {
			return workspace.SetBrandRole(t.Context(), tx, brandA2(), member(), core.RolePublisher, f.now)
		}); err != nil {
			t.Fatal(err)
		}
		if err := f.write(t, asOwner, "member.role_set", func(tx audit.Tx) error {
			return workspace.SetBrandRole(t.Context(), tx, brandA2(), member(), core.RoleAdmin, f.now)
		}); err != nil {
			t.Fatal(err)
		}
		asMember := scope(wsA(), member())
		f.read(t, asMember, func(tx pgx.Tx) error {
			p, err := workspace.LoadPrincipal(t.Context(), tx, asMember)
			if err != nil || !core.Can(p, core.PermBrandManage, brandA2()) || core.Can(p, core.PermBrandRead, brandA1()) {
				t.Errorf("member principal wrong: %v", err)
			}
			brands, err := workspace.ListBrands(t.Context(), tx, p, uuid.Nil(), 50)
			if err != nil || len(brands) != 1 || brands[0].ID != brandA2() {
				t.Errorf("member sees brands %+v, %v", brands, err)
			}
			return nil
		})
		f.read(t, asOwner, func(tx pgx.Tx) error {
			p, _ := workspace.LoadPrincipal(t.Context(), tx, asOwner)
			brands, _ := workspace.ListBrands(t.Context(), tx, p, uuid.Nil(), 50)
			if len(brands) != 2 {
				t.Errorf("owner sees %d brands", len(brands))
			}
			members, err := workspace.ListMembers(t.Context(), tx, wsA(), uuid.Nil(), 50)
			if err != nil || len(members) != 2 {
				t.Fatalf("members = %+v, %v", members, err)
			}
			for _, m := range members {
				if m.UserID == member() && !slices.Equal(m.Roles, []core.BrandRole{{Brand: brandA2(), Role: core.RoleAdmin}}) {
					t.Errorf("member roles = %+v", m.Roles)
				}
				if m.UserID == outsider() {
					t.Error("outsider listed")
				}
			}
			return nil
		})
		err := f.write(t, asOwner, "member.role_set", func(tx audit.Tx) error {
			return workspace.SetBrandRole(t.Context(), tx, brandA2(), outsider(), core.RoleViewer, f.now)
		})
		if !errors.Is(err, workspace.ErrNotFound) {
			t.Errorf("role for a non-member: %v", err)
		}
		err = f.write(t, asOwner, "member.remove", func(tx audit.Tx) error {
			return workspace.RemoveMember(t.Context(), tx, owner())
		})
		if !errors.Is(err, workspace.ErrOwner) {
			t.Errorf("removing the owner: %v", err)
		}
		if err := f.write(t, asOwner, "member.role_remove", func(tx audit.Tx) error {
			return workspace.RemoveBrandRole(t.Context(), tx, brandA2(), member())
		}); err != nil {
			t.Fatal(err)
		}
		if err := f.write(t, asOwner, "member.remove", func(tx audit.Tx) error {
			return workspace.RemoveMember(t.Context(), tx, member())
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("my workspaces lists only memberships, keyset paged", func(t *testing.T) {
		if _, err := f.admin.Exec(t.Context(), `INSERT INTO tuorps.workspace_members (workspace_id, user_id, is_owner, created_at) VALUES ($1, $2, false, now())`, wsB(), owner()); err != nil {
			t.Fatal(err)
		}
		f.read(t, postgres.Scope{UserID: owner()}, func(tx pgx.Tx) error {
			first, err := workspace.ListMyWorkspaces(t.Context(), tx, owner(), uuid.Nil(), 1)
			if err != nil || len(first) != 1 || first[0].ID != wsA() || !first[0].IsOwner {
				t.Fatalf("page 1 = %+v, %v", first, err)
			}
			second, _ := workspace.ListMyWorkspaces(t.Context(), tx, owner(), first[0].ID, 1)
			if len(second) != 1 || second[0].ID != wsB() || second[0].IsOwner {
				t.Fatalf("page 2 = %+v", second)
			}
			return nil
		})
		f.read(t, postgres.Scope{UserID: member()}, func(tx pgx.Tx) error {
			if got, _ := workspace.ListMyWorkspaces(t.Context(), tx, member(), uuid.Nil(), 50); len(got) != 0 {
				t.Errorf("removed member still lists %+v", got)
			}
			return nil
		})
	})

	t.Run("invitations: create, list, find by token, accept once, revoke", func(t *testing.T) {
		token := sha256.Sum256([]byte("invite-token"))
		inv := workspace.NewInvitation{
			ID: id("0000000001a1"), EmailNormalized: "member@example.in",
			Grants: []core.BrandRole{{Brand: brandA1(), Role: core.RoleAgent}}, InvitedBy: owner(),
			TokenHash: token, ExpiresAt: f.now.Add(7 * 24 * time.Hour), Now: f.now,
		}
		if err := f.write(t, asOwner, "invitation.create", func(tx audit.Tx) error { return workspace.CreateInvitation(t.Context(), tx, inv) }); err != nil {
			t.Fatal(err)
		}
		bad := inv
		bad.ID, bad.TokenHash = id("0000000001a2"), sha256.Sum256([]byte("other"))
		bad.Grants = []core.BrandRole{{Brand: brandB1(), Role: core.RoleViewer}}
		if err := f.write(t, asOwner, "invitation.create", func(tx audit.Tx) error { return workspace.CreateInvitation(t.Context(), tx, bad) }); !errors.Is(err, workspace.ErrNotFound) {
			t.Errorf("invitation to another workspace's brand: %v", err)
		}
		f.read(t, asOwner, func(tx pgx.Tx) error {
			list, err := workspace.ListInvitations(t.Context(), tx, wsA(), uuid.Nil(), 50)
			if err != nil || len(list) != 1 || list[0].EmailNormalized != "member@example.in" {
				t.Errorf("invitations = %+v, %v", list, err)
			}
			return nil
		})
		f.read(t, scope(wsB(), outsider()), func(tx pgx.Tx) error {
			if list, _ := workspace.ListInvitations(t.Context(), tx, wsA(), uuid.Nil(), 50); len(list) != 0 {
				t.Errorf("other workspace sees %d invitations", len(list))
			}
			return nil
		})

		var pending workspace.PendingInvitation
		f.read(t, postgres.Scope{}, func(tx pgx.Tx) error {
			var ok bool
			var err error
			pending, ok, err = workspace.FindInvitation(t.Context(), tx, token, f.now)
			if err != nil || !ok || pending.WorkspaceID != wsA() || len(pending.Grants) != 1 {
				t.Fatalf("FindInvitation = %+v %v %v", pending, ok, err)
			}
			return nil
		})
		accept := func() error {
			return f.write(t, scope(wsA(), member()), "invitation.accept", func(tx audit.Tx) error {
				return workspace.AcceptInvitation(t.Context(), tx, pending, member(), f.now)
			})
		}
		if err := accept(); err != nil {
			t.Fatalf("accept: %v", err)
		}
		if err := accept(); !errors.Is(err, workspace.ErrNotFound) {
			t.Errorf("second accept: %v", err)
		}
		f.read(t, scope(wsA(), member()), func(tx pgx.Tx) error {
			p, err := workspace.LoadPrincipal(t.Context(), tx, scope(wsA(), member()))
			if err != nil || !core.Can(p, core.PermInboxReply, brandA1()) {
				t.Errorf("after accept: %v", err)
			}
			return nil
		})

		second := inv
		second.ID, second.TokenHash = id("0000000001a3"), sha256.Sum256([]byte("revoke-me"))
		_ = f.write(t, asOwner, "invitation.create", func(tx audit.Tx) error { return workspace.CreateInvitation(t.Context(), tx, second) })
		if err := f.write(t, asOwner, "invitation.revoke", func(tx audit.Tx) error { return workspace.RevokeInvitation(t.Context(), tx, second.ID, f.now) }); err != nil {
			t.Fatal(err)
		}
		f.read(t, postgres.Scope{}, func(tx pgx.Tx) error {
			if _, ok, _ := workspace.FindInvitation(t.Context(), tx, second.TokenHash, f.now); ok {
				t.Error("revoked invitation still found")
			}
			return nil
		})
	})

	t.Run("workspace rename and archive brand", func(t *testing.T) {
		if err := f.write(t, asOwner, "workspace.update", func(tx audit.Tx) error {
			return workspace.RenameWorkspace(t.Context(), tx, "Kerala Spices Pvt Ltd", f.now)
		}); err != nil {
			t.Fatal(err)
		}
		if err := f.write(t, asOwner, "brand.archive", func(tx audit.Tx) error {
			return workspace.ArchiveBrand(t.Context(), tx, brandA2(), f.now)
		}); err != nil {
			t.Fatal(err)
		}
		f.read(t, asOwner, func(tx pgx.Tx) error {
			w, err := workspace.GetWorkspace(t.Context(), tx, wsA())
			if err != nil || w.Name != "Kerala Spices Pvt Ltd" {
				t.Errorf("workspace = %+v, %v", w, err)
			}
			b, _ := workspace.GetBrand(t.Context(), tx, wsA(), brandA2())
			if !b.Archived {
				t.Error("brand not archived")
			}
			return nil
		})
		// member() belongs to A only (the owner joined B in an earlier subtest).
		f.read(t, scope(wsA(), member()), func(tx pgx.Tx) error {
			if _, err := workspace.GetWorkspace(t.Context(), tx, wsB()); !errors.Is(err, workspace.ErrNotFound) {
				t.Errorf("non-member sees another workspace: %v", err)
			}
			return nil
		})
	})
}
