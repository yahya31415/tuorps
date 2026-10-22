package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/yahya31415/tuorps/internal/platform/config"
)

func withVars(base map[string]string, extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func allPlatforms() map[string]string {
	return map[string]string{
		"META_APP_ID": "meta-id", "META_APP_SECRET": "zz9-meta-secret", "META_LOGIN_CONFIG_ID": "cfg-1", "META_WEBHOOK_VERIFY_TOKEN": "zz9-meta-verify",
		"INSTAGRAM_APP_ID": "ig-id", "INSTAGRAM_APP_SECRET": "zz9-ig-secret", "INSTAGRAM_WEBHOOK_VERIFY_TOKEN": "zz9-ig-verify",
		"LINKEDIN_CLIENT_ID": "li-id", "LINKEDIN_CLIENT_SECRET": "zz9-li-secret",
		"GOOGLE_CLIENT_ID": "g-id", "GOOGLE_CLIENT_SECRET": "zz9-g-secret",
		"OPS_ALERT_EMAIL": "ops@tuorps.in",
	}
}

// Platforms are optional: each is enabled only by its complete set of
// credentials, so a half-configured platform fails startup instead of
// failing at a customer's first connection.
func TestLoadPlatforms(t *testing.T) {
	t.Run("none configured", func(t *testing.T) {
		cfg, err := config.Load(env(minimal()), files(nil))
		if err != nil {
			t.Fatal(err)
		}
		p := cfg.Platforms
		if p.Meta != nil || p.Instagram != nil || p.LinkedIn != nil || p.Google != nil || p.OpsAlertEmail != "" {
			t.Errorf("Platforms = %+v, want all disabled", p)
		}
	})

	t.Run("all configured", func(t *testing.T) {
		cfg, err := config.Load(env(withVars(minimal(), allPlatforms())), files(nil))
		if err != nil {
			t.Fatal(err)
		}
		p := cfg.Platforms
		if p.Meta == nil || p.Meta.ClientID != "meta-id" || p.Meta.ClientSecret.Reveal() != "zz9-meta-secret" || p.Meta.LoginConfigID != "cfg-1" || p.Meta.WebhookVerifyToken.Reveal() != "zz9-meta-verify" {
			t.Errorf("Meta = %+v", p.Meta)
		}
		if p.Instagram == nil || p.Instagram.ClientID != "ig-id" || p.Instagram.ClientSecret.Reveal() != "zz9-ig-secret" || p.Instagram.WebhookVerifyToken.Reveal() != "zz9-ig-verify" {
			t.Errorf("Instagram = %+v", p.Instagram)
		}
		if p.LinkedIn == nil || p.LinkedIn.ClientID != "li-id" || p.LinkedIn.ClientSecret.Reveal() != "zz9-li-secret" {
			t.Errorf("LinkedIn = %+v", p.LinkedIn)
		}
		if p.Google == nil || p.Google.ClientID != "g-id" || p.Google.ClientSecret.Reveal() != "zz9-g-secret" {
			t.Errorf("Google = %+v", p.Google)
		}
		if p.OpsAlertEmail != "ops@tuorps.in" {
			t.Errorf("OpsAlertEmail = %q", p.OpsAlertEmail)
		}
	})

	t.Run("secrets from files", func(t *testing.T) {
		kv := withVars(minimal(), map[string]string{"GOOGLE_CLIENT_ID": "g-id", "GOOGLE_CLIENT_SECRET_FILE": "/run/secrets/google_client_secret"})
		cfg, err := config.Load(env(kv), files(map[string]string{"/run/secrets/google_client_secret": "from-file\n"}))
		if err != nil || cfg.Platforms.Google == nil || cfg.Platforms.Google.ClientSecret.Reveal() != "from-file" {
			t.Fatalf("Google = %+v, %v", cfg.Platforms.Google, err)
		}
	})

	t.Run("partial sets name every missing variable", func(t *testing.T) {
		kv := withVars(minimal(), map[string]string{"META_APP_ID": "meta-id", "LINKEDIN_CLIENT_SECRET": "zz9-li-secret"})
		_, err := config.Load(env(kv), files(nil))
		if !errors.Is(err, config.ErrMissing) {
			t.Fatalf("err = %v, want ErrMissing", err)
		}
		for _, key := range []string{"META_APP_SECRET", "META_LOGIN_CONFIG_ID", "META_WEBHOOK_VERIFY_TOKEN", "LINKEDIN_CLIENT_ID"} {
			if !strings.Contains(err.Error(), key) {
				t.Errorf("error does not name %s: %v", key, err)
			}
		}
		if strings.Contains(err.Error(), "zz9") {
			t.Errorf("error echoes a secret: %v", err)
		}
	})

	t.Run("invalid ops alert address", func(t *testing.T) {
		kv := withVars(minimal(), map[string]string{"OPS_ALERT_EMAIL": "not an address zz9"})
		_, err := config.Load(env(kv), files(nil))
		if !errors.Is(err, config.ErrInvalid) || !strings.Contains(err.Error(), "OPS_ALERT_EMAIL") || strings.Contains(err.Error(), "zz9") {
			t.Fatalf("err = %v", err)
		}
	})
}
