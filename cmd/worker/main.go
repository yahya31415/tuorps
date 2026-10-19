// Command worker runs background work: scheduled publishing, webhook
// processing and sync jobs (River queues arrive in M3). It exposes its own
// health probes on WORKER_HEALTH_ADDR.
//
//	worker [serve]       run the worker (default)
//	worker healthcheck   probe /healthz on WORKER_HEALTH_ADDR (container HEALTHCHECK)
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yahya31415/tuorps/internal/app"
	healthhttp "github.com/yahya31415/tuorps/internal/health/http"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/lifecycle"
	"github.com/yahya31415/tuorps/internal/platform/logx"
)

var errUsage = errors.New("usage: worker [serve | healthcheck]")

const probeTimeout = 2 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.LookupEnv, os.ReadFile, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, lookup config.Lookup, readFile config.ReadFile, stdout io.Writer) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "serve":
		return serve(ctx, lookup, readFile, stdout)
	case "healthcheck":
		addr, ok := lookup("WORKER_HEALTH_ADDR")
		if !ok || addr == "" {
			addr = ":8081"
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
	log := logx.New(stdout, cfg.LogLevel).With("component", "worker")
	deps, err := app.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = deps.Close() }()

	rt := httpx.NewRouter()
	healthhttp.Register(rt, deps.Checks(), cfg.ReadinessTimeout, log)
	srv := httpx.NewServer(cfg.WorkerHealthAddr, httpx.Chain(rt, app.Middleware(cfg, log)...), cfg.HTTP, log)
	return lifecycle.Serve(ctx, srv, cfg.ShutdownTimeout, log)
}
