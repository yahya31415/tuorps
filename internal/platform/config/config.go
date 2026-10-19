// Package config loads process configuration from the environment and
// Docker secrets. Load is deterministic: all I/O arrives through the Lookup
// and ReadFile parameters, so it fails fast and is fully testable.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Lookup reads an environment variable (os.LookupEnv in production).
type Lookup func(key string) (string, bool)

// ReadFile reads a secret file (os.ReadFile in production).
type ReadFile func(path string) ([]byte, error)

// ErrMissing marks a required variable that is unset or blank.
var ErrMissing = errors.New("required but not set")

// Env is the deployment environment.
type Env string

const (
	EnvDev  Env = "dev"
	EnvProd Env = "prod"
)

// HTTP holds server limits shared by the API and worker health servers.
type HTTP struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxBodyBytes      int64
}

// DB configures the pgx connection pool.
type DB struct {
	URL             Secret
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// Redis configures the go-redis client.
type Redis struct {
	URL          Secret
	PoolSize     int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// Config is the validated configuration for the api and worker processes.
type Config struct {
	Env              Env
	HTTPAddr         string
	WorkerHealthAddr string
	LogLevel         slog.Level
	ShutdownTimeout  time.Duration
	ReadinessTimeout time.Duration
	HTTP             HTTP
	DB               DB
	Redis            Redis
}

// FieldError describes one invalid or missing variable. It never contains
// the variable's value.
type FieldError struct {
	Key string
	Err error
}

func (e *FieldError) Error() string { return fmt.Sprintf("config: %s: %v", e.Key, e.Err) }
func (e *FieldError) Unwrap() error { return e.Err }

// Load builds a Config, reporting every problem at once via errors.Join.
func Load(lookup Lookup, _ ReadFile) (Config, error) {
	var errs []error
	secret := func(key string) Secret {
		v, ok := lookup(key)
		if !ok || strings.TrimSpace(v) == "" {
			errs = append(errs, &FieldError{Key: key, Err: ErrMissing})
			return Secret{}
		}
		return NewSecret(v)
	}

	cfg := Config{
		Env:              EnvDev,
		HTTPAddr:         ":8080",
		WorkerHealthAddr: ":8081",
		LogLevel:         slog.LevelInfo,
		ShutdownTimeout:  15 * time.Second,
		ReadinessTimeout: 2 * time.Second,
		HTTP: HTTP{
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxBodyBytes:      1 << 20,
		},
		DB: DB{
			URL:             secret("DATABASE_URL"),
			MaxConns:        20,
			MinConns:        2,
			MaxConnLifetime: time.Hour,
			MaxConnIdleTime: 15 * time.Minute,
		},
		Redis: Redis{
			URL:          secret("REDIS_URL"),
			PoolSize:     20,
			DialTimeout:  2 * time.Second,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
		},
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}
