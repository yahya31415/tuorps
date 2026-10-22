package linkedin_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/connections/adapters/linkedin"
	"github.com/yahya31415/tuorps/internal/connections/core"
)

const (
	clientID     = "li-client"
	clientSecret = "li-client-secret"
	redirect     = "https://app.tuorps.test/v1/oauth/linkedin/callback"
)

// fake serves www.linkedin.com OAuth and api.linkedin.com REST calls from
// docs/api-reference.md ("LinkedIn"); REST calls must carry the pinned
// version and protocol headers.
func fake(t *testing.T) *httptest.Server {
	t.Helper()
	write := func(w http.ResponseWriter, status int, body string) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
	form := func(r *http.Request) bool {
		return r.ParseForm() == nil && r.URL.RawQuery == "" && r.PostForm.Get("client_id") == clientID && r.PostForm.Get("client_secret") == clientSecret
	}
	rest := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("LinkedIn-Version") != core.LinkedInVersion || r.Header.Get("X-Restli-Protocol-Version") != "2.0.0" || r.Header.Get("Authorization") != "Bearer AQtoken" {
			write(w, 401, `{"status":401,"message":"bad headers"}`)
			return false
		}
		return true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/v2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case !form(r):
			write(w, 401, `{"error":"invalid_client"}`)
		case r.PostForm.Get("grant_type") == "authorization_code" && r.PostForm.Get("code") == "good-code" && r.PostForm.Get("redirect_uri") == redirect:
			write(w, 200, `{"access_token":"AQtoken","expires_in":5184000,"scope":"r_organization_social,w_organization_social"}`)
		case r.PostForm.Get("grant_type") == "refresh_token" && r.PostForm.Get("refresh_token") == "AQrefresh":
			write(w, 200, `{"access_token":"AQnew","expires_in":5184000,"refresh_token":"AQrefresh","refresh_token_expires_in":31000000}`)
		default:
			write(w, 400, `{"error":"invalid_grant","error_description":"x"}`)
		}
	})
	mux.HandleFunc("POST /oauth/v2/introspectToken", func(w http.ResponseWriter, r *http.Request) {
		if !form(r) {
			write(w, 401, `{"error":"invalid_client"}`)
			return
		}
		if r.PostForm.Get("token") == "AQrevoked" {
			write(w, 200, `{"active":false,"status":"revoked"}`)
			return
		}
		write(w, 200, `{"active":true,"status":"active","client_id":"li-client","expires_at":1797000000,"scope":"r_organization_social,w_organization_social","auth_type":"3L"}`)
	})
	mux.HandleFunc("GET /rest/organizationAcls", func(w http.ResponseWriter, r *http.Request) {
		if !rest(w, r) {
			return
		}
		if r.URL.Query().Get("q") != "roleAssignee" {
			write(w, 400, `{}`)
			return
		}
		write(w, 200, `{"elements":[
			{"role":"ADMINISTRATOR","state":"APPROVED","organization":"urn:li:organization:101","roleAssignee":"urn:li:person:x"},
			{"role":"CONTENT_ADMINISTRATOR","state":"APPROVED","organization":"urn:li:organization:101"},
			{"role":"CONTENT_ADMINISTRATOR","state":"APPROVED","organizationTarget":"urn:li:organization:102"},
			{"role":"ANALYST","state":"APPROVED","organization":"urn:li:organization:103"},
			{"role":"ADMINISTRATOR","state":"REQUESTED","organization":"urn:li:organization:104"}
		],"paging":{"start":0,"count":100}}`)
	})
	mux.HandleFunc("GET /rest/organizations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !rest(w, r) {
			return
		}
		if r.PathValue("id") == "101" {
			write(w, 200, `{"id":101,"localizedName":"Kerala Spices Pvt Ltd","vanityName":"keralaspices"}`)
			return
		}
		write(w, 200, `{"id":102}`)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func adapter(t *testing.T) linkedin.Adapter {
	t.Helper()
	s := fake(t)
	return linkedin.New(linkedin.Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURI: redirect, OAuthURL: s.URL, APIURL: s.URL, Timeout: 2 * time.Second})
}

func TestExchange(t *testing.T) {
	a := adapter(t)
	ts, err := a.Exchange(t.Context(), "good-code")
	if err != nil || ts.AccessToken() != "AQtoken" || ts.ExpiresIn() != 60*24*time.Hour || ts.RefreshToken() != "" ||
		!slices.Equal(ts.Scopes(), []string{"r_organization_social", "w_organization_social"}) {
		t.Errorf("Exchange = %q %v %q %v, %v", ts.AccessToken(), ts.ExpiresIn(), ts.RefreshToken(), ts.Scopes(), err)
	}
	if _, err := a.Exchange(t.Context(), "bad-code"); !errors.Is(err, core.ErrReauthRequired) {
		t.Errorf("bad code (invalid_grant): %v", err)
	}
}

func TestRefresh(t *testing.T) {
	ts, err := adapter(t).Refresh(t.Context(), "AQrefresh")
	if err != nil || ts.AccessToken() != "AQnew" || ts.RefreshToken() != "AQrefresh" {
		t.Errorf("Refresh = %q %q, %v", ts.AccessToken(), ts.RefreshToken(), err)
	}
}

// Only approved roles that may post are connectable; an organization is
// listed once whatever its number of roles; the name falls back to the id.
func TestListAccounts(t *testing.T) {
	accts, err := adapter(t).ListAccounts(t.Context(), "AQtoken")
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		kind     core.Kind
		id, name string
		token    bool
	}
	var got []row
	for _, a := range accts {
		got = append(got, row{a.Kind, a.ExternalID, a.DisplayName, a.Token.Present()})
	}
	want := []row{{core.KindLinkedInOrg, "101", "Kerala Spices Pvt Ltd", false}, {core.KindLinkedInOrg, "102", "LinkedIn page 102", false}}
	if !slices.Equal(got, want) {
		t.Errorf("accounts = %+v\nwant %+v", got, want)
	}
}

func TestCheck(t *testing.T) {
	a := adapter(t)
	h, err := a.Check(t.Context(), "AQtoken")
	if err != nil || !h.Valid || !h.ExpiresAt.Equal(time.Unix(1797000000, 0).UTC()) {
		t.Errorf("Check = %+v, %v", h, err)
	}
	if h, err := a.Check(t.Context(), "AQrevoked"); err != nil || h.Valid {
		t.Errorf("revoked: %+v, %v", h, err)
	}
}

func TestProviderPorts(t *testing.T) {
	p := adapter(t).Provider()
	if p.Exchange == nil || p.Refresh == nil || p.ListAccounts == nil || p.Check == nil || p.Revoke != nil {
		t.Errorf("LinkedIn ports: no Revoke (none documented): %+v", p)
	}
}
