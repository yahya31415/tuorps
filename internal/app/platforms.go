package app

import (
	"time"

	"github.com/yahya31415/tuorps/internal/connections/adapters/google"
	"github.com/yahya31415/tuorps/internal/connections/adapters/instagram"
	"github.com/yahya31415/tuorps/internal/connections/adapters/linkedin"
	"github.com/yahya31415/tuorps/internal/connections/adapters/meta"
	connscore "github.com/yahya31415/tuorps/internal/connections/core"
	connhttp "github.com/yahya31415/tuorps/internal/connections/http"
	"github.com/yahya31415/tuorps/internal/platform/config"
)

const platformTimeout = 10 * time.Second

// Platforms builds the configured platform integrations against the
// production hosts. Redirect URIs are APP_ORIGIN/v1/oauth/<provider>/callback
// and must be registered exactly with each platform.
func Platforms(cfg config.Config) map[connscore.Provider]connhttp.Platform {
	redirect := func(p connscore.Provider) string {
		return cfg.AppOrigin.JoinPath("/v1/oauth", string(p), "callback").String()
	}
	out := map[connscore.Provider]connhttp.Platform{}
	if m := cfg.Platforms.Meta; m != nil {
		a := meta.New(meta.Config{AppID: m.ClientID, AppSecret: m.ClientSecret.Reveal(), RedirectURI: redirect(connscore.ProviderMeta), GraphURL: meta.DefaultGraphURL, Timeout: platformTimeout})
		out[connscore.ProviderMeta] = connhttp.Platform{Client: connscore.OAuthClient{ClientID: m.ClientID, RedirectURI: redirect(connscore.ProviderMeta), ConfigID: m.LoginConfigID}, Ports: a.Provider()}
	}
	if i := cfg.Platforms.Instagram; i != nil {
		a := instagram.New(instagram.Config{AppID: i.ClientID, AppSecret: i.ClientSecret.Reveal(), RedirectURI: redirect(connscore.ProviderInstagram), APIURL: instagram.DefaultAPIURL, GraphURL: instagram.DefaultGraphURL, Timeout: platformTimeout})
		out[connscore.ProviderInstagram] = connhttp.Platform{Client: connscore.OAuthClient{ClientID: i.ClientID, RedirectURI: redirect(connscore.ProviderInstagram)}, Ports: a.Provider()}
	}
	if l := cfg.Platforms.LinkedIn; l != nil {
		a := linkedin.New(linkedin.Config{ClientID: l.ClientID, ClientSecret: l.ClientSecret.Reveal(), RedirectURI: redirect(connscore.ProviderLinkedIn), OAuthURL: linkedin.DefaultOAuthURL, APIURL: linkedin.DefaultAPIURL, Timeout: platformTimeout})
		out[connscore.ProviderLinkedIn] = connhttp.Platform{Client: connscore.OAuthClient{ClientID: l.ClientID, RedirectURI: redirect(connscore.ProviderLinkedIn)}, Ports: a.Provider()}
	}
	if g := cfg.Platforms.Google; g != nil {
		a := google.New(google.Config{ClientID: g.ClientID, ClientSecret: g.ClientSecret.Reveal(), RedirectURI: redirect(connscore.ProviderGoogle), OAuthURL: google.DefaultOAuthURL, APIURL: google.DefaultAPIURL, Timeout: platformTimeout})
		out[connscore.ProviderGoogle] = connhttp.Platform{Client: connscore.OAuthClient{ClientID: g.ClientID, RedirectURI: redirect(connscore.ProviderGoogle)}, Ports: a.Provider()}
	}
	return out
}
