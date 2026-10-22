package google_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/connections/adapters/google"
	"github.com/yahya31415/tuorps/internal/connections/core"
)

const (
	clientID     = "g-client.apps.googleusercontent.com"
	clientSecret = "g-client-secret"
	redirect     = "https://app.tuorps.test/v1/oauth/google/callback"
)

// fake serves oauth2.googleapis.com and the YouTube Data API calls from
// docs/api-reference.md ("Google (YouTube)").
func fake(t *testing.T, revoked *bool) *httptest.Server {
	t.Helper()
	write := func(w http.ResponseWriter, status int, body string) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if r.ParseForm() != nil || r.URL.RawQuery != "" || r.PostForm.Get("client_id") != clientID || r.PostForm.Get("client_secret") != clientSecret {
			write(w, 401, `{"error":"invalid_client"}`)
			return
		}
		switch {
		case r.PostForm.Get("grant_type") == "authorization_code" && r.PostForm.Get("code") == "good-code" && r.PostForm.Get("redirect_uri") == redirect:
			write(w, 200, `{"access_token":"ya29.first","expires_in":3920,"refresh_token":"1//refresh","scope":"https://www.googleapis.com/auth/youtube.readonly https://www.googleapis.com/auth/youtube.upload","token_type":"Bearer"}`)
		case r.PostForm.Get("grant_type") == "refresh_token" && r.PostForm.Get("refresh_token") == "1//refresh":
			write(w, 200, `{"access_token":"ya29.second","expires_in":3599,"scope":"https://www.googleapis.com/auth/youtube.readonly","token_type":"Bearer"}`)
		default:
			write(w, 400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
		}
	})
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		if r.ParseForm() != nil || r.PostForm.Get("token") != "1//refresh" {
			write(w, 400, `{"error":"invalid_token"}`)
			return
		}
		*revoked = true
		write(w, 200, ``)
	})
	mux.HandleFunc("GET /youtube/v3/channels", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Header.Get("Authorization") != "Bearer ya29.first" {
			write(w, 401, `{"error":{"code":401,"message":"Request had invalid authentication credentials."}}`)
			return
		}
		if q.Get("mine") != "true" || q.Get("part") != "id,snippet" {
			write(w, 400, `{}`)
			return
		}
		write(w, 200, `{"kind":"youtube#channelListResponse","items":[
			{"id":"UC111","snippet":{"title":"Kerala Spices"}},
			{"id":"UC222","snippet":{"title":"Kerala Spices Recipes"}}]}`)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func adapter(t *testing.T, revoked *bool) google.Adapter {
	t.Helper()
	s := fake(t, revoked)
	return google.New(google.Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURI: redirect, OAuthURL: s.URL, APIURL: s.URL, Timeout: 2 * time.Second})
}

func TestExchange(t *testing.T) {
	a := adapter(t, new(bool))
	ts, err := a.Exchange(t.Context(), "good-code")
	if err != nil || ts.AccessToken() != "ya29.first" || ts.RefreshToken() != "1//refresh" || ts.ExpiresIn() != 3920*time.Second ||
		!slices.Equal(ts.Scopes(), []string{"https://www.googleapis.com/auth/youtube.readonly", "https://www.googleapis.com/auth/youtube.upload"}) {
		t.Errorf("Exchange = %q %q %v %v, %v", ts.AccessToken(), ts.RefreshToken(), ts.ExpiresIn(), ts.Scopes(), err)
	}
	if _, err := a.Exchange(t.Context(), "used-code"); !errors.Is(err, core.ErrReauthRequired) {
		t.Errorf("invalid_grant: %v", err)
	}
}

func TestRefresh(t *testing.T) {
	a := adapter(t, new(bool))
	ts, err := a.Refresh(t.Context(), "1//refresh")
	if err != nil || ts.AccessToken() != "ya29.second" || ts.ExpiresIn() != 3599*time.Second {
		t.Errorf("Refresh = %q %v, %v", ts.AccessToken(), ts.ExpiresIn(), err)
	}
	if _, err := a.Refresh(t.Context(), "1//revoked"); !errors.Is(err, core.ErrReauthRequired) {
		t.Errorf("revoked refresh token: %v, want reauth_required", err)
	}
}

// One Google account can own several channels (Brand Accounts).
func TestListAccounts(t *testing.T) {
	accts, err := adapter(t, new(bool)).ListAccounts(t.Context(), "ya29.first")
	if err != nil || len(accts) != 2 {
		t.Fatalf("ListAccounts = %v, %v", accts, err)
	}
	for i, want := range [][2]string{{"UC111", "Kerala Spices"}, {"UC222", "Kerala Spices Recipes"}} {
		if a := accts[i]; a.Kind != core.KindYouTubeChannel || a.ExternalID != want[0] || a.DisplayName != want[1] || a.Token.Present() {
			t.Errorf("account %d = %+v", i, a)
		}
	}
}

func TestCheck(t *testing.T) {
	a := adapter(t, new(bool))
	if h, err := a.Check(t.Context(), "ya29.first"); err != nil || !h.Valid {
		t.Errorf("valid: %+v, %v", h, err)
	}
	if h, err := a.Check(t.Context(), "ya29.stale"); err != nil || h.Valid {
		t.Errorf("rejected token: %+v, %v; want Valid=false and no error", h, err)
	}
}

func TestRevoke(t *testing.T) {
	var revoked bool
	if err := adapter(t, &revoked).Revoke(t.Context(), "1//refresh"); err != nil || !revoked {
		t.Errorf("Revoke: %v, revoked = %v", err, revoked)
	}
}

func TestProviderPorts(t *testing.T) {
	p := adapter(t, new(bool)).Provider()
	if p.Exchange == nil || p.Refresh == nil || p.ListAccounts == nil || p.Check == nil || p.Revoke == nil {
		t.Errorf("Google has every port: %+v", p)
	}
}
