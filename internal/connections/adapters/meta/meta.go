// Package meta is the Facebook Login for Business adapter: Facebook Pages
// and the Instagram accounts linked to them (docs/api-reference.md, "Meta:
// Facebook Login for Business"). Every call with a user or Page token
// carries appsecret_proof.
package meta

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/url"
	"time"

	"github.com/yahya31415/tuorps/internal/connections"
	"github.com/yahya31415/tuorps/internal/connections/adapters/platformhttp"
	"github.com/yahya31415/tuorps/internal/connections/core"
)

const (
	maxBody  = 1 << 20
	maxPages = 20
	// DefaultGraphURL is the production Graph API host.
	DefaultGraphURL = "https://graph.facebook.com"
)

// Config is the Meta app registration.
type Config struct {
	AppID, AppSecret string
	RedirectURI      string
	GraphURL         string // DefaultGraphURL outside tests
	Timeout          time.Duration
}

// Adapter calls the Graph API.
type Adapter struct {
	cfg  Config
	http platformhttp.Client
}

// New returns an Adapter.
func New(c Config) Adapter {
	return Adapter{cfg: c, http: platformhttp.New(core.ProviderMeta, c.Timeout, maxBody)}
}

// Provider exposes the adapter's operations. Page tokens do not expire, so
// there is no Refresh.
func (a Adapter) Provider() connections.Provider {
	return connections.Provider{Exchange: a.Exchange, ListAccounts: a.ListAccounts, Check: a.Check, Revoke: a.Revoke}
}

func (a Adapter) endpoint(path string, q url.Values) string {
	return a.cfg.GraphURL + "/" + core.GraphVersion + path + "?" + q.Encode()
}

// withToken adds the access token and its appsecret_proof.
func (a Adapter) withToken(q url.Values, token string) url.Values {
	q.Set("access_token", token)
	q.Set("appsecret_proof", core.AppSecretProof(token, []byte(a.cfg.AppSecret)))
	return q
}

func (a Adapter) call(ctx context.Context, method, rawURL, op string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return &platformhttp.Error{Provider: core.ProviderMeta, Op: op, Code: core.ErrRejected}
	}
	body, err := a.http.Do(req, op)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &platformhttp.Error{Provider: core.ProviderMeta, Op: op, Status: http.StatusOK, Code: core.ErrRejected}
	}
	return nil
}

func (a Adapter) token(ctx context.Context, q url.Values, op string) (core.TokenSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint("/oauth/access_token", q), nil)
	if err != nil {
		return core.TokenSet{}, &platformhttp.Error{Provider: core.ProviderMeta, Op: op, Code: core.ErrRejected}
	}
	body, err := a.http.Do(req, op)
	if err != nil {
		return core.TokenSet{}, err
	}
	ts, err := core.ParseTokenResponse(core.ProviderMeta, body)
	if err != nil {
		return core.TokenSet{}, &platformhttp.Error{Provider: core.ProviderMeta, Op: op, Status: http.StatusOK, Code: core.ErrRejected}
	}
	return ts, nil
}

// Exchange swaps the code for a short-lived user token, that for a
// long-lived one (about 60 days), and looks up the app-scoped user id.
func (a Adapter) Exchange(ctx context.Context, code string) (core.TokenSet, error) {
	short, err := a.token(ctx, url.Values{"client_id": {a.cfg.AppID}, "redirect_uri": {a.cfg.RedirectURI}, "client_secret": {a.cfg.AppSecret}, "code": {code}}, "exchange")
	if err != nil {
		return core.TokenSet{}, err
	}
	long, err := a.token(ctx, url.Values{"grant_type": {"fb_exchange_token"}, "client_id": {a.cfg.AppID}, "client_secret": {a.cfg.AppSecret}, "fb_exchange_token": {short.AccessToken()}}, "long_lived")
	if err != nil {
		return core.TokenSet{}, err
	}
	var me struct {
		ID string `json:"id"`
	}
	if err := a.call(ctx, http.MethodGet, a.endpoint("/me", a.withToken(url.Values{"fields": {"id"}}, long.AccessToken())), "me", &me); err != nil {
		return core.TokenSet{}, err
	}
	if me.ID == "" {
		return core.TokenSet{}, &platformhttp.Error{Provider: core.ProviderMeta, Op: "me", Status: http.StatusOK, Code: core.ErrRejected}
	}
	return long.WithProviderUserID(me.ID), nil
}

// ListAccounts lists the Pages the user manages, each with its Page token,
// and the Instagram professional account linked to each Page (which uses
// that Page's token). Paging links are followed only on the Graph host.
func (a Adapter) ListAccounts(ctx context.Context, userToken string) ([]core.Account, error) {
	next := a.endpoint("/me/accounts", a.withToken(url.Values{"fields": {"id,name,access_token,instagram_business_account{id,username}"}, "limit": {"100"}}, userToken))
	var out []core.Account
	for range maxPages {
		var page struct {
			Data []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				AccessToken string `json:"access_token"`
				Instagram   *struct {
					ID       string `json:"id"`
					Username string `json:"username"`
				} `json:"instagram_business_account"`
			} `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := a.call(ctx, http.MethodGet, next, "accounts", &page); err != nil {
			return nil, err
		}
		for _, p := range page.Data {
			token := core.BearerToken(p.AccessToken, 0)
			out = append(out, core.Account{Kind: core.KindFacebookPage, ExternalID: p.ID, DisplayName: p.Name, Token: token})
			if p.Instagram != nil && p.Instagram.ID != "" {
				out = append(out, core.Account{Kind: core.KindInstagramAccount, ExternalID: p.Instagram.ID, DisplayName: p.Instagram.Username, Token: token})
			}
		}
		if page.Paging.Next == "" {
			return out, nil
		}
		if !a.onGraphHost(page.Paging.Next) {
			return nil, &platformhttp.Error{Provider: core.ProviderMeta, Op: "accounts", Status: http.StatusOK, Code: core.ErrRejected}
		}
		next = page.Paging.Next
	}
	return nil, &platformhttp.Error{Provider: core.ProviderMeta, Op: "accounts", Status: http.StatusOK, Code: core.ErrRejected}
}

func (a Adapter) onGraphHost(raw string) bool {
	u, err := url.Parse(raw)
	base, berr := url.Parse(a.cfg.GraphURL)
	return err == nil && berr == nil && u.Scheme == base.Scheme && u.Host == base.Host
}

// Check asks debug_token (with the app token) whether token is valid, when
// it expires (zero: never) and when its data access lapses.
func (a Adapter) Check(ctx context.Context, token string) (core.Health, error) {
	var r struct {
		Data struct {
			IsValid             bool  `json:"is_valid"`
			ExpiresAt           int64 `json:"expires_at"`
			DataAccessExpiresAt int64 `json:"data_access_expires_at"`
		} `json:"data"`
	}
	q := url.Values{"input_token": {token}, "access_token": {a.cfg.AppID + "|" + a.cfg.AppSecret}}
	if err := a.call(ctx, http.MethodGet, a.endpoint("/debug_token", q), "check", &r); err != nil {
		return core.Health{}, err
	}
	return core.Health{Valid: r.Data.IsValid, ExpiresAt: unix(r.Data.ExpiresAt), DataAccessExpiresAt: unix(r.Data.DataAccessExpiresAt)}, nil
}

// Revoke removes all the app's permissions for the user.
func (a Adapter) Revoke(ctx context.Context, userToken string) error {
	var r struct {
		Success bool `json:"success"`
	}
	if err := a.call(ctx, http.MethodDelete, a.endpoint("/me/permissions", a.withToken(url.Values{}, userToken)), "revoke", &r); err != nil {
		return err
	}
	if !r.Success {
		return &platformhttp.Error{Provider: core.ProviderMeta, Op: "revoke", Status: http.StatusOK, Code: core.ErrRejected}
	}
	return nil
}

func unix(s int64) time.Time {
	if s <= 0 {
		return time.Time{}
	}
	return time.Unix(s, 0).UTC()
}
