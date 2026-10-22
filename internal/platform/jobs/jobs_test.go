package jobs_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/jobs"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

type pingArgs struct {
	Note string `json:"note"`
}

func (pingArgs) Kind() string { return "test.ping" }

type pingWorker struct {
	river.WorkerDefaults[pingArgs]
	seen chan string
}

func (w *pingWorker) Work(_ context.Context, j *river.Job[pingArgs]) error {
	w.seen <- j.Args.Note
	return nil
}

func pool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	p, err := postgres.NewPool(t.Context(), config.DB{URL: config.NewSecret(url), MaxConns: 5, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestJobs(t *testing.T) {
	pg := testinfra.MigratedPostgres(t)
	migrator := pool(t, pg.MigratorURL)
	log := slog.New(slog.DiscardHandler)

	app := pool(t, pg.AppURL)

	t.Run("the runtime role cannot create River's schema", func(t *testing.T) {
		if err := jobs.Migrate(t.Context(), app, log); err == nil {
			t.Fatal("tuorps_app migrated River's schema")
		}
	})

	t.Run("migrations apply as the schema owner and are idempotent", func(t *testing.T) {
		for range 2 {
			if err := jobs.Migrate(t.Context(), migrator, log); err != nil {
				t.Fatalf("Migrate: %v", err)
			}
		}
	})

	seen := make(chan string, 8)
	workers := river.NewWorkers()
	river.AddWorker(workers, &pingWorker{seen: seen})
	runner, err := jobs.NewWorker(app, workers, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		_ = runner.Stop(sctx)
	})

	inserter, err := jobs.NewInserter(app, log)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a job inserted in a committed transaction runs", func(t *testing.T) {
		err := postgres.InTx(t.Context(), app, postgres.Scope{}, func(tx pgx.Tx) error {
			_, err := inserter.InsertTx(t.Context(), tx, pingArgs{Note: "committed"}, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-seen:
			if got != "committed" {
				t.Fatalf("worker saw %q", got)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("job never ran")
		}
	})

	t.Run("a job inserted in a rolled-back transaction never runs", func(t *testing.T) {
		boom := errors.New("rollback")
		_ = postgres.InTx(t.Context(), app, postgres.Scope{}, func(tx pgx.Tx) error {
			if _, err := inserter.InsertTx(t.Context(), tx, pingArgs{Note: "rolled-back"}, nil); err != nil {
				return err
			}
			return boom
		})
		select {
		case got := <-seen:
			t.Fatalf("worker ran %q from a rolled-back transaction", got)
		case <-time.After(3 * time.Second):
		}
	})

	t.Run("completed jobs are retained briefly (data minimisation)", func(t *testing.T) {
		if got := jobs.CompletedRetention; got > 24*time.Hour {
			t.Errorf("completed job retention = %v, want <= 24h", got)
		}
	})
}

// Maintenance runs on schedules: a worker can be given periodic jobs.
func TestWorkerRunsPeriodicJobs(t *testing.T) {
	pg := testinfra.MigratedPostgres(t)
	log := slog.New(slog.DiscardHandler)
	if err := jobs.Migrate(t.Context(), pool(t, pg.MigratorURL), log); err != nil {
		t.Fatal(err)
	}
	app := pool(t, pg.AppURL)
	seen := make(chan string, 8)
	workers := river.NewWorkers()
	river.AddWorker(workers, &pingWorker{seen: seen})
	every := river.NewPeriodicJob(river.PeriodicInterval(time.Hour), func() (river.JobArgs, *river.InsertOpts) {
		return pingArgs{Note: "periodic"}, nil
	}, &river.PeriodicJobOpts{RunOnStart: true})
	runner, err := jobs.NewWorker(app, workers, log, every)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runner.Stop(ctx)
	})
	select {
	case got := <-seen:
		if got != "periodic" {
			t.Fatalf("worker saw %q", got)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("periodic job never ran")
	}
}
