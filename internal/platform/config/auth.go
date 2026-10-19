package config

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/keyring"
)

var (
	errOrigin        = errors.New("invalid value: want an origin such as https://app.example.in (no path)")
	errHTTPSRequired = errors.New("invalid value: https is required in prod")
	errRemoved       = errors.New("invalid value: setting removed (session cookies are always Secure); unset it")
	errIdleAbsolute  = errors.New("invalid value: idle timeout exceeds the absolute timeout")
	errCIDR          = errors.New("invalid value: want comma-separated CIDRs")
	errKeyring       = errors.New("invalid value: want <key-id>:<base64 32-byte key> lines")
	errActiveKey     = errors.New("invalid value: no configured key has this ID")
)

// Auth configures sessions and password hashing.
type Auth struct {
	SessionIdle          time.Duration
	SessionAbsolute      time.Duration
	Argon2MaxConcurrency int // bounds argon2id memory: each hash uses ~46 MiB
}

// Encryption holds the keyring material for secrets at rest.
type Encryption struct {
	Keys        Secret
	ActiveKeyID string
}

// Keyring builds the keyring. Load has already validated the material.
func (e Encryption) Keyring() (*keyring.Keyring, error) {
	keys, err := keyring.Parse(e.Keys.Reveal())
	if err != nil {
		return nil, err
	}
	return keyring.New(keys, e.ActiveKeyID)
}

// parseOrigin accepts scheme://host[:port] with http(s) and nothing else.
func parseOrigin(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errOrigin
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

func parseCIDRs(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		p, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, errCIDR
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// readAuth reads origin, session, proxy and encryption settings for env.
func (r *reader) readAuth(cfg *Config) {
	origin, originSet := r.raw("APP_ORIGIN")
	cfg.AppOrigin = &url.URL{Scheme: "http", Host: "localhost:8080"}
	if originSet {
		if u, err := parseOrigin(origin); err != nil {
			r.fail("APP_ORIGIN", err)
		} else {
			cfg.AppOrigin = u
		}
	}
	cfg.Auth = Auth{
		SessionIdle:          opt(r, "SESSION_IDLE_TIMEOUT", 12*time.Hour, parseDuration),
		SessionAbsolute:      opt(r, "SESSION_ABSOLUTE_TIMEOUT", 14*24*time.Hour, parseDuration),
		Argon2MaxConcurrency: opt(r, "ARGON2_MAX_CONCURRENCY", 4, parsePositiveInt),
	}
	cfg.TrustedProxies = opt(r, "TRUSTED_PROXY_CIDRS", []netip.Prefix(nil), parseCIDRs)

	if _, set := r.raw("COOKIE_SECURE"); set {
		r.fail("COOKIE_SECURE", errRemoved)
	}
	if cfg.Auth.SessionIdle > cfg.Auth.SessionAbsolute {
		r.fail("SESSION_IDLE_TIMEOUT", errIdleAbsolute)
	}
	if cfg.Env == EnvProd {
		switch {
		case !originSet:
			r.fail("APP_ORIGIN", ErrMissing)
		case cfg.AppOrigin.Scheme != "https":
			r.fail("APP_ORIGIN", errHTTPSRequired)
		}
	}

	keys := r.secret("ENCRYPTION_KEYS")
	active, activeSet := r.raw("ENCRYPTION_ACTIVE_KEY_ID")
	if !activeSet {
		r.fail("ENCRYPTION_ACTIVE_KEY_ID", ErrMissing)
	}
	cfg.Encryption = Encryption{Keys: keys, ActiveKeyID: active}
	if keys.Reveal() == "" {
		return
	}
	parsed, err := keyring.Parse(keys.Reveal())
	if err != nil {
		r.fail("ENCRYPTION_KEYS", errKeyring)
		return
	}
	if _, ok := parsed[active]; activeSet && !ok {
		r.fail("ENCRYPTION_ACTIVE_KEY_ID", errActiveKey)
	}
}
