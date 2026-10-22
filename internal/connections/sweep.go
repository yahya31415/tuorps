package connections

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/audit"
	auditcore "github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/connections/adapters/postgres/sqlcgen"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
)

const (
	sweepEvery    = 15 * time.Minute
	sweepBatch    = 500
	wipeEvery     = time.Hour
	unusedGraceOf = time.Hour // a grant nobody connected within this is wiped
)

// SweepArgs is the periodic sweep for due connections.
type SweepArgs struct{}

// Kind is the River job kind.
func (SweepArgs) Kind() string { return "connections.sweep" }

// Sweeper enqueues a maintenance pass for every due connection, across
// workspaces, through the connections_due definer function (ids only).
type Sweeper struct {
	river.WorkerDefaults[SweepArgs]
	WorkerDeps
}

// NewSweeper returns a Sweeper.
func NewSweeper(d WorkerDeps) *Sweeper { return &Sweeper{WorkerDeps: d} }

func (s *Sweeper) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
	var due []sqlcgen.ConnectionsDueRow
	if err := postgres.InReadTx(ctx, s.Pool, postgres.Scope{}, func(tx pgx.Tx) error {
		var err error
		due, err = q(tx).ConnectionsDue(ctx, sqlcgen.ConnectionsDueParams{Now: s.Now(), MaxRows: sweepBatch})
		return err
	}); err != nil {
		return err
	}
	// One pending pass per connection per sweep period, however often the
	// sweep runs.
	opts := &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: sweepEvery}}
	for _, d := range due {
		if _, err := s.Inserter.Insert(ctx, MaintainArgs{ConnectionID: d.ID, WorkspaceID: d.WorkspaceID}, opts); err != nil {
			return err
		}
	}
	return nil
}

// WipeUnusedArgs is the periodic wipe of grants nobody uses.
type WipeUnusedArgs struct{}

// Kind is the River job kind.
func (WipeUnusedArgs) Kind() string { return "connections.wipe_unused" }

// UnusedGrantWiper wipes the tokens of grants that no live connection has
// used for an hour: abandoned account selections and fully disconnected
// logins (ADR 0009). Each wipe is audited.
type UnusedGrantWiper struct {
	river.WorkerDefaults[WipeUnusedArgs]
	WorkerDeps
}

// NewUnusedGrantWiper returns an UnusedGrantWiper.
func NewUnusedGrantWiper(d WorkerDeps) *UnusedGrantWiper { return &UnusedGrantWiper{WorkerDeps: d} }

func (w *UnusedGrantWiper) Work(ctx context.Context, _ *river.Job[WipeUnusedArgs]) error {
	now := w.Now()
	var unused []sqlcgen.GrantsUnusedRow
	if err := postgres.InReadTx(ctx, w.Pool, postgres.Scope{}, func(tx pgx.Tx) error {
		var err error
		unused, err = q(tx).GrantsUnused(ctx, now.Add(-unusedGraceOf))
		return err
	}); err != nil {
		return err
	}
	for _, g := range unused {
		ev, _ := auditcore.NewEvent("connection.grant_wipe", "oauth_grant", g.ID.String(), map[string]string{"brand_id": g.BrandID.String()})
		if err := w.Writer.InWriteTx(ctx, postgres.Scope{WorkspaceID: g.WorkspaceID}, ev, auditcore.Meta{}, func(tx audit.Tx) error {
			return WipeGrantTokens(ctx, tx, g.BrandID, g.ID, now)
		}); err != nil {
			return err
		}
	}
	return nil
}

// PeriodicJobs are the connection domain's schedules.
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(sweepEvery), func() (river.JobArgs, *river.InsertOpts) { return SweepArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(wipeEvery), func() (river.JobArgs, *river.InsertOpts) { return WipeUnusedArgs{}, nil }, nil),
	}
}

// Register adds the connection domain's workers.
func Register(workers *river.Workers, d WorkerDeps) {
	river.AddWorker(workers, NewMaintainer(d))
	river.AddWorker(workers, NewSweeper(d))
	river.AddWorker(workers, NewUnusedGrantWiper(d))
}
