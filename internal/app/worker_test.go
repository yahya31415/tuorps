package app_test

import (
	"context"
	"log/slog"
	"net/mail"
	"slices"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/app"
	"github.com/yahya31415/tuorps/internal/connections"
	"github.com/yahya31415/tuorps/internal/email"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/webhooks"
)

// dupe is a stand-in worker used to probe registrations: registering a
// kind twice fails, so a failure proves the kind is wired.
type dupe[T river.JobArgs] struct{ river.WorkerDefaults[T] }

func (dupe[T]) Work(context.Context, *river.Job[T]) error { return nil }

func registered[T river.JobArgs](w *river.Workers) bool {
	return river.AddWorkerSafely(w, &dupe[T]{}) != nil
}

// The worker runs every job kind M3 enqueues, and schedules the periodic
// ones (sweep, unused-grant wipe, version check, receipt prune).
func TestWorkerJobs(t *testing.T) {
	smtp := config.SMTP{Host: "mailpit", Port: 1025, TLS: config.SMTPNoTLS, From: mail.Address{Address: "no-reply@tuorps.test"}, Timeout: time.Second}
	workers, periodic, err := app.WorkerJobs(testConfig(t), lazyDeps(t), smtp, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	for kind, ok := range map[string]bool{
		email.SendArgs{}.Kind():             registered[email.SendArgs](workers),
		connections.MaintainArgs{}.Kind():   registered[connections.MaintainArgs](workers),
		connections.SweepArgs{}.Kind():      registered[connections.SweepArgs](workers),
		connections.WipeUnusedArgs{}.Kind(): registered[connections.WipeUnusedArgs](workers),
		connections.VersionsArgs{}.Kind():   registered[connections.VersionsArgs](workers),
		webhooks.ProcessArgs{}.Kind():       registered[webhooks.ProcessArgs](workers),
		webhooks.PruneArgs{}.Kind():         registered[webhooks.PruneArgs](workers),
	} {
		if !ok {
			t.Errorf("no worker for %s", kind)
		}
	}
	if len(periodic) != 4 || slices.Contains(periodic, nil) {
		t.Errorf("periodic jobs = %d, want 4", len(periodic))
	}
}
