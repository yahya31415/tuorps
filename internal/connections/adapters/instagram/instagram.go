// Package instagram is the Instagram API with Instagram Login adapter, for
// professional accounts without a Facebook Page (docs/api-reference.md,
// "Instagram API with Instagram Login"). It uses the Instagram app ID and
// secret, not the Meta app's.
package instagram

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
	// DefaultAPIURL and DefaultGraphURL are the production hosts.
	DefaultAPIURL   = "https://api.instagram.com"
	DefaultGraphURL = "https://graph.instagram.com"
)

// Config is the Instagram app registration (Business Login settings).
type Config struct {
	AppID, AppSecret string
	RedirectURI      string
	APIURL           string // DefaultAPIURL outside tests
	GraphURL         string // DefaultGraphURL outside tests
	Timeout          time.Duration
}

// Adapter calls the Instagram API.
type Adapter struct {
	cfg  Config
	http platformhttp.Client
}

// New returns an Adapter.
func New(c Config) Adapter {
	return Adapter{cfg: c, http: platformhttp.New(core.ProviderInstagram, c.Timeout, maxBody)}
}

// Provider exposes the adapter's operations. No revoke endpoint is
// documented, so there is no Revoke.
func (a Adapter) Provider() connections.Provider {
	return connections.Provider{Exchange: a.Exchange, Refresh: a.Refresh, ListAccounts: a.ListAccounts, Check: a.Check}
}

func (a Adapter) rejected(op string) error {
	return &platformhttp.Error{Provider: core.ProviderInstagram, Op: op, Status: http.StatusOK, Code: core.ErrRejected}
}

func (a Adapter) do(ctx context.Context, method, rawURL string, form url.Values, op string) ([]byte, error) {
	var req *http.Request
	var err error
	if form != nil {
		req, err = http.NewRequestWithContext(ctx, method, rawURL, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, method, rawURL, nil)
	}
	if err != nil {
		return nil, &platformhttp.Error{Provider: core.ProviderInstagram, Op: op, Code: core.ErrRejected}
	}
	return a.http.Do(req, op)
}

func (a Adapter) tokenSet(body []byte, op string) (core.TokenSet, error) {
	ts, err := core.ParseTokenResponse(core.ProviderInstagram, body)
	if err != nil {
		return core.TokenSet{}, a.rejected(op)
	}
	return ts, nil
}

// Exchange swaps the code (without the "#_" Instagram appends) for a
// short-lived token, then for a long-lived one (60 days). The grant keeps
// the Instagram-scoped user id from the code exchange.
func (a Adapter) Exchange(ctx context.Context, code string) (core.TokenSet, error) {
	form := url.Values{"client_id": {a.cfg.AppID}, "client_secret": {a.cfg.AppSecret}, "grant_type": {"authorization_code"},
		"redirect_uri": {a.cfg.RedirectURI}, "code": {strings.TrimSuffix(code, "#_")}}
	body, err := a.do(ctx, http.MethodPost, a.cfg.APIURL+"/oauth/access_token", form, "exchange")
	if err != nil {
		return core.TokenSet{}, err
	}
	short, err := a.tokenSet(body, "exchange")
	if err != nil {
		return core.TokenSet{}, err
	}
	q := url.Values{"grant_type": {"ig_exchange_token"}, "client_secret": {a.cfg.AppSecret}, "access_token": {short.AccessToken()}}
	body, err = a.do(ctx, http.MethodGet, a.cfg.GraphURL+"/access_token?"+q.Encode(), nil, "long_lived")
	if err != nil {
		return core.TokenSet{}, err
	}
	long, err := a.tokenSet(body, "long_lived")
	if err != nil {
		return core.TokenSet{}, err
	}
	return long.WithProviderUserID(short.ProviderUserID()), nil
}

// Refresh extends a long-lived token by 60 days (it must be at least 24
// hours old and unexpired; core.Evaluate schedules it).
func (a Adapter) Refresh(ctx context.Context, token string) (core.TokenSet, error) {
	q := url.Values{"grant_type": {"ig_refresh_token"}, "access_token": {token}}
	body, err := a.do(ctx, http.MethodGet, a.cfg.GraphURL+"/refresh_access_token?"+q.Encode(), nil, "refresh")
	if err != nil {
		return core.TokenSet{}, err
	}
	return a.tokenSet(body, "refresh")
}

type meJSON struct {
	UserID   string   `json:"user_id"`
	Username string   `json:"username"`
	Data     []meJSON `json:"data"`
}

func (a Adapter) me(ctx context.Context, token string) (meJSON, error) {
	q := url.Values{"fields": {"user_id,username"}, "access_token": {token}}
	body, err := a.do(ctx, http.MethodGet, a.cfg.GraphURL+"/"+core.GraphVersion+"/me?"+q.Encode(), nil, "me")
	if err != nil {
		return meJSON{}, err
	}
	var m meJSON
	if err := json.Unmarshal(body, &m); err != nil {
		return meJSON{}, a.rejected("me")
	}
	if len(m.Data) == 1 {
		m = m.Data[0]
	}
	if m.UserID == "" {
		return meJSON{}, a.rejected("me")
	}
	return m, nil
}

// ListAccounts returns the professional account itself; it is connected
// with the grant's token. Its user_id is the id webhooks carry.
func (a Adapter) ListAccounts(ctx context.Context, token string) ([]core.Account, error) {
	m, err := a.me(ctx, token)
	if err != nil {
		return nil, err
	}
	return []core.Account{{Kind: core.KindInstagramAccount, ExternalID: m.UserID, DisplayName: m.Username}}, nil
}

// Check reports whether token still reads the account. An auth failure is
// an answer (Valid false), not an error.
func (a Adapter) Check(ctx context.Context, token string) (core.Health, error) {
	_, err := a.me(ctx, token)
	switch {
	case err == nil:
		return core.Health{Valid: true}, nil
	case errors.Is(err, core.ErrReauthRequired):
		return core.Health{Valid: false}, nil
	}
	return core.Health{}, err
}
