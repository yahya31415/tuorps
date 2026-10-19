// Package testinfra starts real dependencies (Postgres 18, Redis 8) in
// containers for adapter and integration tests. Import it only from
// _test.go files. Images are pinned by digest to match compose.yaml.
package testinfra

import (
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"net"
	"net/url"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/yahya31415/tuorps/migrations"
)

const (
	PostgresImage = "postgres:18.6-alpine@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873"
	RedisImage    = "redis:8.8.3-alpine@sha256:0b2b77d3ea5078274795e3177cdbdada8b96316684a38911d528534ed679b5ec"
	MailpitImage  = "axllent/mailpit:v1.31.2@sha256:74d609a42ec279aa63c6b4622a6fa9b5408d1ad5b1d76a1c4be40a265ce0863d"

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

// MailpitOptions configures the SMTP side of Mailpit.
type MailpitOptions struct {
	CertPEM, KeyPEM []byte // enables TLS when set
	RequireSTARTTLS bool   // reject clients that do not upgrade
	ImplicitTLS     bool   // TLS from the first byte (port-465 style)
}

// Mailpit is a running SMTP capture server with an HTTP API.
type Mailpit struct {
	SMTPHost string
	SMTPPort int
	APIURL   string
}

// StartMailpit starts Mailpit (accepting any SMTP credentials) and
// terminates it when tb ends.
func StartMailpit(tb testing.TB, opts MailpitOptions) *Mailpit {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), startupWait)
	defer cancel()
	env := map[string]string{"MP_SMTP_AUTH_ACCEPT_ANY": "true"}
	if opts.CertPEM == nil {
		env["MP_SMTP_AUTH_ALLOW_INSECURE"] = "true" // Mailpit refuses accept-any auth without TLS otherwise
	}
	var files []testcontainers.ContainerFile
	if opts.CertPEM != nil {
		files = []testcontainers.ContainerFile{
			{Reader: bytes.NewReader(opts.CertPEM), ContainerFilePath: "/certs/cert.pem", FileMode: 0o644},
			{Reader: bytes.NewReader(opts.KeyPEM), ContainerFilePath: "/certs/key.pem", FileMode: 0o644},
		}
		env["MP_SMTP_TLS_CERT"], env["MP_SMTP_TLS_KEY"] = "/certs/cert.pem", "/certs/key.pem"
	}
	if opts.RequireSTARTTLS {
		env["MP_SMTP_REQUIRE_STARTTLS"] = "true"
	}
	if opts.ImplicitTLS {
		env["MP_SMTP_REQUIRE_TLS"] = "true"
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        MailpitImage,
			ExposedPorts: []string{"1025/tcp", "8025/tcp"},
			Env:          env,
			Files:        files,
			WaitingFor:   wait.ForHTTP("/livez").WithPort("8025/tcp"),
		},
		Started: true,
	})
	tb.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	if err != nil {
		tb.Fatalf("testinfra: start mailpit: %v", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		tb.Fatalf("testinfra: mailpit host: %v", err)
	}
	smtpPort, err := c.MappedPort(ctx, "1025/tcp")
	if err != nil {
		tb.Fatalf("testinfra: mailpit smtp port: %v", err)
	}
	apiPort, err := c.MappedPort(ctx, "8025/tcp")
	if err != nil {
		tb.Fatalf("testinfra: mailpit api port: %v", err)
	}
	return &Mailpit{SMTPHost: host, SMTPPort: int(smtpPort.Num()), APIURL: "http://" + net.JoinHostPort(host, apiPort.Port())}
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
