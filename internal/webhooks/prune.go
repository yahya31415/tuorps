package webhooks

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/webhooks/adapters/postgres/sqlcgen"
)

// receiptsKept outlives every platform's retry window (Meta 36 h, LinkedIn 8 h).
const receiptsKept = 7 * 24 * time.Hour

// PruneArgs is the daily receipt prune.
type PruneArgs struct{}

// Kind is the River job kind.
func (PruneArgs) Kind() string { return "webhooks.prune" }

// Pruner deletes old webhook receipts through the definer function.
type Pruner struct {
	river.WorkerDefaults[PruneArgs]
	pool *pgxpool.Pool
	now  clock.Now
}

// NewPruner returns a Pruner.
func NewPruner(pool *pgxpool.Pool, now clock.Now) *Pruner { return &Pruner{pool: pool, now: now} }

func (p *Pruner) Work(ctx context.Context, _ *river.Job[PruneArgs]) error {
	return postgres.InTx(ctx, p.pool, postgres.Scope{}, func(tx pgx.Tx) error {
		_, err := sqlcgen.New(tx).PruneReceipts(ctx, p.now().Add(-receiptsKept))
		return err
	})
}

// PeriodicJobs are the webhook domain's schedules.
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour), func() (river.JobArgs, *river.InsertOpts) { return PruneArgs{}, nil }, nil),
	}
}
