package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"slices"

	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/connections"
	connscore "github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/email"
	emailadapters "github.com/yahya31415/tuorps/internal/email/adapters"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/jobs"
	"github.com/yahya31415/tuorps/internal/webhooks"
)

// WorkerJobs wires every background job and the periodic schedules
// (email delivery, connection maintenance, webhook processing, the version
// check and the receipt prune). The outbox and the sweep enqueue through
// their own insert-only client.
func WorkerJobs(cfg config.Config, deps *Deps, smtp config.SMTP, log *slog.Logger) (*river.Workers, []*river.PeriodicJob, error) {
	keys, err := cfg.Encryption.Keyring()
	if err != nil {
		return nil, nil, fmt.Errorf("app: keyring: %w", err)
	}
	inserter, err := jobs.NewInserter(deps.Pool, log)
	if err != nil {
		return nil, nil, err
	}
	now := clock.System()
	outbox := email.NewOutbox(inserter, keys, rand.Reader)
	platforms := deps.Platforms
	if platforms == nil {
		platforms = Platforms(cfg)
	}
	providers := make(map[connscore.Provider]connections.Provider, len(platforms))
	for p, pl := range platforms {
		providers[p] = pl.Ports
	}
	wd := connections.WorkerDeps{
		Pool: deps.Pool, Writer: audit.NewWriter(deps.Pool, ids.System(), now), Vault: connections.NewVault(keys, rand.Reader),
		Keys: keys, Providers: providers, Mailer: outbox, Inserter: inserter, Origin: cfg.AppOrigin, Now: now, Log: log,
	}

	workers := river.NewWorkers()
	email.Register(workers, email.NewWorker(keys, emailadapters.NewSMTP(smtp, nil), smtp.From, ids.System(), now))
	connections.Register(workers, wd)
	river.AddWorker(workers, connections.NewVersionAlerter(connections.VersionDeps{
		Pool: deps.Pool, Mailer: outbox, OpsEmail: cfg.Platforms.OpsAlertEmail, Versions: connscore.PinnedVersions, Now: now, Log: log,
	}))
	river.AddWorker(workers, webhooks.NewProcessor(webhooks.ProcessorDeps{
		Keys: keys,
		Deauthorize: func(ctx context.Context, p connscore.Provider, user string) error {
			return connections.DeauthorizeUser(ctx, wd, p, user)
		},
		DeleteData: func(ctx context.Context, p connscore.Provider, user, code string) error {
			return connections.DeleteUserData(ctx, wd, p, user, code)
		},
	}))
	river.AddWorker(workers, webhooks.NewPruner(deps.Pool, now))
	periodic := slices.Concat(connections.PeriodicJobs(), connections.VersionPeriodicJobs(), webhooks.PeriodicJobs())
	return workers, periodic, nil
}
