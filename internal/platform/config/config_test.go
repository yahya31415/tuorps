package config_test

import (
	"errors"
	"io/fs"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/config"
)

// env builds a Lookup over a fixed map.
func env(kv map[string]string) config.Lookup {
	return func(k string) (string, bool) {
		v, ok := kv[k]
		return v, ok
	}
}

// files builds a ReadFile over a fixed map; unknown paths do not exist.
func files(kv map[string]string) config.ReadFile {
	return func(p string) ([]byte, error) {
		v, ok := kv[p]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(v), nil
	}
}

// testKeys is a valid keyring document: one 32-byte key (all 0x01).
const testKeys = "k1:AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="

func minimal() map[string]string {
	return map[string]string{
		"DATABASE_URL":             "postgres://app:pw@db:5432/tuorps",
		"REDIS_URL":                "redis://:pw@redis:6379/0",
		"ENCRYPTION_KEYS":          testKeys,
		"ENCRYPTION_ACTIVE_KEY_ID": "k1",
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := config.Load(env(minimal()), files(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"Env", cfg.Env, config.EnvDev},
		{"HTTPAddr", cfg.HTTPAddr, ":8080"},
		{"WorkerHealthAddr", cfg.WorkerHealthAddr, ":8081"},
		{"LogLevel", cfg.LogLevel, slog.LevelInfo},
		{"ShutdownTimeout", cfg.ShutdownTimeout, 15 * time.Second},
		{"ReadinessTimeout", cfg.ReadinessTimeout, 2 * time.Second},
		{"HTTP.ReadHeaderTimeout", cfg.HTTP.ReadHeaderTimeout, 5 * time.Second},
		{"HTTP.ReadTimeout", cfg.HTTP.ReadTimeout, 15 * time.Second},
		{"HTTP.WriteTimeout", cfg.HTTP.WriteTimeout, 30 * time.Second},
		{"HTTP.IdleTimeout", cfg.HTTP.IdleTimeout, 120 * time.Second},
		{"HTTP.MaxBodyBytes", cfg.HTTP.MaxBodyBytes, int64(1 << 20)},
		{"DB.URL", cfg.DB.URL.Reveal(), "postgres://app:pw@db:5432/tuorps"},
		{"DB.MaxConns", cfg.DB.MaxConns, int32(20)},
		{"DB.MinConns", cfg.DB.MinConns, int32(2)},
		{"DB.MaxConnLifetime", cfg.DB.MaxConnLifetime, time.Hour},
		{"DB.MaxConnIdleTime", cfg.DB.MaxConnIdleTime, 15 * time.Minute},
		{"Redis.URL", cfg.Redis.URL.Reveal(), "redis://:pw@redis:6379/0"},
		{"Redis.PoolSize", cfg.Redis.PoolSize, 20},
		{"Redis.DialTimeout", cfg.Redis.DialTimeout, 2 * time.Second},
		{"Redis.ReadTimeout", cfg.Redis.ReadTimeout, time.Second},
		{"Redis.WriteTimeout", cfg.Redis.WriteTimeout, time.Second},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoadReportsEveryMissingRequiredVar(t *testing.T) {
	_, err := config.Load(env(nil), files(nil))
	if err == nil {
		t.Fatal("Load with empty env: want error")
	}
	if !errors.Is(err, config.ErrMissing) {
		t.Errorf("error does not wrap ErrMissing: %v", err)
	}
	for _, key := range []string{"DATABASE_URL", "REDIS_URL"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not name %s: %v", key, err)
		}
	}
}

func TestLoadTreatsEmptyAsMissing(t *testing.T) {
	kv := minimal()
	kv["REDIS_URL"] = "  "
	_, err := config.Load(env(kv), files(nil))
	if !errors.Is(err, config.ErrMissing) || !strings.Contains(err.Error(), "REDIS_URL") {
		t.Fatalf("want missing REDIS_URL, got %v", err)
	}
}

func TestSecretNeverRendersItsValue(t *testing.T) {
	cfg, err := config.Load(env(minimal()), files(nil))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.DB.URL
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("cfg", "db", s)
	json, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for name, rendered := range map[string]string{
		"String":   s.String(),
		"GoString": s.GoString(),
		"slog":     logged.String(),
		"JSON":     string(json),
		"fmt %v":   fmtV(s),
		"fmt %+v":  fmtPlusV(cfg),
	} {
		if strings.Contains(rendered, "pw@db") {
			t.Errorf("%s leaks the secret: %s", name, rendered)
		}
	}
}
