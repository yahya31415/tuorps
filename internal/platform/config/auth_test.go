package config_test

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/config"
)

func TestLoadAuthAndOriginDefaults(t *testing.T) {
	cfg, err := config.Load(env(minimal()), files(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.AppOrigin.String(); got != "http://localhost:8080" {
		t.Errorf("AppOrigin = %q (dev default)", got)
	}
	if !cfg.Auth.CookieSecure || cfg.Auth.SessionIdle != 12*time.Hour || cfg.Auth.SessionAbsolute != 14*24*time.Hour || cfg.Auth.Argon2MaxConcurrency != 4 {
		t.Errorf("Auth = %+v", cfg.Auth)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("TrustedProxies = %v, want none by default", cfg.TrustedProxies)
	}
	k, err := cfg.Encryption.Keyring()
	if err != nil || k.ActiveKeyID() != "k1" {
		t.Errorf("Keyring = %v, %v", k, err)
	}
}

func TestLoadAuthOverrides(t *testing.T) {
	cfg, err := config.Load(env(with(map[string]string{
		"APP_ORIGIN":               "https://app.tuorps.in",
		"COOKIE_SECURE":            "false",
		"SESSION_IDLE_TIMEOUT":     "2h",
		"SESSION_ABSOLUTE_TIMEOUT": "72h",
		"ARGON2_MAX_CONCURRENCY":   "8",
		"TRUSTED_PROXY_CIDRS":      "10.0.0.0/8, fd00::/8",
	})), files(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AppOrigin.String() != "https://app.tuorps.in" || cfg.Auth.CookieSecure || cfg.Auth.SessionIdle != 2*time.Hour ||
		cfg.Auth.SessionAbsolute != 72*time.Hour || cfg.Auth.Argon2MaxConcurrency != 8 {
		t.Errorf("cfg = %+v %+v", cfg.AppOrigin, cfg.Auth)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	if !slices.Equal(cfg.TrustedProxies, want) {
		t.Errorf("TrustedProxies = %v", cfg.TrustedProxies)
	}
}

func TestLoadEncryptionKeysFromSecretFile(t *testing.T) {
	kv := minimal()
	delete(kv, "ENCRYPTION_KEYS")
	kv["ENCRYPTION_KEYS_FILE"] = "/run/secrets/encryption_keys"
	cfg, err := config.Load(env(kv), files(map[string]string{"/run/secrets/encryption_keys": "# keys\n" + testKeys + "\n"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := cfg.Encryption.Keyring(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmtPlusV(cfg), "AQEBAQ") {
		t.Error("key material visible when formatting the config")
	}
}

func TestLoadAuthRejections(t *testing.T) {
	cases := []struct {
		name string
		kv   map[string]string
		key  string
		want error
	}{
		{"missing keys", without(minimal(), "ENCRYPTION_KEYS"), "ENCRYPTION_KEYS", config.ErrMissing},
		{"missing active key id", without(minimal(), "ENCRYPTION_ACTIVE_KEY_ID"), "ENCRYPTION_ACTIVE_KEY_ID", config.ErrMissing},
		{"malformed keys", with(map[string]string{"ENCRYPTION_KEYS": "k1:zz9"}), "ENCRYPTION_KEYS", config.ErrInvalid},
		{"active key not configured", with(map[string]string{"ENCRYPTION_ACTIVE_KEY_ID": "k2"}), "ENCRYPTION_ACTIVE_KEY_ID", config.ErrInvalid},
		{"origin with path", with(map[string]string{"APP_ORIGIN": "https://app.tuorps.in/zz9"}), "APP_ORIGIN", config.ErrInvalid},
		{"origin not http", with(map[string]string{"APP_ORIGIN": "ftp://zz9"}), "APP_ORIGIN", config.ErrInvalid},
		{"prod requires an explicit origin", with(map[string]string{"TUORPS_ENV": "prod"}), "APP_ORIGIN", config.ErrMissing},
		{"prod requires https", with(map[string]string{"TUORPS_ENV": "prod", "APP_ORIGIN": "http://zz9.example"}), "APP_ORIGIN", config.ErrInvalid},
		{"prod forbids insecure cookies", with(map[string]string{"TUORPS_ENV": "prod", "APP_ORIGIN": "https://a.example", "COOKIE_SECURE": "false"}), "COOKIE_SECURE", config.ErrInvalid},
		{"bad bool", with(map[string]string{"COOKIE_SECURE": "zz9"}), "COOKIE_SECURE", config.ErrInvalid},
		{"idle beyond absolute", with(map[string]string{"SESSION_IDLE_TIMEOUT": "48h", "SESSION_ABSOLUTE_TIMEOUT": "24h"}), "SESSION_IDLE_TIMEOUT", config.ErrInvalid},
		{"bad cidr", with(map[string]string{"TRUSTED_PROXY_CIDRS": "10.0.0.0/8,zz9"}), "TRUSTED_PROXY_CIDRS", config.ErrInvalid},
		{"zero argon2 concurrency", with(map[string]string{"ARGON2_MAX_CONCURRENCY": "0"}), "ARGON2_MAX_CONCURRENCY", config.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(env(tc.kv), files(nil))
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("err = %v, want %v naming %s", err, tc.want, tc.key)
			}
			if strings.Contains(err.Error(), "zz9") {
				t.Errorf("error echoes the value: %v", err)
			}
		})
	}
}

func TestLoadSMTP(t *testing.T) {
	base := map[string]string{"SMTP_HOST": "email-smtp.ap-south-1.amazonaws.com", "MAIL_FROM": "Tuorps <no-reply@tuorps.in>", "SMTP_USERNAME": "AKIA", "SMTP_PASSWORD": "pw"}
	merge := func(extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	t.Run("defaults", func(t *testing.T) {
		s, err := config.LoadSMTP(env(base), files(nil), config.EnvProd)
		if err != nil {
			t.Fatalf("LoadSMTP: %v", err)
		}
		if s.Host != "email-smtp.ap-south-1.amazonaws.com" || s.Port != 587 || s.TLS != config.SMTPStartTLS ||
			s.Username != "AKIA" || s.Password.Reveal() != "pw" || s.From.Address != "no-reply@tuorps.in" || s.Timeout != 10*time.Second {
			t.Errorf("SMTP = %+v", s)
		}
	})
	t.Run("implicit TLS on 465", func(t *testing.T) {
		s, err := config.LoadSMTP(env(merge(map[string]string{"SMTP_TLS": "implicit", "SMTP_PORT": "465"})), files(nil), config.EnvProd)
		if err != nil || s.TLS != config.SMTPImplicitTLS || s.Port != 465 {
			t.Fatalf("SMTP = %+v, %v", s, err)
		}
	})
	t.Run("plaintext allowed only in dev", func(t *testing.T) {
		kv := map[string]string{"SMTP_HOST": "mailpit", "SMTP_PORT": "1025", "SMTP_TLS": "none", "MAIL_FROM": "dev@tuorps.local"}
		if _, err := config.LoadSMTP(env(kv), files(nil), config.EnvDev); err != nil {
			t.Fatalf("dev plaintext: %v", err)
		}
		if _, err := config.LoadSMTP(env(kv), files(nil), config.EnvProd); !errors.Is(err, config.ErrInvalid) || !strings.Contains(err.Error(), "SMTP_TLS") {
			t.Fatalf("prod plaintext: err = %v", err)
		}
	})
	t.Run("rejections", func(t *testing.T) {
		for name, kv := range map[string]map[string]string{
			"missing host":      without(base, "SMTP_HOST"),
			"missing from":      without(base, "MAIL_FROM"),
			"bad from":          merge(map[string]string{"MAIL_FROM": "not an address zz9"}),
			"header injection":  merge(map[string]string{"MAIL_FROM": "a@b.c\r\nBcc: zz9@evil.test"}),
			"bad tls mode":      merge(map[string]string{"SMTP_TLS": "zz9"}),
			"bad port":          merge(map[string]string{"SMTP_PORT": "70000"}),
			"password, no user": without(base, "SMTP_USERNAME"),
		} {
			t.Run(name, func(t *testing.T) {
				_, err := config.LoadSMTP(env(kv), files(nil), config.EnvProd)
				if err == nil {
					t.Fatal("accepted")
				}
				if strings.Contains(err.Error(), "zz9") || strings.Contains(err.Error(), "pw") {
					t.Errorf("error echoes a value: %v", err)
				}
			})
		}
	})
}

func without(kv map[string]string, keys ...string) map[string]string {
	out := map[string]string{}
	for k, v := range kv {
		if !slices.Contains(keys, k) {
			out[k] = v
		}
	}
	return out
}
