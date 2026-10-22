package connhttp_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/apitest"
	"github.com/yahya31415/tuorps/internal/connections"
	"github.com/yahya31415/tuorps/internal/connections/core"
	connhttp "github.com/yahya31415/tuorps/internal/connections/http"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
)

type list[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

type idJSON struct {
	ID string `json:"id"`
}

type accountJSON struct {
	Kind        string `json:"kind"`
	ExternalID  string `json:"external_id"`
	DisplayName string `json:"display_name"`
	Connected   bool   `json:"connected"`
}

type connectionJSON struct {
	ID          string `json:"id"`
	BrandID     string `json:"brand_id"`
	Provider    string `json:"provider"`
	Kind        string `json:"kind"`
	ExternalID  string `json:"external_id"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	Expiring    bool   `json:"expiring"`
}

func token(t *testing.T, p core.Provider, body string) core.TokenSet {
	t.Helper()
	ts, err := core.ParseTokenResponse(p, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// fakePlatforms stands in for Meta and LinkedIn; Google is not configured.
// The fakes insist on the tokens the real flow would hand them, so a test
// passes only if the vault opened the right ciphertext.
func fakePlatforms(t *testing.T) map[core.Provider]connhttp.Platform {
	t.Helper()
	metaUser := token(t, core.ProviderMeta, `{"access_token":"EAAuser","token_type":"bearer","expires_in":5184000}`).WithProviderUserID("10228374655566")
	page1, page2 := core.BearerToken("EAApage1", 0), core.BearerToken("EAApage2", 0)
	exchange := func(want string, ts core.TokenSet) func(context.Context, string) (core.TokenSet, error) {
		return func(_ context.Context, code string) (core.TokenSet, error) {
			if code != want {
				return core.TokenSet{}, core.ErrRejected
			}
			return ts, nil
		}
	}
	return map[core.Provider]connhttp.Platform{
		core.ProviderMeta: {
			Client: core.OAuthClient{ClientID: "meta-app", RedirectURI: "https://app.tuorps.test/v1/oauth/meta/callback", ConfigID: "cfg-1"},
			Ports: connections.Provider{
				Exchange: exchange("good-code", metaUser),
				ListAccounts: func(_ context.Context, tok string) ([]core.Account, error) {
					if tok != "EAAuser" {
						return nil, core.ErrReauthRequired
					}
					return []core.Account{
						{Kind: core.KindFacebookPage, ExternalID: "p1", DisplayName: "Kerala Spices", Token: page1},
						{Kind: core.KindInstagramAccount, ExternalID: "ig1", DisplayName: "keralaspices", Token: page1},
						{Kind: core.KindFacebookPage, ExternalID: "p2", DisplayName: "Kochi Store", Token: page2},
					}, nil
				},
				Check: func(context.Context, string) (core.Health, error) {
					return core.Health{Valid: true, DataAccessExpiresAt: time.Now().Add(90 * 24 * time.Hour)}, nil
				},
			},
		},
		core.ProviderLinkedIn: {
			Client: core.OAuthClient{ClientID: "li-app", RedirectURI: "https://app.tuorps.test/v1/oauth/linkedin/callback"},
			Ports: connections.Provider{
				Exchange: exchange("good-code", token(t, core.ProviderLinkedIn, `{"access_token":"AQuser","expires_in":864000,"scope":"r_organization_social"}`)),
				ListAccounts: func(_ context.Context, tok string) ([]core.Account, error) {
					if tok != "AQuser" {
						return nil, core.ErrReauthRequired
					}
					return []core.Account{{Kind: core.KindLinkedInOrg, ExternalID: "101", DisplayName: "Kerala Spices Pvt Ltd"}}, nil
				},
				Check: func(context.Context, string) (core.Health, error) { return core.Health{Valid: true}, nil },
			},
		},
	}
}

type ids struct{ ws, brand string }

func firstIDs(t *testing.T, c *apitest.Client) ids {
	t.Helper()
	var wss list[idJSON]
	c.Get("/v1/workspaces").JSON(t, &wss)
	var brands list[idJSON]
	c.Get("/v1/workspaces/"+wss.Items[0].ID+"/brands").JSON(t, &brands)
	return ids{wss.Items[0].ID, brands.Items[0].ID}
}

// authorize starts a flow and returns its state.
func authorize(t *testing.T, c *apitest.Client, base, provider string) string {
	t.Helper()
	r := c.Post(base+"/connections/authorize", map[string]string{"provider": provider})
	if r.Status != http.StatusOK {
		t.Fatalf("authorize %s: %d %s", provider, r.Status, r.Body)
	}
	var out struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	r.JSON(t, &out)
	u, err := url.Parse(out.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

// callback follows the provider's redirect back and returns where Tuorps
// sends the browser next.
func callback(t *testing.T, c *apitest.Client, provider, query string) *url.URL {
	t.Helper()
	r := c.Get("/v1/oauth/" + provider + "/callback?" + query)
	if r.Status != http.StatusSeeOther {
		t.Fatalf("callback: %d %s", r.Status, r.Body)
	}
	u, err := url.Parse(r.Header.Get("Location"))
	if err != nil || u.Host != "app.tuorps.test" {
		t.Fatalf("callback redirect %q", r.Header.Get("Location"))
	}
	return u
}

func fragment(u *url.URL) url.Values { v, _ := url.ParseQuery(u.Fragment); return v }

func errorReason(t *testing.T, u *url.URL) string {
	t.Helper()
	if u.Path != "/connections/error" {
		t.Fatalf("redirect %s, want the error page", u)
	}
	return fragment(u).Get("reason")
}

func TestConnectionsAPI(t *testing.T) {
	api := apitest.Start(t, apitest.WithPlatforms(fakePlatforms(t)))
	owner := api.SignUp("owner@example.in", "Kerala Spices")
	outsider := api.SignUp("outsider@example.in", "Other Co")
	a, b := firstIDs(t, owner), firstIDs(t, outsider)
	base := "/v1/workspaces/" + a.ws + "/brands/" + a.brand
	foreign := "/v1/workspaces/" + b.ws + "/brands/" + b.brand

	// A viewer of brand A, invited by the owner.
	viewer := api.SignUp("viewer@example.in", "Viewer Co")
	if r := owner.Post("/v1/workspaces/"+a.ws+"/invitations", map[string]any{"email": "viewer@example.in", "grants": []map[string]string{{"brand_id": a.brand, "role": "viewer"}}}); r.Status != http.StatusCreated {
		t.Fatalf("invite: %d %s", r.Status, r.Body)
	}
	inv, _ := api.LastEmailTo("viewer@example.in", emailcore.KindInvitation)
	if r := viewer.Post("/v1/invitations/accept", map[string]string{"token": apitest.TokenFrom(t, inv)}); r.Status >= 300 {
		t.Fatalf("accept: %d %s", r.Status, r.Body)
	}

	var grantID, pageConn string

	t.Run("authorize returns the provider's consent URL", func(t *testing.T) {
		r := owner.Post(base+"/connections/authorize", map[string]string{"provider": "meta"})
		var out struct {
			AuthorizeURL string `json:"authorize_url"`
		}
		r.JSON(t, &out)
		u, err := url.Parse(out.AuthorizeURL)
		if r.Status != http.StatusOK || err != nil || u.Host != "www.facebook.com" || u.Query().Get("config_id") != "cfg-1" ||
			u.Query().Get("redirect_uri") != "https://app.tuorps.test/v1/oauth/meta/callback" || len(u.Query().Get("state")) < 43 {
			t.Fatalf("authorize = %d %s", r.Status, r.Body)
		}
	})

	t.Run("callback stores a sealed grant and sends the browser to account selection", func(t *testing.T) {
		state := authorize(t, owner, base, "meta")
		u := callback(t, owner, "meta", "code=good-code&state="+url.QueryEscape(state))
		if u.Path != "/connections/select" || fragment(u).Get("brand") != a.brand || fragment(u).Get("workspace") != a.ws {
			t.Fatalf("redirect = %s", u)
		}
		grantID = fragment(u).Get("grant")
		if n := api.Count(`SELECT count(*) FROM tuorps.oauth_grants WHERE id = $1 AND provider = 'meta' AND provider_user_id = '10228374655566'
			AND access_token_enc LIKE 'v1.k1.%' AND position('EAAuser' in access_token_enc) = 0 AND data_access_expires_at IS NOT NULL`, grantID); n != 1 {
			t.Errorf("sealed grant rows = %d", n)
		}
		if u := callback(t, owner, "meta", "code=good-code&state="+url.QueryEscape(state)); errorReason(t, u) != "invalid_state" {
			t.Errorf("replayed state accepted: %s", u)
		}
	})

	t.Run("callback failures all end on the error page", func(t *testing.T) {
		cases := map[string]struct {
			c        *apitest.Client
			provider string
			query    func() string
			reason   string
		}{
			"user denied consent": {owner, "meta", func() string {
				return "error=access_denied&state=" + url.QueryEscape(authorize(t, owner, base, "meta"))
			}, "denied"},
			"bad code":         {owner, "meta", func() string { return "code=bad-code&state=" + url.QueryEscape(authorize(t, owner, base, "meta")) }, "exchange_failed"},
			"unknown state":    {owner, "meta", func() string { return "code=good-code&state=forged" }, "invalid_state"},
			"provider mix-up":  {owner, "linkedin", func() string { return "code=good-code&state=" + url.QueryEscape(authorize(t, owner, base, "meta")) }, "invalid_state"},
			"another user":     {outsider, "meta", func() string { return "code=good-code&state=" + url.QueryEscape(authorize(t, owner, base, "meta")) }, "invalid_state"},
			"signed out":       {api.Client(), "meta", func() string { return "code=good-code&state=" + url.QueryEscape(authorize(t, owner, base, "meta")) }, "signed_out"},
			"unknown provider": {owner, "myspace", func() string { return "code=good-code&state=x" }, "invalid_state"},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				if got := errorReason(t, callback(t, tc.c, tc.provider, tc.query())); got != tc.reason {
					t.Errorf("reason = %q, want %q", got, tc.reason)
				}
			})
		}
	})

	t.Run("a second session of the same user cannot finish the flow", func(t *testing.T) {
		state := authorize(t, owner, base, "meta")
		other := api.Client()
		if r := other.Post("/v1/auth/login", map[string]string{"email": "owner@example.in", "password": apitest.Password}); r.Status != http.StatusOK {
			t.Fatalf("login: %d", r.Status)
		}
		if got := errorReason(t, callback(t, other, "meta", "code=good-code&state="+url.QueryEscape(state))); got != "invalid_state" {
			t.Errorf("reason = %q", got)
		}
	})

	t.Run("candidate accounts are listed for the user who granted them", func(t *testing.T) {
		var l list[accountJSON]
		r := owner.Get(base + "/grants/" + grantID + "/accounts")
		r.JSON(t, &l)
		if r.Status != http.StatusOK || len(l.Items) != 3 || l.Items[0].ExternalID != "p1" || l.Items[1].Kind != "instagram_account" {
			t.Fatalf("accounts = %d %s", r.Status, r.Body)
		}
		if r := viewer.Get(base + "/grants/" + grantID + "/accounts"); r.Status != http.StatusNotFound {
			t.Errorf("another member listing the owner's grant: %d", r.Status)
		}
	})

	t.Run("connect chosen accounts", func(t *testing.T) {
		r := owner.Post(base+"/connections", map[string]any{"grant_id": grantID, "external_ids": []string{"p1", "ig1"}})
		var l list[connectionJSON]
		r.JSON(t, &l)
		if r.Status != http.StatusCreated || len(l.Items) != 2 || l.Items[0].Status != "active" || l.Items[0].Provider != "meta" || l.Items[0].BrandID != a.brand {
			t.Fatalf("connect = %d %s", r.Status, r.Body)
		}
		pageConn = l.Items[0].ID
		if n := api.Count(`SELECT count(*) FROM tuorps.connections WHERE id = $1 AND token_enc LIKE 'v1.k1.%' AND position('EAApage1' in token_enc) = 0 AND next_due_at IS NOT NULL`, pageConn); n != 1 {
			t.Errorf("page token not sealed or not scheduled")
		}
		var accts list[accountJSON]
		owner.Get(base+"/grants/"+grantID+"/accounts").JSON(t, &accts)
		if !accts.Items[0].Connected || accts.Items[2].Connected {
			t.Errorf("connected flags = %+v", accts.Items)
		}
	})

	t.Run("connect refuses duplicates and accounts the grant did not offer", func(t *testing.T) {
		if r := owner.Post(base+"/connections", map[string]any{"grant_id": grantID, "external_ids": []string{"p1"}}); r.Status != http.StatusConflict || r.ErrorCode(t) != "already_connected" {
			t.Errorf("duplicate: %d %s", r.Status, r.Body)
		}
		if r := owner.Post(base+"/connections", map[string]any{"grant_id": grantID, "external_ids": []string{"p2", "zz-not-offered"}}); r.Status != http.StatusUnprocessableEntity || r.ErrorCode(t) != "unknown_account" {
			t.Errorf("forged id: %d %s", r.Status, r.Body)
		}
		if n := api.Count(`SELECT count(*) FROM tuorps.connections WHERE external_id = 'p2'`); n != 0 {
			t.Errorf("a refused request still connected p2")
		}
		for name, body := range map[string]any{
			"no ids":   map[string]any{"grant_id": grantID, "external_ids": []string{}},
			"bad id":   map[string]any{"grant_id": "nope", "external_ids": []string{"p2"}},
			"too many": map[string]any{"grant_id": grantID, "external_ids": strings.Split(strings.Repeat("x,", 51), ",")},
		} {
			if r := owner.Post(base+"/connections", body); r.Status != http.StatusUnprocessableEntity {
				t.Errorf("%s: %d %s", name, r.Status, r.Body)
			}
		}
	})

	t.Run("list and get", func(t *testing.T) {
		var l list[connectionJSON]
		r := viewer.Get(base + "/connections")
		r.JSON(t, &l)
		if r.Status != http.StatusOK || len(l.Items) != 2 {
			t.Fatalf("list as viewer = %d %s", r.Status, r.Body)
		}
		var c connectionJSON
		r = owner.Get(base + "/connections/" + pageConn)
		r.JSON(t, &c)
		if r.Status != http.StatusOK || c.ExternalID != "p1" || c.DisplayName != "Kerala Spices" || c.Kind != "facebook_page" || c.Expiring {
			t.Errorf("get = %d %s", r.Status, r.Body)
		}
	})

	t.Run("LinkedIn: 10-day token reads as expiring; missing scopes are recorded", func(t *testing.T) {
		state := authorize(t, owner, base, "linkedin")
		u := callback(t, owner, "linkedin", "code=good-code&state="+url.QueryEscape(state))
		g := fragment(u).Get("grant")
		if n := api.Count(`SELECT count(*) FROM tuorps.oauth_grants WHERE id = $1 AND cardinality(missing_scopes) = 3`, g); n != 1 {
			t.Errorf("missing scopes not recorded")
		}
		var l list[connectionJSON]
		r := owner.Post(base+"/connections", map[string]any{"grant_id": g, "external_ids": []string{"101"}})
		r.JSON(t, &l)
		if r.Status != http.StatusCreated || len(l.Items) != 1 || !l.Items[0].Expiring {
			t.Errorf("linkedin connect = %d %s", r.Status, r.Body)
		}
	})

	t.Run("reconnect moves the connection to a fresh grant", func(t *testing.T) {
		if _, err := api.Admin.Exec(context.Background(), `UPDATE tuorps.connections SET status = 'needs_reauth', last_error = 'reauth_required' WHERE id = $1`, pageConn); err != nil {
			t.Fatal(err)
		}
		r := owner.Post(base+"/connections/"+pageConn+"/reconnect", nil)
		var out struct {
			AuthorizeURL string `json:"authorize_url"`
		}
		r.JSON(t, &out)
		u, _ := url.Parse(out.AuthorizeURL)
		if r.Status != http.StatusOK || u.Host != "www.facebook.com" {
			t.Fatalf("reconnect = %d %s", r.Status, r.Body)
		}
		done := callback(t, owner, "meta", "code=good-code&state="+url.QueryEscape(u.Query().Get("state")))
		if done.Path != "/connections/done" || fragment(done).Get("connection") != pageConn {
			t.Fatalf("redirect = %s", done)
		}
		var c connectionJSON
		owner.Get(base+"/connections/"+pageConn).JSON(t, &c)
		if c.Status != "active" {
			t.Errorf("after reconnect status = %s", c.Status)
		}
		if n := api.Count(`SELECT count(*) FROM tuorps.connections WHERE id = $1 AND grant_id <> $2`, pageConn, grantID); n != 1 {
			t.Errorf("connection still on the old grant")
		}
	})

	t.Run("disconnect", func(t *testing.T) {
		if r := owner.Delete(base+"/connections/"+pageConn, nil); r.Status != http.StatusNoContent {
			t.Fatalf("disconnect = %d %s", r.Status, r.Body)
		}
		var c connectionJSON
		owner.Get(base+"/connections/"+pageConn).JSON(t, &c)
		if c.Status != "disconnected" {
			t.Errorf("status = %s", c.Status)
		}
		if n := api.Count(`SELECT count(*) FROM tuorps.connections WHERE id = $1 AND token_enc IS NULL`, pageConn); n != 1 {
			t.Error("token not wiped")
		}
		if r := owner.Delete(base+"/connections/"+pageConn, nil); r.Status != http.StatusNotFound {
			t.Errorf("second disconnect = %d", r.Status)
		}
	})

	t.Run("validation and configuration", func(t *testing.T) {
		if r := owner.Post(base+"/connections/authorize", map[string]string{"provider": "google"}); r.Status != http.StatusUnprocessableEntity || r.ErrorCode(t) != "provider_not_configured" {
			t.Errorf("unconfigured: %d %s", r.Status, r.Body)
		}
		if r := owner.Post(base+"/connections/authorize", map[string]string{"provider": "myspace"}); r.Status != http.StatusUnprocessableEntity || r.ErrorCode(t) != "validation_failed" {
			t.Errorf("unknown provider: %d %s", r.Status, r.Body)
		}
	})

	t.Run("roles: a viewer reads but cannot connect or disconnect", func(t *testing.T) {
		if r := viewer.Post(base+"/connections/authorize", map[string]string{"provider": "meta"}); r.Status != http.StatusForbidden {
			t.Errorf("authorize as viewer: %d", r.Status)
		}
		if r := viewer.Post(base+"/connections", map[string]any{"grant_id": grantID, "external_ids": []string{"p2"}}); r.Status != http.StatusForbidden {
			t.Errorf("connect as viewer: %d", r.Status)
		}
		var l list[connectionJSON]
		owner.Get(base+"/connections").JSON(t, &l)
		if r := viewer.Delete(base+"/connections/"+l.Items[1].ID, nil); r.Status != http.StatusForbidden {
			t.Errorf("disconnect as viewer: %d", r.Status)
		}
		if r := viewer.Post(base+"/connections/"+l.Items[1].ID+"/reconnect", nil); r.Status != http.StatusForbidden {
			t.Errorf("reconnect as viewer: %d", r.Status)
		}
	})

	t.Run("no session: 401", func(t *testing.T) {
		anon := api.Client()
		for _, r := range []apitest.Response{
			anon.Get(base + "/connections"),
			anon.Post(base+"/connections/authorize", map[string]string{"provider": "meta"}),
			anon.Get(base + "/grants/" + grantID + "/accounts"),
		} {
			if r.Status != http.StatusUnauthorized {
				t.Errorf("anonymous: %d", r.Status)
			}
		}
	})

	t.Run("cross-tenant: 404 everywhere", func(t *testing.T) {
		for _, r := range []apitest.Response{
			outsider.Get(base + "/connections"),
			outsider.Get(base + "/connections/" + pageConn),
			outsider.Post(base+"/connections/authorize", map[string]string{"provider": "meta"}),
			outsider.Get(base + "/grants/" + grantID + "/accounts"),
			outsider.Post(base+"/connections", map[string]any{"grant_id": grantID, "external_ids": []string{"p2"}}),
			outsider.Delete(base+"/connections/"+pageConn, nil),
			outsider.Post(base+"/connections/"+pageConn+"/reconnect", nil),
			owner.Get(foreign + "/connections"),
			owner.Get("/v1/workspaces/" + a.ws + "/brands/" + b.brand + "/connections"),
		} {
			if r.Status != http.StatusNotFound {
				t.Errorf("cross-tenant: %d %s", r.Status, r.Body)
			}
		}
	})

	t.Run("archived brands are not found", func(t *testing.T) {
		var nb idJSON
		owner.Post("/v1/workspaces/"+a.ws+"/brands", map[string]string{"name": "Old Brand"}).JSON(t, &nb)
		if r := owner.Delete("/v1/workspaces/"+a.ws+"/brands/"+nb.ID, nil); r.Status != http.StatusNoContent {
			t.Fatalf("archive: %d", r.Status)
		}
		if r := owner.Post("/v1/workspaces/"+a.ws+"/brands/"+nb.ID+"/connections/authorize", map[string]string{"provider": "meta"}); r.Status != http.StatusNotFound {
			t.Errorf("authorize on archived brand: %d", r.Status)
		}
	})

	t.Run("audited", func(t *testing.T) {
		for _, action := range []string{"connection.grant", "connection.create", "connection.reconnect", "connection.disconnect"} {
			if n := api.Count(`SELECT count(*) FROM tuorps.audit_log WHERE workspace_id = $1 AND action = $2`, a.ws, action); n == 0 {
				t.Errorf("no %s audit row", action)
			}
			if n := api.Count(`SELECT count(*) FROM tuorps.audit_log WHERE workspace_id = $1 AND action = $2`, b.ws, action); n != 0 {
				t.Errorf("%s audited in the other workspace", action)
			}
		}
		if n := api.Count(`SELECT count(*) FROM tuorps.audit_log WHERE details::text LIKE '%EAA%' OR details::text LIKE '%good-code%'`); n != 0 {
			t.Errorf("tokens or codes in the audit log")
		}
	})

}
