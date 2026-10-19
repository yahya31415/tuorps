// Command api is the Tuorps HTTP API.
//
//	api [serve]                       run the HTTP server (default)
//	api migrate [up|down|status]      apply schema migrations as tuorps_migrator
//	api healthcheck                   probe /healthz on HTTP_ADDR (container HEALTHCHECK)
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/redis/go-redis/v9"

	"github.com/yahya31415/tuorps/internal/health"
	"github.com/yahya31415/tuorps/internal/health/adapters"
	healthhttp "github.com/yahya31415/tuorps/internal/health/http"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/lifecycle"
	"github.com/yahya31415/tuorps/internal/platform/logx"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/redisx"
	"github.com/yahya31415/tuorps/migrations"
)

var errUsage = errors.New("usage: api [serve | migrate [up|down|status] | healthcheck]")

const probeTimeout = 2 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.LookupEnv, os.ReadFile, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, lookup config.Lookup, readFile config.ReadFile, stdout io.Writer) error {
	cmd, rest := "serve", args
	if len(args) > 0 {
		cmd, rest = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve(ctx, lookup, readFile, stdout)
	case "migrate":
		return migrate(ctx, rest, lookup, readFile, stdout)
	case "healthcheck":
		addr, ok := lookup("HTTP_ADDR")
		if !ok || addr == "" {
			addr = ":8080"
		}
		return lifecycle.Probe(ctx, addr, "/healthz", probeTimeout)
	}
	return errUsage
}

func serve(ctx context.Context, lookup config.Lookup, readFile config.ReadFile, stdout io.Writer) error {
	cfg, err := config.Load(lookup, readFile)
	if err != nil {
		return err
	}
	log := logx.New(stdout, cfg.LogLevel)

	pool, err := postgres.NewPool(ctx, cfg.DB)
	if err != nil {
		return err
	}
	defer pool.Close()
	rdb, err := redisx.NewClient(ctx, cfg.Redis)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	rt := newRouter(checks(pool, rdb), cfg.ReadinessTimeout, log)
	h := httpx.Chain(rt,
		httpx.RequestID(ids.System()),
		httpx.AccessLog(log, clock.System()),
		httpx.Recover(log),
		httpx.SecurityHeaders(cfg.Env == config.EnvProd),
		httpx.BodyLimit(cfg.HTTP.MaxBodyBytes),
	)
	return lifecycle.Serve(ctx, httpx.NewServer(cfg.HTTPAddr, h, cfg.HTTP, log), cfg.ShutdownTimeout, log)
}

func newRouter(checks []health.NamedCheck, readinessTimeout time.Duration, log *slog.Logger) *httpx.Router {
	rt := httpx.NewRouter()
	healthhttp.Register(rt, checks, readinessTimeout, log)
	return rt
}

func checks(pool *pgxpool.Pool, rdb *redis.Client) []health.NamedCheck {
	return []health.NamedCheck{
		{Name: "postgres", Critical: true, Run: adapters.PostgresCheck(pool)},
		{Name: "redis", Critical: true, Run: adapters.RedisCheck(rdb)},
	}
}

func migrate(ctx context.Context, args []string, lookup config.Lookup, readFile config.ReadFile, stdout io.Writer) error {
	action := "up"
	if len(args) > 0 {
		action = args[0]
	}
	if action != "up" && action != "down" && action != "status" {
		return errUsage
	}
	cfg, err := config.LoadMigrate(lookup, readFile)
	if err != nil {
		return err
	}
	log := logx.New(stdout, cfg.LogLevel)
	db, err := migrations.Open(cfg.DatabaseURL.Reveal())
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	p, err := migrations.NewProvider(db, log)
	if err != nil {
		return err
	}

	switch action {
	case "up":
		_, err = p.Up(ctx)
	case "down":
		_, err = p.Down(ctx)
	case "status":
		var statuses []*goose.MigrationStatus
		if statuses, err = p.Status(ctx); err == nil {
			for _, s := range statuses {
				fmt.Fprintf(stdout, "%05d  %-8s  %s\n", s.Source.Version, s.State, filepath.Base(s.Source.Path))
			}
		}
	}
	if err != nil {
		return fmt.Errorf("migrate %s: %w", action, err)
	}
	return nil
}
