package config_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/yahya31415/tuorps/internal/platform/config"
)

func TestLoadMigrate(t *testing.T) {
	t.Run("requires only the migrator URL", func(t *testing.T) {
		cfg, err := config.LoadMigrate(env(map[string]string{
			"MIGRATE_DATABASE_URL": "postgres://owner:pw@db/tuorps",
			"LOG_LEVEL":            "debug",
		}), files(nil))
		if err != nil {
			t.Fatalf("LoadMigrate: %v", err)
		}
		if got := cfg.DatabaseURL.Reveal(); got != "postgres://owner:pw@db/tuorps" {
			t.Errorf("DatabaseURL = %q", got)
		}
		if cfg.LogLevel != slog.LevelDebug {
			t.Errorf("LogLevel = %v", cfg.LogLevel)
		}
	})
	t.Run("reads the URL from a secret file", func(t *testing.T) {
		cfg, err := config.LoadMigrate(
			env(map[string]string{"MIGRATE_DATABASE_URL_FILE": "/s"}),
			files(map[string]string{"/s": "postgresql://owner:pw@db/tuorps\n"}))
		if err != nil || cfg.DatabaseURL.Reveal() != "postgresql://owner:pw@db/tuorps" {
			t.Fatalf("cfg = %v, err = %v", cfg.DatabaseURL.Reveal(), err)
		}
	})
	t.Run("missing URL", func(t *testing.T) {
		_, err := config.LoadMigrate(env(nil), files(nil))
		if !errors.Is(err, config.ErrMissing) || !strings.Contains(err.Error(), "MIGRATE_DATABASE_URL") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("wrong scheme", func(t *testing.T) {
		_, err := config.LoadMigrate(env(map[string]string{"MIGRATE_DATABASE_URL": "redis://h"}), files(nil))
		if !errors.Is(err, config.ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
	})
}
