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

	"github.com/pressly/goose/v3"

	"github.com/yahya31415/tuorps/internal/app"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/jobs"
	"github.com/yahya31415/tuorps/internal/platform/lifecycle"
	"github.com/yahya31415/tuorps/internal/platform/logx"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
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
	log := logx.New(stdout, cfg.LogLevel).With("component", "api")
	deps, err := app.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = deps.Close() }()

	built, err := app.BuildAPI(cfg, deps, log)
	if err != nil {
		return err
	}
	return lifecycle.Serve(ctx, httpx.NewServer(cfg.HTTPAddr, built.Handler, cfg.HTTP, log), cfg.ShutdownTimeout, log)
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
		if _, err = p.Up(ctx); err == nil {
			err = migrateJobs(ctx, cfg, log)
		}
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

// migrateJobs applies River's schema (the transactional job outbox) as the
// schema owner, after the application migrations.
func migrateJobs(ctx context.Context, cfg config.Migrate, log *slog.Logger) error {
	pool, err := postgres.NewPool(ctx, config.DB{URL: cfg.DatabaseURL, MaxConns: 2, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute})
	if err != nil {
		return err
	}
	defer pool.Close()
	return jobs.Migrate(ctx, pool, log)
}
