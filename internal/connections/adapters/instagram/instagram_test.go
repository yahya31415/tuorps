package instagram_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/connections/adapters/instagram"
	"github.com/yahya31415/tuorps/internal/connections/core"
)

const (
	appID     = "ig-app-123"
	appSecret = "instagram-app-secret"
	redirect  = "https://app.tuorps.test/v1/oauth/instagram/callback"
)

// fake serves both api.instagram.com and graph.instagram.com calls from
// docs/api-reference.md ("Instagram API with Instagram Login").
func fake(t *testing.T, flatMe bool) *httptest.Server {
	t.Helper()
	write := func(w http.ResponseWriter, status int, body string) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("client_secret") != appSecret || r.PostForm.Get("client_id") != appID ||
			r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != redirect {
			write(w, 400, `{"error_type":"OAuthException","code":400,"error_message":"bad client"}`)
			return
		}
		if r.URL.RawQuery != "" {
			write(w, 400, `{"error_message":"secrets must not be in the query"}`)
			return
		}
		if r.PostForm.Get("code") != "good-code" {
			write(w, 400, `{"error_type":"OAuthException","code":400,"error_message":"Invalid code"}`)
			return
		}
		write(w, 200, `{"data":[{"access_token":"IGshort","user_id":"17841400000000009","permissions":"instagram_business_basic,instagram_business_manage_comments"}]}`)
	})
	mux.HandleFunc("GET /access_token", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("grant_type") != "ig_exchange_token" || q.Get("client_secret") != appSecret || q.Get("access_token") != "IGshort" {
			write(w, 400, `{"error":{"type":"OAuthException","code":190}}`)
			return
		}
		write(w, 200, `{"access_token":"IGlong","token_type":"bearer","expires_in":5184000}`)
	})
	mux.HandleFunc("GET /refresh_access_token", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("grant_type") != "ig_refresh_token" || q.Get("access_token") != "IGlong" {
			write(w, 400, `{"error":{"type":"OAuthException","code":190}}`)
			return
		}
		write(w, 200, `{"access_token":"IGrefreshed","token_type":"bearer","expires_in":5184000}`)
	})
	mux.HandleFunc("GET /v26.0/me", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") == "IGexpired" {
			write(w, 400, `{"error":{"message":"Error validating access token","type":"OAuthException","code":190}}`)
			return
		}
		if flatMe {
			write(w, 200, `{"user_id":"17841400000000001","username":"keralaspices","id":"9900"}`)
			return
		}
		write(w, 200, `{"data":[{"user_id":"17841400000000001","username":"keralaspices"}]}`)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func adapter(t *testing.T, flatMe bool) instagram.Adapter {
	t.Helper()
	s := fake(t, flatMe)
	return instagram.New(instagram.Config{AppID: appID, AppSecret: appSecret, RedirectURI: redirect, APIURL: s.URL, GraphURL: s.URL, Timeout: 2 * time.Second})
}

func TestExchange(t *testing.T) {
	a := adapter(t, false)
	// Instagram appends "#_" to the code in the redirect; it is not part of it.
	ts, err := a.Exchange(t.Context(), "good-code#_")
	if err != nil {
		t.Fatal(err)
	}
	if ts.AccessToken() != "IGlong" || ts.ExpiresIn() != 60*24*time.Hour || ts.ProviderUserID() != "17841400000000009" {
		t.Errorf("Exchange = %q, %v, %q", ts.AccessToken(), ts.ExpiresIn(), ts.ProviderUserID())
	}
	if _, err := a.Exchange(t.Context(), "bad-code"); !errors.Is(err, core.ErrRejected) {
		t.Errorf("bad code: %v", err)
	}
}

func TestRefresh(t *testing.T) {
	ts, err := adapter(t, false).Refresh(t.Context(), "IGlong")
	if err != nil || ts.AccessToken() != "IGrefreshed" || ts.ExpiresIn() != 60*24*time.Hour {
		t.Errorf("Refresh = %q, %v, %v", ts.AccessToken(), ts.ExpiresIn(), err)
	}
}

// The account is the professional account itself, connected with the
// grant's token; /me is documented both wrapped in data[] and flat.
func TestListAccounts(t *testing.T) {
	for _, flat := range []bool{false, true} {
		accts, err := adapter(t, flat).ListAccounts(t.Context(), "IGlong")
		if err != nil || len(accts) != 1 {
			t.Fatalf("flat=%v: %v, %v", flat, accts, err)
		}
		a := accts[0]
		if a.Kind != core.KindInstagramAccount || a.ExternalID != "17841400000000001" || a.DisplayName != "keralaspices" || a.Token.Present() {
			t.Errorf("flat=%v: account = %+v", flat, a)
		}
	}
}

func TestCheck(t *testing.T) {
	a := adapter(t, false)
	if h, err := a.Check(t.Context(), "IGlong"); err != nil || !h.Valid {
		t.Errorf("valid token: %+v, %v", h, err)
	}
	if h, err := a.Check(t.Context(), "IGexpired"); err != nil || h.Valid {
		t.Errorf("expired token: %+v, %v; want Valid=false and no error", h, err)
	}
}

func TestProviderPorts(t *testing.T) {
	p := adapter(t, false).Provider()
	if p.Exchange == nil || p.Refresh == nil || p.ListAccounts == nil || p.Check == nil || p.Revoke != nil {
		t.Errorf("Instagram ports: no Revoke (none documented): %+v", p)
	}
}
