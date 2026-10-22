// Package google is the Google OAuth and YouTube Data API adapter for
// YouTube channels (docs/api-reference.md, "Google (YouTube)").
package google

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
	DefaultOAuthURL = "https://oauth2.googleapis.com"
	DefaultAPIURL   = "https://www.googleapis.com"
)

// Config is the Google OAuth client registration.
type Config struct {
	ClientID, ClientSecret string
	RedirectURI            string
	OAuthURL               string // DefaultOAuthURL outside tests
	APIURL                 string // DefaultAPIURL outside tests
	Timeout                time.Duration
}

// Adapter calls Google.
type Adapter struct {
	cfg  Config
	http platformhttp.Client
}

// New returns an Adapter.
func New(c Config) Adapter {
	return Adapter{cfg: c, http: platformhttp.New(core.ProviderGoogle, c.Timeout, maxBody)}
}

// Provider exposes the adapter's operations.
func (a Adapter) Provider() connections.Provider {
	return connections.Provider{Exchange: a.Exchange, Refresh: a.Refresh, ListAccounts: a.ListAccounts, Check: a.Check, Revoke: a.Revoke}
}

func (a Adapter) postForm(ctx context.Context, path string, form url.Values, op string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.OAuthURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, &platformhttp.Error{Provider: core.ProviderGoogle, Op: op, Code: core.ErrRejected}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return a.http.Do(req, op)
}

func (a Adapter) token(ctx context.Context, form url.Values, op string) (core.TokenSet, error) {
	form.Set("client_id", a.cfg.ClientID)
	form.Set("client_secret", a.cfg.ClientSecret)
	body, err := a.postForm(ctx, "/token", form, op)
	if err != nil {
		return core.TokenSet{}, err
	}
	ts, err := core.ParseTokenResponse(core.ProviderGoogle, body)
	if err != nil {
		return core.TokenSet{}, &platformhttp.Error{Provider: core.ProviderGoogle, Op: op, Status: http.StatusOK, Code: core.ErrRejected}
	}
	return ts, nil
}

// Exchange swaps the code for an access token and (offline access) a
// refresh token. The granted scopes may be a subset of those requested.
func (a Adapter) Exchange(ctx context.Context, code string) (core.TokenSet, error) {
	return a.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {a.cfg.RedirectURI}}, "exchange")
}

// Refresh gets a new access token; the refresh token is usually not
// returned again, and invalid_grant means the grant is gone.
func (a Adapter) Refresh(ctx context.Context, refreshToken string) (core.TokenSet, error) {
	return a.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}, "refresh")
}

type channelList struct {
	Items []struct {
		ID      string `json:"id"`
		Snippet struct {
			Title string `json:"title"`
		} `json:"snippet"`
	} `json:"items"`
}

func (a Adapter) myChannels(ctx context.Context, accessToken, op string) (channelList, error) {
	q := url.Values{"part": {"id,snippet"}, "mine": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.APIURL+"/youtube/v3/channels?"+q.Encode(), nil)
	if err != nil {
		return channelList{}, &platformhttp.Error{Provider: core.ProviderGoogle, Op: op, Code: core.ErrRejected}
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	body, err := a.http.Do(req, op)
	if err != nil {
		return channelList{}, err
	}
	var l channelList
	if json.Unmarshal(body, &l) != nil {
		return channelList{}, &platformhttp.Error{Provider: core.ProviderGoogle, Op: op, Status: http.StatusOK, Code: core.ErrRejected}
	}
	return l, nil
}

// ListAccounts lists the channels the user owns (1 quota unit); one Google
// account may own several.
func (a Adapter) ListAccounts(ctx context.Context, accessToken string) ([]core.Account, error) {
	l, err := a.myChannels(ctx, accessToken, "accounts")
	if err != nil {
		return nil, err
	}
	out := make([]core.Account, 0, len(l.Items))
	for _, c := range l.Items {
		if c.ID != "" {
			out = append(out, core.Account{Kind: core.KindYouTubeChannel, ExternalID: c.ID, DisplayName: c.Snippet.Title})
		}
	}
	return out, nil
}

// Check reads the user's channels with accessToken (1 unit). An auth
// failure is an answer (Valid false), not an error.
func (a Adapter) Check(ctx context.Context, accessToken string) (core.Health, error) {
	_, err := a.myChannels(ctx, accessToken, "check")
	switch {
	case err == nil:
		return core.Health{Valid: true}, nil
	case errors.Is(err, core.ErrReauthRequired):
		return core.Health{Valid: false}, nil
	}
	return core.Health{}, err
}

// Revoke withdraws every scope granted to the project for this user.
func (a Adapter) Revoke(ctx context.Context, token string) error {
	_, err := a.postForm(ctx, "/revoke", url.Values{"token": {token}}, "revoke")
	return err
}
