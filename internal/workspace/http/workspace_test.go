package wshttp_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/yahya31415/tuorps/internal/apitest"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
)

type workspaceJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IsOwner bool   `json:"is_owner"`
}

type brandJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}

type list[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

type grant struct {
	BrandID string `json:"brand_id"`
	Role    string `json:"role"`
}

type memberJSON struct {
	UserID  string  `json:"user_id"`
	Email   string  `json:"email"`
	IsOwner bool    `json:"is_owner"`
	Roles   []grant `json:"roles"`
}

// firstWorkspace is the workspace signup created for c.
func firstWorkspace(t *testing.T, c *apitest.Client) workspaceJSON {
	t.Helper()
	var l list[workspaceJSON]
	c.Get("/v1/workspaces").JSON(t, &l)
	if len(l.Items) == 0 {
		t.Fatal("no workspace")
	}
	return l.Items[0]
}

func brands(t *testing.T, c *apitest.Client, ws string) []brandJSON {
	t.Helper()
	var l list[brandJSON]
	r := c.Get("/v1/workspaces/" + ws + "/brands")
	if r.Status != http.StatusOK {
		t.Fatalf("list brands: %d %s", r.Status, r.Body)
	}
	r.JSON(t, &l)
	return l.Items
}

// invite sends an invitation and returns the token from its email.
func invite(t *testing.T, api *apitest.API, by *apitest.Client, ws, email string, grants ...grant) string {
	t.Helper()
	r := by.Post("/v1/workspaces/"+ws+"/invitations", map[string]any{"email": email, "grants": grants})
	if r.Status != http.StatusCreated {
		t.Fatalf("invite %s: %d %s", email, r.Status, r.Body)
	}
	m, ok := api.LastEmailTo(email, emailcore.KindInvitation)
	if !ok {
		t.Fatalf("no invitation email to %s", email)
	}
	return apitest.TokenFrom(t, m)
}

func TestWorkspaceAPI(t *testing.T) {
	api := apitest.Start(t)
	owner := api.SignUp("owner@example.in", "Kerala Spices")
	member := api.SignUp("member@example.in", "Member Co")
	outsider := api.SignUp("outsider@example.in", "Other Co")
	ws := firstWorkspace(t, owner)
	firstBrand := brands(t, owner, ws.ID)[0]
	foreign := firstWorkspace(t, outsider)
	foreignBrand := brands(t, outsider, foreign.ID)[0]
	base := "/v1/workspaces/" + ws.ID

	t.Run("my workspaces: create, list and page", func(t *testing.T) {
		r := owner.Post("/v1/workspaces", map[string]string{"name": "Second Venture"})
		var created workspaceJSON
		r.JSON(t, &created)
		if r.Status != http.StatusCreated || created.Name != "Second Venture" || !created.IsOwner {
			t.Fatalf("create = %d %s", r.Status, r.Body)
		}
		var page list[workspaceJSON]
		owner.Get("/v1/workspaces?limit=1").JSON(t, &page)
		if len(page.Items) != 1 || page.NextCursor == nil {
			t.Fatalf("page 1 = %+v", page)
		}
		var next list[workspaceJSON]
		owner.Get("/v1/workspaces?limit=1&cursor="+*page.NextCursor).JSON(t, &next)
		if len(next.Items) != 1 || next.Items[0].ID == page.Items[0].ID {
			t.Fatalf("page 2 = %+v", next)
		}
		if r := owner.Get("/v1/workspaces?cursor=!!"); r.Status != http.StatusBadRequest || r.ErrorCode(t) != "invalid_page" {
			t.Errorf("bad cursor: %d %s", r.Status, r.Body)
		}
		if r := owner.Post("/v1/workspaces", map[string]string{"name": ""}); r.Status != http.StatusUnprocessableEntity {
			t.Errorf("empty name: %d", r.Status)
		}
		if r := api.Client().Get("/v1/workspaces"); r.Status != http.StatusUnauthorized {
			t.Errorf("anonymous: %d", r.Status)
		}
	})

	t.Run("workspace read and rename: owner yes, others no, outsiders cannot even see it", func(t *testing.T) {
		if r := owner.Get(base); r.Status != http.StatusOK {
			t.Errorf("owner read: %d", r.Status)
		}
		for name, path := range map[string]string{"outsider": base, "malformed id": "/v1/workspaces/not-a-uuid", "unknown id": "/v1/workspaces/0199c3f0-0000-7000-8000-000000000000"} {
			if r := outsider.Get(path); r.Status != http.StatusNotFound || r.ErrorCode(t) != "not_found" {
				t.Errorf("%s: %d %s", name, r.Status, r.Body)
			}
		}
		if r := outsider.Patch(base, map[string]string{"name": "pwned"}); r.Status != http.StatusNotFound {
			t.Errorf("outsider rename: %d", r.Status)
		}
		if r := owner.Patch(base, map[string]string{"name": "Kerala Spices Pvt Ltd"}); r.Status != http.StatusOK {
			t.Errorf("owner rename: %d %s", r.Status, r.Body)
		}
	})

	t.Run("brands", func(t *testing.T) {
		r := owner.Post(base+"/brands", map[string]string{"name": "Spice Exports"})
		var b brandJSON
		r.JSON(t, &b)
		if r.Status != http.StatusCreated || b.Name != "Spice Exports" {
			t.Fatalf("create brand = %d %s", r.Status, r.Body)
		}
		if r := owner.Post(base+"/brands", map[string]string{"name": "spice exports"}); r.Status != http.StatusConflict || r.ErrorCode(t) != "name_taken" {
			t.Errorf("duplicate: %d %s", r.Status, r.Body)
		}
		if r := owner.Get(base + "/brands/" + foreignBrand.ID); r.Status != http.StatusNotFound {
			t.Errorf("another workspace's brand through this workspace: %d", r.Status)
		}
		if r := outsider.Get(base + "/brands/" + b.ID); r.Status != http.StatusNotFound {
			t.Errorf("outsider reads brand: %d", r.Status)
		}
		if r := owner.Patch(base+"/brands/"+b.ID, map[string]string{"name": "Spice Exports Ltd"}); r.Status != http.StatusOK {
			t.Errorf("rename: %d %s", r.Status, r.Body)
		}
		if len(brands(t, owner, ws.ID)) != 2 {
			t.Error("owner should see both brands")
		}
	})

	var adminOfFirst *apitest.Client
	t.Run("invitations: send, list, accept with the right verified email only, once", func(t *testing.T) {
		token := invite(t, api, owner, ws.ID, "member@example.in", grant{BrandID: firstBrand.ID, Role: "publisher"})
		var pending list[struct {
			Email  string  `json:"email"`
			Grants []grant `json:"grants"`
		}]
		owner.Get(base+"/invitations").JSON(t, &pending)
		if len(pending.Items) != 1 || pending.Items[0].Email != "member@example.in" {
			t.Errorf("pending = %+v", pending)
		}
		if r := outsider.Get(base + "/invitations"); r.Status != http.StatusNotFound {
			t.Errorf("outsider lists invitations: %d", r.Status)
		}
		if r := outsider.Post("/v1/invitations/accept", map[string]string{"token": token}); r.Status != http.StatusForbidden || r.ErrorCode(t) != "invitation_email_mismatch" {
			t.Errorf("wrong account accepts: %d %s", r.Status, r.Body)
		}
		r := member.Post("/v1/invitations/accept", map[string]string{"token": token})
		var accepted struct {
			WorkspaceID string `json:"workspace_id"`
		}
		r.JSON(t, &accepted)
		if r.Status != http.StatusOK || accepted.WorkspaceID != ws.ID {
			t.Fatalf("accept = %d %s", r.Status, r.Body)
		}
		if r := member.Post("/v1/invitations/accept", map[string]string{"token": token}); r.Status != http.StatusBadRequest || r.ErrorCode(t) != "invalid_token" {
			t.Errorf("second accept: %d %s", r.Status, r.Body)
		}
		got := brands(t, member, ws.ID)
		if len(got) != 1 || got[0].ID != firstBrand.ID {
			t.Errorf("member sees brands %+v, want only the granted one", got)
		}

		adminOfFirst = api.SignUp("admin@example.in", "Admin Co")
		tok := invite(t, api, owner, ws.ID, "admin@example.in", grant{BrandID: firstBrand.ID, Role: "admin"})
		if r := adminOfFirst.Post("/v1/invitations/accept", map[string]string{"token": tok}); r.Status != http.StatusOK {
			t.Fatalf("admin accept: %d %s", r.Status, r.Body)
		}
	})

	t.Run("invitation rules", func(t *testing.T) {
		spice := brands(t, owner, ws.ID)[1]
		cases := []struct {
			name string
			by   *apitest.Client
			body map[string]any
			want int
			code string
		}{
			{"admin invites to a brand they do not administer", adminOfFirst, map[string]any{"email": "x@example.in", "grants": []grant{{BrandID: spice.ID, Role: "viewer"}}}, http.StatusForbidden, "forbidden"},
			{"publisher cannot invite", member, map[string]any{"email": "x@example.in", "grants": []grant{{BrandID: firstBrand.ID, Role: "viewer"}}}, http.StatusForbidden, "forbidden"},
			{"unknown role", owner, map[string]any{"email": "x@example.in", "grants": []grant{{BrandID: firstBrand.ID, Role: "owner"}}}, http.StatusUnprocessableEntity, "validation_failed"},
			{"no grants", owner, map[string]any{"email": "x@example.in", "grants": []grant{}}, http.StatusUnprocessableEntity, "validation_failed"},
			{"brand of another workspace", owner, map[string]any{"email": "x@example.in", "grants": []grant{{BrandID: foreignBrand.ID, Role: "viewer"}}}, http.StatusUnprocessableEntity, "unknown_brand"},
			{"bad email", owner, map[string]any{"email": "nope", "grants": []grant{{BrandID: firstBrand.ID, Role: "viewer"}}}, http.StatusUnprocessableEntity, "validation_failed"},
		}
		for _, tc := range cases {
			if r := tc.by.Post(base+"/invitations", tc.body); r.Status != tc.want || r.ErrorCode(t) != tc.code {
				t.Errorf("%s: %d %s", tc.name, r.Status, r.Body)
			}
		}
		if r := adminOfFirst.Post(base+"/invitations", map[string]any{"email": "agent@example.in", "grants": []grant{{BrandID: firstBrand.ID, Role: "agent"}}}); r.Status != http.StatusCreated {
			t.Errorf("admin invites to own brand: %d %s", r.Status, r.Body)
		}
		var l list[struct{ ID string }]
		owner.Get(base+"/invitations").JSON(t, &l)
		if len(l.Items) == 0 {
			t.Fatal("no pending invitation to revoke")
		}
		if r := member.Delete(base+"/invitations/"+l.Items[0].ID, nil); r.Status != http.StatusForbidden {
			t.Errorf("publisher revokes: %d", r.Status)
		}
		if r := owner.Delete(base+"/invitations/"+l.Items[0].ID, nil); r.Status != http.StatusNoContent {
			t.Errorf("owner revokes: %d %s", r.Status, r.Body)
		}
		if r := owner.Delete(base+"/invitations/"+l.Items[0].ID, nil); r.Status != http.StatusNotFound {
			t.Errorf("revoke twice: %d", r.Status)
		}
	})

	t.Run("role-based brand permissions", func(t *testing.T) {
		if r := member.Patch(base+"/brands/"+firstBrand.ID, map[string]string{"name": "x"}); r.Status != http.StatusForbidden {
			t.Errorf("publisher renames brand: %d", r.Status)
		}
		if r := adminOfFirst.Patch(base+"/brands/"+firstBrand.ID, map[string]string{"name": "Kerala Spices Main"}); r.Status != http.StatusOK {
			t.Errorf("brand admin renames brand: %d %s", r.Status, r.Body)
		}
		spice := brands(t, owner, ws.ID)[1]
		if r := member.Get(base + "/brands/" + spice.ID); r.Status != http.StatusNotFound {
			t.Errorf("member reads a brand without a role: %d", r.Status)
		}
		if r := member.Post(base+"/brands", map[string]string{"name": "Mine"}); r.Status != http.StatusForbidden {
			t.Errorf("non-owner creates brand: %d", r.Status)
		}
		if r := adminOfFirst.Delete(base+"/brands/"+firstBrand.ID, nil); r.Status != http.StatusForbidden {
			t.Errorf("brand admin archives (owner only): %d", r.Status)
		}
		if r := owner.Delete(base+"/brands/"+spice.ID, nil); r.Status != http.StatusNoContent {
			t.Errorf("owner archives: %d %s", r.Status, r.Body)
		}
	})

	t.Run("members and roles", func(t *testing.T) {
		var l list[memberJSON]
		r := owner.Get(base + "/members")
		r.JSON(t, &l)
		if r.Status != http.StatusOK || len(l.Items) != 3 {
			t.Fatalf("members = %d %s", r.Status, r.Body)
		}
		var memberID, ownerID string
		for _, m := range l.Items {
			switch m.Email {
			case "member@example.in":
				memberID = m.UserID
				if len(m.Roles) != 1 || m.Roles[0].Role != "publisher" {
					t.Errorf("member roles = %+v", m.Roles)
				}
			case "owner@example.in":
				ownerID = m.UserID
			}
		}
		if r := member.Get(base + "/members"); r.Status != http.StatusForbidden {
			t.Errorf("publisher lists members: %d", r.Status)
		}
		if r := adminOfFirst.Get(base + "/members"); r.Status != http.StatusOK {
			t.Errorf("brand admin lists members: %d", r.Status)
		}
		role := base + "/brands/" + firstBrand.ID + "/members/" + memberID
		if r := member.Put(role, map[string]string{"role": "admin"}); r.Status != http.StatusForbidden {
			t.Errorf("self-promotion: %d", r.Status)
		}
		if r := adminOfFirst.Put(role, map[string]string{"role": "agent"}); r.Status != http.StatusNoContent {
			t.Errorf("brand admin sets role: %d %s", r.Status, r.Body)
		}
		if r := adminOfFirst.Put(role, map[string]string{"role": "root"}); r.Status != http.StatusUnprocessableEntity {
			t.Errorf("unknown role: %d", r.Status)
		}
		outsiderID := firstWorkspace(t, outsider).ID // any non-member ID is fine
		if r := owner.Put(base+"/brands/"+firstBrand.ID+"/members/"+outsiderID, map[string]string{"role": "viewer"}); r.Status != http.StatusNotFound {
			t.Errorf("role for a non-member: %d", r.Status)
		}
		if r := outsider.Put(role, map[string]string{"role": "viewer"}); r.Status != http.StatusNotFound {
			t.Errorf("outsider sets role: %d", r.Status)
		}
		if r := owner.Delete(base+"/members/"+ownerID, nil); r.Status != http.StatusConflict || r.ErrorCode(t) != "cannot_remove_owner" {
			t.Errorf("remove owner: %d %s", r.Status, r.Body)
		}
		if r := adminOfFirst.Delete(base+"/members/"+memberID, nil); r.Status != http.StatusForbidden {
			t.Errorf("admin removes member (owner only): %d", r.Status)
		}
		if r := adminOfFirst.Delete(role, nil); r.Status != http.StatusNoContent {
			t.Errorf("brand admin removes role: %d", r.Status)
		}
		if r := owner.Delete(base+"/members/"+memberID, nil); r.Status != http.StatusNoContent {
			t.Errorf("owner removes member: %d %s", r.Status, r.Body)
		}
		if r := member.Get(base); r.Status != http.StatusNotFound {
			t.Errorf("removed member still sees the workspace: %d", r.Status)
		}
	})

	t.Run("activity log: owner only, newest first, paged, tenant-isolated", func(t *testing.T) {
		var page list[struct {
			ID     string `json:"id"`
			Action string `json:"action"`
		}]
		r := owner.Get(base + "/audit-log?limit=2")
		r.JSON(t, &page)
		if r.Status != http.StatusOK || len(page.Items) != 2 || page.NextCursor == nil {
			t.Fatalf("audit page = %d %s", r.Status, r.Body)
		}
		if page.Items[0].Action != "member.remove" {
			t.Errorf("newest event = %q, want member.remove", page.Items[0].Action)
		}
		seen := map[string]bool{}
		cursor := ""
		for range 50 {
			var p list[struct{ Action string }]
			path := base + "/audit-log?limit=100"
			if cursor != "" {
				path += "&cursor=" + cursor
			}
			owner.Get(path).JSON(t, &p)
			for _, e := range p.Items {
				seen[e.Action] = true
			}
			if p.NextCursor == nil {
				break
			}
			cursor = *p.NextCursor
		}
		for _, a := range []string{"auth.signup", "workspace.update", "brand.create", "brand.update", "brand.archive", "invitation.create", "invitation.accept", "invitation.revoke", "member.role_set", "member.role_remove", "member.remove"} {
			if !seen[a] {
				t.Errorf("activity log missing %s", a)
			}
		}
		if r := adminOfFirst.Get(base + "/audit-log"); r.Status != http.StatusForbidden {
			t.Errorf("non-owner reads activity log: %d", r.Status)
		}
		if r := outsider.Get(base + "/audit-log"); r.Status != http.StatusNotFound {
			t.Errorf("outsider reads activity log: %d", r.Status)
		}
		var theirs list[struct{ Action string }]
		outsider.Get("/v1/workspaces/"+foreign.ID+"/audit-log?limit=100").JSON(t, &theirs)
		for _, e := range theirs.Items {
			if e.Action == "brand.archive" || e.Action == "member.remove" {
				t.Errorf("another workspace's log contains %s", e.Action)
			}
		}
	})

	t.Run("write routes are rate limited per user", func(t *testing.T) {
		busy := api.SignUp("busy@example.in", "Busy Co")
		w := firstWorkspace(t, busy)
		var last apitest.Response
		for i := range 121 {
			last = busy.Patch("/v1/workspaces/"+w.ID, map[string]string{"name": fmt.Sprintf("Busy %d", i)})
			if last.Status == http.StatusTooManyRequests {
				break
			}
		}
		if last.Status != http.StatusTooManyRequests {
			t.Errorf("121 writes in a minute were all accepted")
		}
		if r := busy.Get("/v1/workspaces/" + w.ID); r.Status != http.StatusOK {
			t.Errorf("reads are not write-limited: %d", r.Status)
		}
	})
}
