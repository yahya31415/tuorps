package meta_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/connections/adapters/meta"
	"github.com/yahya31415/tuorps/internal/connections/core"
)

const (
	appID     = "app-123"
	appSecret = "meta-app-secret"
	redirect  = "https://app.tuorps.test/v1/oauth/meta/callback"
)

func proof(token string) string {
	m := hmac.New(sha256.New, []byte(appSecret))
	m.Write([]byte(token))
	return hex.EncodeToString(m.Sum(nil))
}

// fakeGraph serves the Graph API calls from docs/api-reference.md
// ("Meta: Facebook Login for Business"). Every call made with a user or
// Page token must carry its appsecret_proof.
func fakeGraph(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
	needProof := func(w http.ResponseWriter, r *http.Request) bool {
		q := r.URL.Query()
		if q.Get("appsecret_proof") != proof(q.Get("access_token")) {
			write(w, 400, `{"error":{"message":"Invalid appsecret_proof","type":"GraphMethodException","code":100}}`)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /v26.0/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("client_id") != appID || q.Get("client_secret") != appSecret:
			write(w, 400, `{"error":{"type":"OAuthException","code":101}}`)
		case q.Get("code") == "good-code" && q.Get("redirect_uri") == redirect:
			write(w, 200, `{"access_token":"EAAshort","token_type":"bearer","expires_in":3600}`)
		case q.Get("grant_type") == "fb_exchange_token" && q.Get("fb_exchange_token") == "EAAshort":
			write(w, 200, `{"access_token":"EAAlong","token_type":"bearer","expires_in":5183944}`)
		default:
			write(w, 400, `{"error":{"message":"Invalid verification code format.","type":"OAuthException","code":100}}`)
		}
	})
	mux.HandleFunc("GET /v26.0/me", func(w http.ResponseWriter, r *http.Request) {
		if needProof(w, r) {
			write(w, 200, `{"id":"10228374655566"}`)
		}
	})
	mux.HandleFunc("GET /v26.0/me/accounts", func(w http.ResponseWriter, r *http.Request) {
		if !needProof(w, r) {
			return
		}
		if r.URL.Query().Get("after") == "" {
			write(w, 200, `{"data":[{"id":"p1","name":"Kerala Spices","access_token":"EAApage1","instagram_business_account":{"id":"ig1","username":"keralaspices"}}],
				"paging":{"next":"`+srv.URL+`/v26.0/me/accounts?after=c1&access_token=EAAlong&appsecret_proof=`+proof("EAAlong")+`"}}`)
			return
		}
		write(w, 200, `{"data":[{"id":"p2","name":"Kochi Store","access_token":"EAApage2"}],"paging":{}}`)
	})
	mux.HandleFunc("GET /v26.0/debug_token", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("access_token") != appID+"|"+appSecret {
			write(w, 400, `{"error":{"type":"OAuthException","code":190}}`)
			return
		}
		if q.Get("input_token") == "EAArevoked" {
			write(w, 200, `{"data":{"is_valid":false,"expires_at":0,"error":{"code":190,"subcode":460}}}`)
			return
		}
		write(w, 200, `{"data":{"app_id":"app-123","is_valid":true,"expires_at":0,"data_access_expires_at":1799000000,"scopes":["pages_show_list"]}}`)
	})
	mux.HandleFunc("DELETE /v26.0/me/permissions", func(w http.ResponseWriter, r *http.Request) {
		if needProof(w, r) {
			write(w, 200, `{"success":true}`)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func provider(t *testing.T, graph string) meta.Config {
	t.Helper()
	return meta.Config{AppID: appID, AppSecret: appSecret, RedirectURI: redirect, GraphURL: graph, Timeout: 2 * time.Second}
}

func TestExchange(t *testing.T) {
	p := meta.New(provider(t, fakeGraph(t).URL))
	ts, err := p.Exchange(t.Context(), "good-code")
	if err != nil {
		t.Fatal(err)
	}
	if ts.AccessToken() != "EAAlong" || ts.ExpiresIn() != 5183944*time.Second || ts.ProviderUserID() != "10228374655566" {
		t.Errorf("Exchange = %q, %v, %q; want the long-lived token and the user id", ts.AccessToken(), ts.ExpiresIn(), ts.ProviderUserID())
	}
	if _, err := p.Exchange(t.Context(), "bad-code"); !errors.Is(err, core.ErrRejected) {
		t.Errorf("bad code: %v", err)
	}
}

func TestListAccountsFollowsPagingOnTheGraphHost(t *testing.T) {
	p := meta.New(provider(t, fakeGraph(t).URL))
	accts, err := p.ListAccounts(t.Context(), "EAAlong")
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		kind      core.Kind
		ext, name string
		pageToken string
	}
	var got []row
	for _, a := range accts {
		got = append(got, row{a.Kind, a.ExternalID, a.DisplayName, a.Token.AccessToken()})
	}
	want := []row{
		{core.KindFacebookPage, "p1", "Kerala Spices", "EAApage1"},
		{core.KindInstagramAccount, "ig1", "keralaspices", "EAApage1"},
		{core.KindFacebookPage, "p2", "Kochi Store", "EAApage2"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("accounts = %+v\nwant %+v", got, want)
	}
}

func TestListAccountsRefusesPagingToAnotherHost(t *testing.T) {
	var visited bool
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { visited = true; _, _ = io.WriteString(w, `{"data":[]}`) }))
	defer evil.Close()
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"p1","name":"P","access_token":"EAApage1"}],"paging":{"next":"`+evil.URL+`/steal"}}`)
	}))
	defer graph.Close()
	if _, err := meta.New(provider(t, graph.URL)).ListAccounts(t.Context(), "EAAlong"); !errors.Is(err, core.ErrRejected) || visited {
		t.Errorf("err = %v, visited = %v; want a refusal without visiting the other host", err, visited)
	}
}

func TestCheck(t *testing.T) {
	p := meta.New(provider(t, fakeGraph(t).URL))
	h, err := p.Check(t.Context(), "EAApage1")
	if err != nil || !h.Valid || !h.ExpiresAt.IsZero() || !h.DataAccessExpiresAt.Equal(time.Unix(1799000000, 0).UTC()) {
		t.Errorf("Check = %+v, %v", h, err)
	}
	h, err = p.Check(t.Context(), "EAArevoked")
	if err != nil || h.Valid {
		t.Errorf("revoked token: %+v, %v", h, err)
	}
}

func TestRevoke(t *testing.T) {
	if err := meta.New(provider(t, fakeGraph(t).URL)).Revoke(t.Context(), "EAAlong"); err != nil {
		t.Fatal(err)
	}
}

func TestProviderPorts(t *testing.T) {
	p := meta.New(provider(t, "https://graph.facebook.com")).Provider()
	if p.Exchange == nil || p.ListAccounts == nil || p.Check == nil || p.Revoke == nil || p.Refresh != nil {
		t.Errorf("Meta ports: exchange, list, check and revoke; no refresh (Page tokens do not expire): %+v", p)
	}
}
