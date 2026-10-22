package core_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/quick"
	"uuid"

	"github.com/yahya31415/tuorps/internal/workspace/core"
)

func id(s string) uuid.UUID { return uuid.MustParse("0199c3f0-0000-7000-8000-" + s) }

func ws() uuid.UUID     { return id("00000000000a") }
func brandA() uuid.UUID { return id("00000000ba01") }
func brandB() uuid.UUID { return id("00000000ba02") }
func user() uuid.UUID   { return id("0000000000a1") }

func principal(t *testing.T, owner bool, roles map[uuid.UUID]core.Role) core.Principal {
	t.Helper()
	p, err := core.NewPrincipal(user(), ws(), owner, roles)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseRole(t *testing.T) {
	for _, s := range []string{"admin", "publisher", "agent", "viewer"} {
		if r, err := core.ParseRole(s); err != nil || string(r) != s {
			t.Errorf("ParseRole(%q) = %q, %v", s, r, err)
		}
	}
	for _, s := range []string{"owner", "Admin", "", "superuser"} {
		if _, err := core.ParseRole(s); !errors.Is(err, core.ErrInvalidRole) {
			t.Errorf("ParseRole(%q) err = %v", s, err)
		}
	}
}

func TestCanMatrix(t *testing.T) {
	type check struct {
		perm  core.Permission
		brand uuid.UUID
		want  bool
	}
	cases := map[string]struct {
		p      func(t *testing.T) core.Principal
		checks []check
	}{
		"owner can do everything everywhere in the workspace": {
			func(t *testing.T) core.Principal { return principal(t, true, nil) },
			[]check{{core.PermWorkspaceManage, uuid.Nil(), true}, {core.PermBrandCreate, uuid.Nil(), true}, {core.PermAuditRead, uuid.Nil(), true},
				{core.PermPostPublish, brandB(), true}, {core.PermBrandMembersManage, brandB(), true}},
		},
		"brand admin manages only that brand": {
			func(t *testing.T) core.Principal {
				return principal(t, false, map[uuid.UUID]core.Role{brandA(): core.RoleAdmin})
			},
			[]check{{core.PermBrandManage, brandA(), true}, {core.PermBrandMembersManage, brandA(), true}, {core.PermPostPublish, brandA(), true},
				{core.PermInboxReply, brandA(), true}, {core.PermBrandManage, brandB(), false}, {core.PermBrandRead, brandB(), false},
				{core.PermBrandCreate, uuid.Nil(), false}, {core.PermWorkspaceManage, uuid.Nil(), false}, {core.PermAuditRead, uuid.Nil(), false}},
		},
		"publisher publishes but does not manage or answer the inbox": {
			func(t *testing.T) core.Principal {
				return principal(t, false, map[uuid.UUID]core.Role{brandA(): core.RolePublisher})
			},
			[]check{{core.PermPostCreate, brandA(), true}, {core.PermPostPublish, brandA(), true}, {core.PermAnalyticsRead, brandA(), true},
				{core.PermInboxReply, brandA(), false}, {core.PermBrandManage, brandA(), false}, {core.PermBrandMembersManage, brandA(), false}},
		},
		"agent answers the inbox but does not publish": {
			func(t *testing.T) core.Principal {
				return principal(t, false, map[uuid.UUID]core.Role{brandA(): core.RoleAgent})
			},
			[]check{{core.PermInboxRead, brandA(), true}, {core.PermInboxReply, brandA(), true}, {core.PermPostPublish, brandA(), false}, {core.PermPostCreate, brandA(), false}},
		},
		"viewer only reads": {
			func(t *testing.T) core.Principal {
				return principal(t, false, map[uuid.UUID]core.Role{brandA(): core.RoleViewer})
			},
			[]check{{core.PermBrandRead, brandA(), true}, {core.PermAnalyticsRead, brandA(), true}, {core.PermPostCreate, brandA(), false},
				{core.PermInboxReply, brandA(), false}, {core.PermInboxRead, brandA(), false}},
		},
		"a member with no brand roles can do nothing": {
			func(t *testing.T) core.Principal { return principal(t, false, nil) },
			[]check{{core.PermBrandRead, brandA(), false}, {core.PermBrandCreate, uuid.Nil(), false}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := tc.p(t)
			for _, c := range tc.checks {
				if got := core.Can(p, c.perm, c.brand); got != c.want {
					t.Errorf("Can(%s, %v) = %v, want %v", c.perm, c.brand, got, c.want)
				}
			}
		})
	}
}

// Every member of a brand may see its connected accounts; only brand admins
// (and the owner) may connect, reconnect or disconnect them (ADR 0008).
func TestConnectionPermissions(t *testing.T) {
	for _, tc := range []struct {
		role         core.Role
		read, manage bool
	}{
		{core.RoleViewer, true, false},
		{core.RoleAgent, true, false},
		{core.RolePublisher, true, false},
		{core.RoleAdmin, true, true},
	} {
		p := principal(t, false, map[uuid.UUID]core.Role{brandA(): tc.role})
		if got := core.Can(p, core.PermConnectionRead, brandA()); got != tc.read {
			t.Errorf("%s: read = %v, want %v", tc.role, got, tc.read)
		}
		if got := core.Can(p, core.PermConnectionManage, brandA()); got != tc.manage {
			t.Errorf("%s: manage = %v, want %v", tc.role, got, tc.manage)
		}
		if core.Can(p, core.PermConnectionRead, brandB()) || core.Can(p, core.PermConnectionManage, brandB()) {
			t.Errorf("%s of brand A has connection rights on brand B", tc.role)
		}
	}
	if owner := principal(t, true, nil); !core.Can(owner, core.PermConnectionManage, brandB()) {
		t.Error("owner cannot manage connections")
	}
	all := core.AllPermissions()
	for _, perm := range []core.Permission{core.PermConnectionRead, core.PermConnectionManage} {
		if !slices.Contains(all, perm) {
			t.Errorf("AllPermissions() misses %s, so the property tests skip it", perm)
		}
	}
}

func TestPrincipalIsImmutable(t *testing.T) {
	roles := map[uuid.UUID]core.Role{brandA(): core.RoleViewer}
	p := principal(t, false, roles)
	roles[brandA()] = core.RoleAdmin
	if core.Can(p, core.PermBrandManage, brandA()) {
		t.Fatal("principal shares the caller's role map")
	}
	if _, err := core.NewPrincipal(uuid.Nil(), ws(), false, nil); err == nil {
		t.Error("principal without a user")
	}
	if _, err := core.NewPrincipal(user(), uuid.Nil(), false, nil); err == nil {
		t.Error("principal without a workspace")
	}
	if _, err := core.NewPrincipal(user(), ws(), false, map[uuid.UUID]core.Role{brandA(): "root"}); !errors.Is(err, core.ErrInvalidRole) {
		t.Errorf("unknown role accepted: %v", err)
	}
}

// Property: permissions nest. Whatever a viewer may do on a brand, any
// role may do; whatever any role may do, the owner may do; a member of
// another brand can do nothing on this one.
func TestPropertyRoleOrdering(t *testing.T) {
	roles := []core.Role{core.RoleViewer, core.RoleAgent, core.RolePublisher, core.RoleAdmin}
	perms := core.AllPermissions()
	prop := func(ri, pi uint8) bool {
		r := roles[int(ri)%len(roles)]
		perm := perms[int(pi)%len(perms)]
		member, _ := core.NewPrincipal(user(), ws(), false, map[uuid.UUID]core.Role{brandA(): r})
		viewer, _ := core.NewPrincipal(user(), ws(), false, map[uuid.UUID]core.Role{brandA(): core.RoleViewer})
		owner, _ := core.NewPrincipal(user(), ws(), true, nil)
		if core.Can(viewer, perm, brandA()) && !core.Can(member, perm, brandA()) {
			return false
		}
		if core.Can(member, perm, brandA()) && !core.Can(owner, perm, brandA()) {
			return false
		}
		return !core.Can(member, perm, brandB())
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Error(err)
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"Kerala Spices", "ശ്രീ സ്റ്റോഴ്സ്", "A"} {
		if got, err := core.ValidateName("  " + ok + " "); err != nil || got != ok {
			t.Errorf("ValidateName(%q) = %q, %v", ok, got, err)
		}
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 101), "tab\tname", "line\nbreak", "\xff"} {
		if _, err := core.ValidateName(bad); !errors.Is(err, core.ErrInvalidName) {
			t.Errorf("ValidateName(%q) err = %v", bad, err)
		}
	}
}

func TestNewInvitation(t *testing.T) {
	owner := principal(t, true, nil)
	adminA := principal(t, false, map[uuid.UUID]core.Role{brandA(): core.RoleAdmin})
	publisherA := principal(t, false, map[uuid.UUID]core.Role{brandA(): core.RolePublisher})
	grants := func(pairs ...any) []core.BrandRole {
		var out []core.BrandRole
		for i := 0; i < len(pairs); i += 2 {
			out = append(out, core.BrandRole{Brand: pairs[i].(uuid.UUID), Role: pairs[i+1].(core.Role)})
		}
		return out
	}
	cases := []struct {
		name   string
		by     core.Principal
		grants []core.BrandRole
		want   error
	}{
		{"owner invites to any brand", owner, grants(brandA(), core.RoleAdmin, brandB(), core.RoleViewer), nil},
		{"brand admin invites to their brand", adminA, grants(brandA(), core.RoleAgent), nil},
		{"brand admin cannot invite to another brand", adminA, grants(brandB(), core.RoleViewer), core.ErrForbidden},
		{"publisher cannot invite", publisherA, grants(brandA(), core.RoleViewer), core.ErrForbidden},
		{"at least one brand", owner, nil, core.ErrInvalidInvitation},
		{"no duplicate brands", owner, grants(brandA(), core.RoleViewer, brandA(), core.RoleAdmin), core.ErrInvalidInvitation},
		{"no unknown roles", owner, grants(brandA(), core.Role("owner")), core.ErrInvalidRole},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := core.CheckInvitation(tc.by, tc.grants); !errors.Is(err, tc.want) {
				t.Fatalf("CheckInvitation = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCheckAcceptance(t *testing.T) {
	cases := []struct {
		name               string
		invited, userEmail string
		verified           bool
		want               error
	}{
		{"matching verified email", "new@example.in", "new@example.in", true, nil},
		{"unverified email", "new@example.in", "new@example.in", false, core.ErrEmailMismatch},
		{"different email", "new@example.in", "other@example.in", true, core.ErrEmailMismatch},
	}
	for _, tc := range cases {
		if err := core.CheckAcceptance(tc.invited, tc.userEmail, tc.verified); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
}

func BenchmarkCan(b *testing.B) {
	p, _ := core.NewPrincipal(user(), ws(), false, map[uuid.UUID]core.Role{brandA(): core.RolePublisher, brandB(): core.RoleViewer})
	b.ReportAllocs()
	for b.Loop() {
		_ = core.Can(p, core.PermPostPublish, brandA())
	}
}

func TestPrincipalAccessors(t *testing.T) {
	p := principal(t, false, map[uuid.UUID]core.Role{brandA(): core.RoleAgent})
	if p.UserID() != user() || p.WorkspaceID() != ws() || p.IsOwner() {
		t.Fatalf("accessors = %v %v %v", p.UserID(), p.WorkspaceID(), p.IsOwner())
	}
	if r, ok := p.BrandRole(brandA()); !ok || r != core.RoleAgent {
		t.Errorf("BrandRole(A) = %v %v", r, ok)
	}
	if _, ok := p.BrandRole(brandB()); ok {
		t.Error("BrandRole(B) present")
	}
}

func TestCanOnAnyBrand(t *testing.T) {
	cases := []struct {
		name  string
		owner bool
		roles map[uuid.UUID]core.Role
		want  bool
	}{
		{"owner without roles", true, nil, true},
		{"admin of one brand", false, map[uuid.UUID]core.Role{brandA(): core.RoleViewer, brandB(): core.RoleAdmin}, true},
		{"publisher everywhere", false, map[uuid.UUID]core.Role{brandA(): core.RolePublisher, brandB(): core.RolePublisher}, false},
		{"no roles", false, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := core.CanOnAnyBrand(principal(t, c.owner, c.roles), core.PermBrandMembersManage); got != c.want {
				t.Errorf("CanOnAnyBrand = %v, want %v", got, c.want)
			}
		})
	}
}
