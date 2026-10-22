// Package linkedin is the LinkedIn Community Management adapter for
// company pages (docs/api-reference.md, "LinkedIn"). REST calls carry the
// pinned LinkedIn-Version header.
package linkedin

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yahya31415/tuorps/internal/connections"
	"github.com/yahya31415/tuorps/internal/connections/adapters/platformhttp"
	"github.com/yahya31415/tuorps/internal/connections/core"
)

const (
	maxBody = 1 << 20
	// DefaultOAuthURL and DefaultAPIURL are the production hosts.
	DefaultOAuthURL = "https://www.linkedin.com"
	DefaultAPIURL   = "https://api.linkedin.com"
	orgURNPrefix    = "urn:li:organization:"
)

// Config is the LinkedIn app registration.
type Config struct {
	ClientID, ClientSecret string
	RedirectURI            string
	OAuthURL               string // DefaultOAuthURL outside tests
	APIURL                 string // DefaultAPIURL outside tests
	Timeout                time.Duration
}

// Adapter calls LinkedIn.
type Adapter struct {
	cfg  Config
	http platformhttp.Client
}

// New returns an Adapter.
func New(c Config) Adapter {
	return Adapter{cfg: c, http: platformhttp.New(core.ProviderLinkedIn, c.Timeout, maxBody)}
}

// Provider exposes the adapter's operations. No revoke endpoint is
// documented, so there is no Revoke; disconnect wipes Tuorps's copy.
func (a Adapter) Provider() connections.Provider {
	return connections.Provider{Exchange: a.Exchange, Refresh: a.Refresh, ListAccounts: a.ListAccounts, Check: a.Check}
}

func (a Adapter) rejected(op string) error {
	return &platformhttp.Error{Provider: core.ProviderLinkedIn, Op: op, Status: http.StatusOK, Code: core.ErrRejected}
}

func (a Adapter) postForm(ctx context.Context, path string, form url.Values, op string) ([]byte, error) {
	form.Set("client_id", a.cfg.ClientID)
	form.Set("client_secret", a.cfg.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.OAuthURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, &platformhttp.Error{Provider: core.ProviderLinkedIn, Op: op, Code: core.ErrRejected}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return a.http.Do(req, op)
}

func (a Adapter) rest(ctx context.Context, token, pathAndQuery, op string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.APIURL+"/rest"+pathAndQuery, nil)
	if err != nil {
		return &platformhttp.Error{Provider: core.ProviderLinkedIn, Op: op, Code: core.ErrRejected}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("LinkedIn-Version", core.LinkedInVersion)
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	body, err := a.http.Do(req, op)
	if err != nil {
		return err
	}
	if json.Unmarshal(body, out) != nil {
		return a.rejected(op)
	}
	return nil
}

func (a Adapter) tokens(body []byte, op string) (core.TokenSet, error) {
	ts, err := core.ParseTokenResponse(core.ProviderLinkedIn, body)
	if err != nil {
		return core.TokenSet{}, a.rejected(op)
	}
	return ts, nil
}

// Exchange swaps the code for a 60-day access token (and a refresh token
// for approved partners only).
func (a Adapter) Exchange(ctx context.Context, code string) (core.TokenSet, error) {
	body, err := a.postForm(ctx, "/oauth/v2/accessToken", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {a.cfg.RedirectURI}}, "exchange")
	if err != nil {
		return core.TokenSet{}, err
	}
	return a.tokens(body, "exchange")
}

// Refresh uses a programmatic refresh token (approved partners only).
func (a Adapter) Refresh(ctx context.Context, refreshToken string) (core.TokenSet, error) {
	body, err := a.postForm(ctx, "/oauth/v2/accessToken", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}, "refresh")
	if err != nil {
		return core.TokenSet{}, err
	}
	return a.tokens(body, "refresh")
}

// ListAccounts lists the organizations the member may post for: approved
// ADMINISTRATOR, CONTENT_ADMINISTRATOR or DIRECT_SPONSORED_CONTENT_POSTER
// roles, once per organization. The sample responses name the organization
// field both "organization" and "organizationTarget", so both are read.
func (a Adapter) ListAccounts(ctx context.Context, token string) ([]core.Account, error) {
	var acls struct {
		Elements []struct {
			Role               string `json:"role"`
			State              string `json:"state"`
			Organization       string `json:"organization"`
			OrganizationTarget string `json:"organizationTarget"`
		} `json:"elements"`
	}
	if err := a.rest(ctx, token, "/organizationAcls?q=roleAssignee&count=100", "accounts", &acls); err != nil {
		return nil, err
	}
	var out []core.Account
	seen := map[string]bool{}
	for _, e := range acls.Elements {
		urn := e.Organization
		if urn == "" {
			urn = e.OrganizationTarget
		}
		orgID, ok := strings.CutPrefix(urn, orgURNPrefix)
		if !ok || orgID == "" || e.State != "APPROVED" || !mayPost(e.Role) || seen[orgID] {
			continue
		}
		seen[orgID] = true
		name, err := a.orgName(ctx, token, orgID)
		if err != nil {
			return nil, err
		}
		out = append(out, core.Account{Kind: core.KindLinkedInOrg, ExternalID: orgID, DisplayName: name})
	}
	return out, nil
}

// mayPost reports whether an organization role may post as the
// organization (w_organization_social).
func mayPost(role string) bool {
	switch role {
	case "ADMINISTRATOR", "CONTENT_ADMINISTRATOR", "DIRECT_SPONSORED_CONTENT_POSTER":
		return true
	}
	return false
}

// orgName reads the organization's localized name, falling back to its id
// when the lookup has none (the field is not in docs/api-reference.md).
// Only an authorization failure is an error.
func (a Adapter) orgName(ctx context.Context, token, orgID string) (string, error) {
	var org struct {
		LocalizedName string `json:"localizedName"`
	}
	err := a.rest(ctx, token, "/organizations/"+url.PathEscape(orgID), "organization", &org)
	if errors.Is(err, core.ErrReauthRequired) {
		return "", err
	}
	if err != nil || org.LocalizedName == "" {
		return "LinkedIn page " + orgID, nil
	}
	return org.LocalizedName, nil
}

// Check introspects the token: valid while active, with its expiry.
func (a Adapter) Check(ctx context.Context, token string) (core.Health, error) {
	body, err := a.postForm(ctx, "/oauth/v2/introspectToken", url.Values{"token": {token}}, "check")
	if err != nil {
		return core.Health{}, err
	}
	var r struct {
		Active    bool   `json:"active"`
		Status    string `json:"status"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if json.Unmarshal(body, &r) != nil {
		return core.Health{}, a.rejected("check")
	}
	h := core.Health{Valid: r.Active && r.Status == "active"}
	if r.ExpiresAt > 0 {
		h.ExpiresAt = time.Unix(r.ExpiresAt, 0).UTC()
	}
	return h, nil
}
