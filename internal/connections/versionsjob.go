package connections

import (
	"context"
	"log/slog"
	"net/mail"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/connections/core"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
)

const sunsetWindow = 60 * 24 * time.Hour

// SystemMailer queues operator email outside any workspace.
type SystemMailer interface {
	EnqueueSystem(ctx context.Context, tx pgx.Tx, m emailcore.Message) error
}

// VersionDeps are the version manager's collaborators.
type VersionDeps struct {
	Pool     *pgxpool.Pool
	Mailer   SystemMailer
	OpsEmail string                      // empty: log only
	Versions func() []core.PinnedVersion // core.PinnedVersions outside tests
	Now      clock.Now
	Log      *slog.Logger
}

// VersionsArgs is the daily pinned-version check.
type VersionsArgs struct{}

// Kind is the River job kind.
func (VersionsArgs) Kind() string { return "platform.versions" }

// VersionAlerter is the API version manager's alarm: every pinned platform
// API version within 60 days of its sunset is logged at ERROR and, when an
// operator address is configured, emailed.
type VersionAlerter struct {
	river.WorkerDefaults[VersionsArgs]
	VersionDeps
}

// NewVersionAlerter returns a VersionAlerter.
func NewVersionAlerter(d VersionDeps) *VersionAlerter { return &VersionAlerter{VersionDeps: d} }

func (a *VersionAlerter) Work(ctx context.Context, _ *river.Job[VersionsArgs]) error {
	alerts := core.SunsetAlerts(a.Versions(), a.Now(), sunsetWindow)
	for _, al := range alerts {
		a.Log.ErrorContext(ctx, "pinned platform API version sunsets soon", "api", al.API, "version", al.Version,
			"sunset", al.Sunset.Format(time.DateOnly), "days_left", al.DaysLeft)
	}
	if a.OpsEmail == "" || len(alerts) == 0 {
		return nil
	}
	return postgres.InTx(ctx, a.Pool, postgres.Scope{}, func(tx pgx.Tx) error {
		for _, al := range alerts {
			msg, err := emailcore.NewMessage(mail.Address{Address: a.OpsEmail}, emailcore.KindVersionSunset, "en", map[string]string{
				"api": al.API, "version": al.Version, "sunset": al.Sunset.Format(time.DateOnly), "days": strconv.Itoa(al.DaysLeft),
			})
			if err != nil {
				return err
			}
			if err := a.Mailer.EnqueueSystem(ctx, tx, msg); err != nil {
				return err
			}
		}
		return nil
	})
}

// VersionPeriodicJobs schedules the daily check, at most once a day.
func VersionPeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour), func() (river.JobArgs, *river.InsertOpts) {
			return VersionsArgs{}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: 24 * time.Hour}}
		}, &river.PeriodicJobOpts{RunOnStart: true}),
	}
}
