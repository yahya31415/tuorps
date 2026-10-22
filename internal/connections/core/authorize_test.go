package core_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/yahya31415/tuorps/internal/connections/core"
)

const testState = "St4te_with-url+safe/chars="

func client(p core.Provider) core.OAuthClient {
	c := core.OAuthClient{ClientID: "client-123", RedirectURI: "https://app.tuorps.test/v1/oauth/" + string(p) + "/callback"}
	if p == core.ProviderMeta {
		c.ConfigID = "cfg-789"
	}
	return c
}

func authorize(t *testing.T, p core.Provider) (*url.URL, url.Values) {
	t.Helper()
	raw, err := core.AuthorizeURL(p, client(p), testState)
	if err != nil {
		t.Fatalf("AuthorizeURL(%s): %v", p, err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("unparseable URL %q: %v", raw, err)
	}
	return u, u.Query()
}

// Endpoints and parameters per docs/api-reference.md, "OAuth, tokens and
// webhook security".
func TestAuthorizeURL(t *testing.T) {
	cases := []struct {
		p        core.Provider
		endpoint string
		want     map[string]string
	}{
		{core.ProviderMeta, "https://www.facebook.com/v26.0/dialog/oauth", map[string]string{
			"client_id": "client-123", "redirect_uri": "https://app.tuorps.test/v1/oauth/meta/callback",
			"response_type": "code", "config_id": "cfg-789", "state": testState,
		}},
		{core.ProviderInstagram, "https://www.instagram.com/oauth/authorize", map[string]string{
			"client_id": "client-123", "redirect_uri": "https://app.tuorps.test/v1/oauth/instagram/callback",
			"response_type": "code", "state": testState,
			"scope": "instagram_business_basic,instagram_business_content_publish,instagram_business_manage_comments,instagram_business_manage_messages,instagram_business_manage_insights",
		}},
		{core.ProviderLinkedIn, "https://www.linkedin.com/oauth/v2/authorization", map[string]string{
			"client_id": "client-123", "redirect_uri": "https://app.tuorps.test/v1/oauth/linkedin/callback",
			"response_type": "code", "state": testState,
			"scope": "w_organization_social r_organization_social rw_organization_admin r_organization_admin",
		}},
		{core.ProviderGoogle, "https://accounts.google.com/o/oauth2/v2/auth", map[string]string{
			"client_id": "client-123", "redirect_uri": "https://app.tuorps.test/v1/oauth/google/callback",
			"response_type": "code", "state": testState, "access_type": "offline", "prompt": "consent",
			"include_granted_scopes": "true",
			"scope": "https://www.googleapis.com/auth/youtube.upload https://www.googleapis.com/auth/youtube.force-ssl https://www.googleapis.com/auth/youtube.readonly https://www.googleapis.com/auth/yt-analytics.readonly",
		}},
	}
	for _, tc := range cases {
		t.Run(string(tc.p), func(t *testing.T) {
			u, q := authorize(t, tc.p)
			if got := u.Scheme + "://" + u.Host + u.Path; got != tc.endpoint {
				t.Errorf("endpoint = %s, want %s", got, tc.endpoint)
			}
			if len(q) != len(tc.want) {
				t.Errorf("params = %v, want exactly %v", q, tc.want)
			}
			for k, v := range tc.want {
				if q.Get(k) != v {
					t.Errorf("%s = %q, want %q", k, q.Get(k), v)
				}
			}
			if strings.Contains(u.RawQuery, "+") {
				t.Errorf("query %q encodes spaces as '+'; LinkedIn documents %%20", u.RawQuery)
			}
		})
	}
}

// Meta defines permissions in the login configuration, so its requested
// scopes are empty; the others list what they put in the URL.
func TestRequestedScopes(t *testing.T) {
	if s := core.RequestedScopes(core.ProviderMeta); len(s) != 0 {
		t.Errorf("Meta scopes = %v, want none (config_id)", s)
	}
	for _, p := range []core.Provider{core.ProviderInstagram, core.ProviderLinkedIn, core.ProviderGoogle} {
		_, q := authorize(t, p)
		sep := " "
		if p == core.ProviderInstagram {
			sep = ","
		}
		if got, want := q.Get("scope"), strings.Join(core.RequestedScopes(p), sep); got != want {
			t.Errorf("%s: URL scope %q, RequestedScopes %q", p, got, want)
		}
	}
}

func TestAuthorizeURLRejectsBadInput(t *testing.T) {
	ok := client(core.ProviderMeta)
	cases := map[string]struct {
		p     core.Provider
		c     core.OAuthClient
		state string
	}{
		"unknown provider":    {"x", ok, testState},
		"empty state":         {core.ProviderMeta, ok, ""},
		"no client id":        {core.ProviderMeta, core.OAuthClient{RedirectURI: ok.RedirectURI, ConfigID: "c"}, testState},
		"meta without config": {core.ProviderMeta, core.OAuthClient{ClientID: "c", RedirectURI: ok.RedirectURI}, testState},
		"relative redirect":   {core.ProviderGoogle, core.OAuthClient{ClientID: "c", RedirectURI: "/v1/oauth/google/callback"}, testState},
		"redirect fragment":   {core.ProviderGoogle, core.OAuthClient{ClientID: "c", RedirectURI: "https://a.test/cb#x"}, testState},
		"non-http redirect":   {core.ProviderGoogle, core.OAuthClient{ClientID: "c", RedirectURI: "javascript:alert(1)"}, testState},
	}
	for name, tc := range cases {
		if _, err := core.AuthorizeURL(tc.p, tc.c, tc.state); !errors.Is(err, core.ErrInvalidOAuthClient) && !errors.Is(err, core.ErrInvalidProvider) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
