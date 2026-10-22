package config

import (
	"errors"
	"net/mail"
)

var errOpsEmail = errors.New("invalid value: not an email address")

// OAuthApp is a platform app registration.
type OAuthApp struct {
	ClientID     string
	ClientSecret Secret
}

// MetaApp is the Meta app used with Facebook Login for Business.
type MetaApp struct {
	OAuthApp
	LoginConfigID      string // Facebook Login for Business configuration
	WebhookVerifyToken Secret // hub.verify_token for Page and Instagram webhooks
}

// InstagramApp is the Instagram app (API with Instagram Login).
type InstagramApp struct {
	OAuthApp
	WebhookVerifyToken Secret
}

// Platforms are the optional platform integrations. A nil entry is a
// platform that is not configured; a partly configured one fails Load.
type Platforms struct {
	Meta          *MetaApp
	Instagram     *InstagramApp
	LinkedIn      *OAuthApp
	Google        *OAuthApp
	OpsAlertEmail string // version-sunset alerts; empty disables the email
}

// present reports whether key is set directly or through key_FILE.
func (r *reader) present(key string) bool {
	_, direct := r.raw(key)
	_, viaFile := r.raw(key + "_FILE")
	return direct || viaFile
}

// group reports whether any of plain or secrets is set; if so, every one
// must be, and each missing key is reported.
func (r *reader) group(plain, secrets []string) bool {
	set := false
	for _, k := range plain {
		_, ok := r.raw(k)
		set = set || ok
	}
	for _, k := range secrets {
		set = set || r.present(k)
	}
	if !set {
		return false
	}
	for _, k := range plain {
		if _, ok := r.raw(k); !ok {
			r.fail(k, ErrMissing)
		}
	}
	return true
}

func (r *reader) readPlatforms(cfg *Config) {
	p := &cfg.Platforms
	if r.group([]string{"META_APP_ID", "META_LOGIN_CONFIG_ID"}, []string{"META_APP_SECRET", "META_WEBHOOK_VERIFY_TOKEN"}) {
		id, _ := r.raw("META_APP_ID")
		cfgID, _ := r.raw("META_LOGIN_CONFIG_ID")
		p.Meta = &MetaApp{OAuthApp: OAuthApp{ClientID: id, ClientSecret: r.secret("META_APP_SECRET")}, LoginConfigID: cfgID, WebhookVerifyToken: r.secret("META_WEBHOOK_VERIFY_TOKEN")}
	}
	if r.group([]string{"INSTAGRAM_APP_ID"}, []string{"INSTAGRAM_APP_SECRET", "INSTAGRAM_WEBHOOK_VERIFY_TOKEN"}) {
		id, _ := r.raw("INSTAGRAM_APP_ID")
		p.Instagram = &InstagramApp{OAuthApp: OAuthApp{ClientID: id, ClientSecret: r.secret("INSTAGRAM_APP_SECRET")}, WebhookVerifyToken: r.secret("INSTAGRAM_WEBHOOK_VERIFY_TOKEN")}
	}
	p.LinkedIn = r.oauthApp("LINKEDIN_CLIENT_ID", "LINKEDIN_CLIENT_SECRET")
	p.Google = r.oauthApp("GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET")
	if v, ok := r.raw("OPS_ALERT_EMAIL"); ok {
		if a, err := mail.ParseAddress(v); err != nil || a.Name != "" {
			r.fail("OPS_ALERT_EMAIL", errOpsEmail)
		} else {
			p.OpsAlertEmail = a.Address
		}
	}
}

func (r *reader) oauthApp(idKey, secretKey string) *OAuthApp {
	if !r.group([]string{idKey}, []string{secretKey}) {
		return nil
	}
	id, _ := r.raw(idKey)
	return &OAuthApp{ClientID: id, ClientSecret: r.secret(secretKey)}
}
