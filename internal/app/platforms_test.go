package app_test

import (
	"testing"

	"github.com/yahya31415/tuorps/internal/app"
	connscore "github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/platform/config"
)

func loadWith(t *testing.T, extra map[string]string) config.Config {
	t.Helper()
	kv := map[string]string{
		"DATABASE_URL": "postgres://app:pw@db:5432/tuorps", "REDIS_URL": "redis://:pw@redis:6379/0",
		"ENCRYPTION_KEYS": "k1:AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", "ENCRYPTION_ACTIVE_KEY_ID": "k1",
		"APP_ORIGIN": "https://app.tuorps.in",
	}
	for k, v := range extra {
		kv[k] = v
	}
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := kv[k]; return v, ok }, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Configured platforms become integrations with exact redirect URIs on
// APP_ORIGIN; unconfigured ones are absent.
func TestPlatformsFromConfig(t *testing.T) {
	if got := app.Platforms(loadWith(t, nil)); len(got) != 0 {
		t.Errorf("no credentials gave %d platforms", len(got))
	}
	got := app.Platforms(loadWith(t, map[string]string{
		"META_APP_ID": "meta-id", "META_APP_SECRET": "s", "META_LOGIN_CONFIG_ID": "cfg-1", "META_WEBHOOK_VERIFY_TOKEN": "v",
		"INSTAGRAM_APP_ID": "ig-id", "INSTAGRAM_APP_SECRET": "s", "INSTAGRAM_WEBHOOK_VERIFY_TOKEN": "v",
		"LINKEDIN_CLIENT_ID": "li-id", "LINKEDIN_CLIENT_SECRET": "s",
		"GOOGLE_CLIENT_ID": "g-id", "GOOGLE_CLIENT_SECRET": "s",
	}))
	want := map[connscore.Provider]string{connscore.ProviderMeta: "meta-id", connscore.ProviderInstagram: "ig-id", connscore.ProviderLinkedIn: "li-id", connscore.ProviderGoogle: "g-id"}
	if len(got) != len(want) {
		t.Fatalf("platforms = %d, want %d", len(got), len(want))
	}
	for p, id := range want {
		pl := got[p]
		if pl.Client.ClientID != id || pl.Client.RedirectURI != "https://app.tuorps.in/v1/oauth/"+string(p)+"/callback" {
			t.Errorf("%s client = %+v", p, pl.Client)
		}
		if pl.Ports.Exchange == nil || pl.Ports.ListAccounts == nil || pl.Ports.Check == nil {
			t.Errorf("%s is missing ports", p)
		}
		if _, err := connscore.AuthorizeURL(p, pl.Client, "state"); err != nil {
			t.Errorf("%s cannot build an authorize URL: %v", p, err)
		}
	}
	if got[connscore.ProviderMeta].Client.ConfigID != "cfg-1" {
		t.Errorf("Meta login configuration not passed")
	}
}
