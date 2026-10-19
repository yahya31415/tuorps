package config_test

import (
	"errors"
	"log/slog"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/config"
)

func with(extra map[string]string) map[string]string {
	kv := minimal()
	maps.Copy(kv, extra)
	return kv
}

func TestLoadReadsSecretsFromFiles(t *testing.T) {
	kv := map[string]string{
		"DATABASE_URL_FILE": "/run/secrets/database_url",
		"REDIS_URL_FILE":    "/run/secrets/redis_url",
	}
	fs := map[string]string{
		"/run/secrets/database_url": "postgres://app:filepw@db/tuorps\n",
		"/run/secrets/redis_url":    "redis://:filepw@redis:6379/0\r\n",
	}
	cfg, err := config.Load(env(kv), files(fs))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.DB.URL.Reveal(); got != "postgres://app:filepw@db/tuorps" {
		t.Errorf("DB.URL = %q (trailing newline must be trimmed)", got)
	}
	if got := cfg.Redis.URL.Reveal(); got != "redis://:filepw@redis:6379/0" {
		t.Errorf("Redis.URL = %q", got)
	}
}

func TestLoadSecretFileErrors(t *testing.T) {
	cases := []struct {
		name    string
		kv      map[string]string
		fs      map[string]string
		wantKey string
		wantErr error
	}{
		{"unreadable file", map[string]string{"DATABASE_URL_FILE": "/nope", "REDIS_URL": "redis://r"}, nil, "DATABASE_URL_FILE", config.ErrInvalid},
		{"empty file", map[string]string{"DATABASE_URL_FILE": "/empty", "REDIS_URL": "redis://r"}, map[string]string{"/empty": "\n"}, "DATABASE_URL_FILE", config.ErrMissing},
		{"both value and file", with(map[string]string{"DATABASE_URL_FILE": "/f"}), map[string]string{"/f": "postgres://x"}, "DATABASE_URL", config.ErrConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(env(tc.kv), files(tc.fs))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Errorf("error does not name %s: %v", tc.wantKey, err)
			}
		})
	}
}

func TestLoadOverrides(t *testing.T) {
	cases := []struct {
		key, value string
		get        func(config.Config) any
		want       any
	}{
		{"TUORPS_ENV", "prod", func(c config.Config) any { return c.Env }, config.EnvProd},
		{"HTTP_ADDR", "127.0.0.1:9000", func(c config.Config) any { return c.HTTPAddr }, "127.0.0.1:9000"},
		{"WORKER_HEALTH_ADDR", ":9001", func(c config.Config) any { return c.WorkerHealthAddr }, ":9001"},
		{"LOG_LEVEL", "debug", func(c config.Config) any { return c.LogLevel }, slog.LevelDebug},
		{"LOG_LEVEL", "WARN", func(c config.Config) any { return c.LogLevel }, slog.LevelWarn},
		{"LOG_LEVEL", "error", func(c config.Config) any { return c.LogLevel }, slog.LevelError},
		{"SHUTDOWN_TIMEOUT", "30s", func(c config.Config) any { return c.ShutdownTimeout }, 30 * time.Second},
		{"READINESS_TIMEOUT", "500ms", func(c config.Config) any { return c.ReadinessTimeout }, 500 * time.Millisecond},
		{"HTTP_READ_HEADER_TIMEOUT", "2s", func(c config.Config) any { return c.HTTP.ReadHeaderTimeout }, 2 * time.Second},
		{"HTTP_READ_TIMEOUT", "10s", func(c config.Config) any { return c.HTTP.ReadTimeout }, 10 * time.Second},
		{"HTTP_WRITE_TIMEOUT", "1m", func(c config.Config) any { return c.HTTP.WriteTimeout }, time.Minute},
		{"HTTP_IDLE_TIMEOUT", "90s", func(c config.Config) any { return c.HTTP.IdleTimeout }, 90 * time.Second},
		{"HTTP_MAX_BODY_BYTES", "512KiB", func(c config.Config) any { return c.HTTP.MaxBodyBytes }, int64(512 << 10)},
		{"HTTP_MAX_BODY_BYTES", "2MiB", func(c config.Config) any { return c.HTTP.MaxBodyBytes }, int64(2 << 20)},
		{"HTTP_MAX_BODY_BYTES", "4096", func(c config.Config) any { return c.HTTP.MaxBodyBytes }, int64(4096)},
		{"DB_MAX_CONNS", "40", func(c config.Config) any { return c.DB.MaxConns }, int32(40)},
		{"DB_MIN_CONNS", "0", func(c config.Config) any { return c.DB.MinConns }, int32(0)},
		{"DB_MAX_CONN_LIFETIME", "30m", func(c config.Config) any { return c.DB.MaxConnLifetime }, 30 * time.Minute},
		{"DB_MAX_CONN_IDLE_TIME", "5m", func(c config.Config) any { return c.DB.MaxConnIdleTime }, 5 * time.Minute},
		{"REDIS_POOL_SIZE", "50", func(c config.Config) any { return c.Redis.PoolSize }, 50},
		{"REDIS_DIAL_TIMEOUT", "3s", func(c config.Config) any { return c.Redis.DialTimeout }, 3 * time.Second},
		{"REDIS_READ_TIMEOUT", "750ms", func(c config.Config) any { return c.Redis.ReadTimeout }, 750 * time.Millisecond},
		{"REDIS_WRITE_TIMEOUT", "750ms", func(c config.Config) any { return c.Redis.WriteTimeout }, 750 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			cfg, err := config.Load(env(with(map[string]string{tc.key: tc.value})), files(nil))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := tc.get(cfg); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadRejectsInvalidValuesWithoutEchoingThem(t *testing.T) {
	cases := []struct{ key, value string }{
		{"TUORPS_ENV", "staging-zz9"},
		{"HTTP_ADDR", "no-port-zz9"},
		{"WORKER_HEALTH_ADDR", "zz9"},
		{"LOG_LEVEL", "loud-zz9"},
		{"SHUTDOWN_TIMEOUT", "zz9"},
		{"SHUTDOWN_TIMEOUT", "-1s"},
		{"SHUTDOWN_TIMEOUT", "0s"},
		{"HTTP_READ_TIMEOUT", "zz9"},
		{"HTTP_MAX_BODY_BYTES", "zz9MiB"},
		{"HTTP_MAX_BODY_BYTES", "0"},
		{"DB_MAX_CONNS", "zz9"},
		{"DB_MAX_CONNS", "0"},
		{"DB_MAX_CONNS", "99999999999"},
		{"DB_MIN_CONNS", "-1"},
		{"DB_MIN_CONNS", "21"}, // above the default max of 20
		{"REDIS_POOL_SIZE", "-3"},
		{"DATABASE_URL", "mysql://u:zz9@h/db"},
		{"DATABASE_URL", "zz9 not a url"},
		{"REDIS_URL", "http://u:zz9@h"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			_, err := config.Load(env(with(map[string]string{tc.key: tc.value})), files(nil))
			if !errors.Is(err, config.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error does not name %s: %v", tc.key, err)
			}
			if strings.Contains(err.Error(), "zz9") {
				t.Errorf("error echoes the value: %v", err)
			}
		})
	}
}

func TestLoadCollectsAllErrors(t *testing.T) {
	_, err := config.Load(env(map[string]string{"LOG_LEVEL": "loud", "DB_MAX_CONNS": "x"}), files(nil))
	for _, key := range []string{"DATABASE_URL", "REDIS_URL", "LOG_LEVEL", "DB_MAX_CONNS"} {
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}
