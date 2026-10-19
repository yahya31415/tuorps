// Package testinfra starts real dependencies (Postgres 18, Redis 8) in
// containers for adapter and integration tests. Import it only from
// _test.go files. Images are pinned by digest to match compose.yaml.
package testinfra

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/url"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/yahya31415/tuorps/migrations"
)

const (
	PostgresImage = "postgres:18.6-alpine@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873"
	RedisImage    = "redis:8.8.3-alpine@sha256:0b2b77d3ea5078274795e3177cdbdada8b96316684a38911d528534ed679b5ec"

	database    = "tuorps"
	startupWait = 2 * time.Minute
)

// Postgres is a running database initialised with deploy/postgres/initdb,
// so it has the same roles and schema as production.
type Postgres struct {
	Container   *tcpostgres.PostgresContainer
	AdminURL    string // superuser; for test setup only
	MigratorURL string // tuorps_migrator: owns the schema
	AppURL      string // tuorps_app: runtime role subject to RLS
}

// StartPostgres starts a fresh database and terminates it when tb ends.
func StartPostgres(tb testing.TB) *Postgres {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), startupWait)
	defer cancel()

	appPW, migratorPW := rand.Text(), rand.Text()
	c, err := tcpostgres.Run(ctx, PostgresImage,
		tcpostgres.WithDatabase(database),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword(rand.Text()),
		tcpostgres.WithInitScripts(filepath.Join(repoRoot(), "deploy/postgres/initdb/10-roles.sh")),
		testcontainers.WithEnv(map[string]string{
			"TUORPS_APP_PASSWORD":      appPW,
			"TUORPS_MIGRATOR_PASSWORD": migratorPW,
		}),
		tcpostgres.BasicWaitStrategies(),
	)
	tb.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	if err != nil {
		tb.Fatalf("testinfra: start postgres: %v", err)
	}
	admin, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		tb.Fatalf("testinfra: postgres connection string: %v", err)
	}
	return &Postgres{
		Container:   c,
		AdminURL:    admin,
		MigratorURL: withUser(tb, admin, "tuorps_migrator", migratorPW),
		AppURL:      withUser(tb, admin, "tuorps_app", appPW),
	}
}

// MigratedPostgres is StartPostgres with every migration applied.
func MigratedPostgres(tb testing.TB) *Postgres {
	tb.Helper()
	pg := StartPostgres(tb)
	db, err := migrations.Open(pg.MigratorURL)
	if err != nil {
		tb.Fatalf("testinfra: open migrator: %v", err)
	}
	defer func() { _ = db.Close() }()
	p, err := migrations.NewProvider(db, slog.New(slog.DiscardHandler))
	if err != nil {
		tb.Fatalf("testinfra: migrations: %v", err)
	}
	if _, err := p.Up(context.Background()); err != nil {
		tb.Fatalf("testinfra: migrate up: %v", err)
	}
	return pg
}

// Redis is a running Redis server.
type Redis struct {
	Container *tcredis.RedisContainer
	URL       string
}

// StartRedis starts a fresh Redis and terminates it when tb ends.
func StartRedis(tb testing.TB) *Redis {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), startupWait)
	defer cancel()
	c, err := tcredis.Run(ctx, RedisImage)
	tb.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	if err != nil {
		tb.Fatalf("testinfra: start redis: %v", err)
	}
	u, err := c.ConnectionString(ctx)
	if err != nil {
		tb.Fatalf("testinfra: redis connection string: %v", err)
	}
	return &Redis{Container: c, URL: u}
}

func withUser(tb testing.TB, raw, user, password string) string {
	tb.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		tb.Fatalf("testinfra: parse url: %v", err)
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

// repoRoot locates the repository from this source file's path.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}
