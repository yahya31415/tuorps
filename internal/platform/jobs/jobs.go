// Package jobs wires River, the Postgres-backed job queue. Jobs inserted
// with InsertTx share the caller's transaction, so River's job table is a
// transactional outbox: a job exists only if the write that caused it
// commits.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

const (
	// CompletedRetention is how long finished jobs (and their arguments)
	// are kept before River prunes them.
	CompletedRetention = 24 * time.Hour
	jobTimeout         = time.Minute
	maxAttempts        = 10
	defaultWorkers     = 20
)

// Client is a River client over pgx.
type Client = river.Client[pgx.Tx]

// Migrate applies River's schema migrations. It must run as the schema owner.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	m, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Logger: log})
	if err != nil {
		return fmt.Errorf("jobs: migrator: %w", err)
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("jobs: migrate: %w", err)
	}
	return nil
}

// NewInserter returns an insert-only client (the api process).
func NewInserter(pool *pgxpool.Pool, log *slog.Logger) (*Client, error) {
	c, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: log})
	if err != nil {
		return nil, fmt.Errorf("jobs: inserter: %w", err)
	}
	return c, nil
}

// NewWorker returns a client that works the default queue with workers and
// schedules the periodic jobs (the worker process). Call Start and, on
// shutdown, Stop.
func NewWorker(pool *pgxpool.Pool, workers *river.Workers, log *slog.Logger, periodic ...*river.PeriodicJob) (*Client, error) {
	c, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:                      log,
		Workers:                     workers,
		Queues:                      map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: defaultWorkers}},
		CompletedJobRetentionPeriod: CompletedRetention,
		JobTimeout:                  jobTimeout,
		MaxAttempts:                 maxAttempts,
		PeriodicJobs:                periodic,
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: worker: %w", err)
	}
	return c, nil
}
