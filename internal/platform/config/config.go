// Package config loads process configuration from the environment and
// Docker secrets. Load is deterministic: all I/O arrives through the Lookup
// and ReadFile parameters, so it fails fast and is fully testable.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
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
	AppOrigin        *url.URL // public web origin: email links and the CSRF trusted origin
	Auth             Auth
	TrustedProxies   []netip.Prefix // proxies whose X-Forwarded-For is honoured
	Encryption       Encryption
	Platforms        Platforms
}

// FieldError describes one invalid or missing variable. Err is always a
// fixed sentinel, so the message never contains the variable's value.
type FieldError struct {
	Key string
	Err error
}

func (e *FieldError) Error() string { return fmt.Sprintf("config: %s: %v", e.Key, e.Err) }
func (e *FieldError) Unwrap() error { return e.Err }

// Is lets every specific invalid-value sentinel match ErrInvalid.
func (e *FieldError) Is(target error) bool { return target == ErrInvalid && isInvalid(e.Err) }

// reader accumulates field errors while reading variables.
type reader struct {
	lookup   Lookup
	readFile ReadFile
	errs     []error
}

func (r *reader) fail(key string, err error) {
	r.errs = append(r.errs, &FieldError{Key: key, Err: err})
}

// raw returns the trimmed value of key, if set and non-blank.
func (r *reader) raw(key string) (string, bool) {
	v, ok := r.lookup(key)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

// secret reads key directly or from the file named by key_FILE.
func (r *reader) secret(key string) Secret {
	v, direct := r.raw(key)
	path, viaFile := r.raw(key + "_FILE")
	switch {
	case direct && viaFile:
		r.fail(key, ErrConflict)
	case direct:
		return NewSecret(v)
	case viaFile:
		b, err := r.readFile(path)
		if err != nil {
			r.fail(key+"_FILE", errFile)
			return Secret{}
		}
		if s := strings.TrimRight(string(b), "\r\n"); strings.TrimSpace(s) != "" {
			return NewSecret(s)
		}
		r.fail(key+"_FILE", ErrMissing)
	default:
		r.fail(key, ErrMissing)
	}
	return Secret{}
}

// requiredSecret reads a secret and validates it.
func (r *reader) requiredSecret(key string, validate func(Secret) (Secret, error)) Secret {
	s := r.secret(key)
	if s.Reveal() == "" {
		return s
	}
	v, err := validate(s)
	if err != nil {
		r.fail(key, err)
	}
	return v
}

// opt parses key with parse, keeping def when the variable is unset.
func opt[T any](r *reader, key string, def T, parse func(string) (T, error)) T {
	s, ok := r.raw(key)
	if !ok {
		return def
	}
	v, err := parse(s)
	if err != nil {
		r.fail(key, err)
		return def
	}
	return v
}

// Migrate is the configuration for running schema migrations, which connect
// as the schema-owning role rather than the runtime application role.
type Migrate struct {
	DatabaseURL Secret
	LogLevel    slog.Level
}

// LoadMigrate builds a Migrate config, reporting every problem at once.
func LoadMigrate(lookup Lookup, readFile ReadFile) (Migrate, error) {
	r := &reader{lookup: lookup, readFile: readFile}
	cfg := Migrate{
		DatabaseURL: r.requiredSecret("MIGRATE_DATABASE_URL", urlWithScheme("postgres", "postgresql")),
		LogLevel:    opt(r, "LOG_LEVEL", slog.LevelInfo, parseLevel),
	}
	if len(r.errs) > 0 {
		return Migrate{}, errors.Join(r.errs...)
	}
	return cfg, nil
}

// Load builds a Config, reporting every problem at once via errors.Join.
func Load(lookup Lookup, readFile ReadFile) (Config, error) {
	r := &reader{lookup: lookup, readFile: readFile}
	cfg := Config{
		Env:              opt(r, "TUORPS_ENV", EnvDev, parseEnv),
		HTTPAddr:         opt(r, "HTTP_ADDR", ":8080", parseAddr),
		WorkerHealthAddr: opt(r, "WORKER_HEALTH_ADDR", ":8081", parseAddr),
		LogLevel:         opt(r, "LOG_LEVEL", slog.LevelInfo, parseLevel),
		ShutdownTimeout:  opt(r, "SHUTDOWN_TIMEOUT", 15*time.Second, parseDuration),
		ReadinessTimeout: opt(r, "READINESS_TIMEOUT", 2*time.Second, parseDuration),
		HTTP: HTTP{
			ReadHeaderTimeout: opt(r, "HTTP_READ_HEADER_TIMEOUT", 5*time.Second, parseDuration),
			ReadTimeout:       opt(r, "HTTP_READ_TIMEOUT", 15*time.Second, parseDuration),
			WriteTimeout:      opt(r, "HTTP_WRITE_TIMEOUT", 30*time.Second, parseDuration),
			IdleTimeout:       opt(r, "HTTP_IDLE_TIMEOUT", 120*time.Second, parseDuration),
			MaxBodyBytes:      opt(r, "HTTP_MAX_BODY_BYTES", int64(1<<20), ParseSize),
		},
		DB: DB{
			URL:             r.requiredSecret("DATABASE_URL", urlWithScheme("postgres", "postgresql")),
			MaxConns:        opt(r, "DB_MAX_CONNS", int32(20), parsePositiveInt32),
			MinConns:        opt(r, "DB_MIN_CONNS", int32(2), parseNonNegativeInt32),
			MaxConnLifetime: opt(r, "DB_MAX_CONN_LIFETIME", time.Hour, parseDuration),
			MaxConnIdleTime: opt(r, "DB_MAX_CONN_IDLE_TIME", 15*time.Minute, parseDuration),
		},
		Redis: Redis{
			URL:          r.requiredSecret("REDIS_URL", urlWithScheme("redis", "rediss")),
			PoolSize:     opt(r, "REDIS_POOL_SIZE", 20, parsePositiveInt),
			DialTimeout:  opt(r, "REDIS_DIAL_TIMEOUT", 2*time.Second, parseDuration),
			ReadTimeout:  opt(r, "REDIS_READ_TIMEOUT", time.Second, parseDuration),
			WriteTimeout: opt(r, "REDIS_WRITE_TIMEOUT", time.Second, parseDuration),
		},
	}
	if cfg.DB.MinConns > cfg.DB.MaxConns {
		r.fail("DB_MIN_CONNS", errMinMax)
	}
	r.readAuth(&cfg)
	r.readPlatforms(&cfg)
	if len(r.errs) > 0 {
		return Config{}, errors.Join(r.errs...)
	}
	return cfg, nil
}
