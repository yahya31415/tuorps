package connections_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/connections"
	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/email"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/jobs"
)

// The version manager: a pinned version within 60 days of its sunset is an
// ERROR log line and, when OPS_ALERT_EMAIL is set, an email to operators.
func TestVersionAlerter(t *testing.T) {
	mf := newMaintFixture(t)
	inserter, err := jobs.NewInserter(mf.pool, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	pinned := func() []core.PinnedVersion {
		return []core.PinnedVersion{
			{API: "linkedin", Version: "202610", Sunset: time.Date(2027, 10, 15, 0, 0, 0, 0, time.UTC)},
			{API: "meta-graph", Version: "v26.0", Sunset: time.Date(2028, 7, 29, 0, 0, 0, 0, time.UTC)},
		}
	}
	at := time.Date(2027, 8, 20, 9, 0, 0, 0, time.UTC) // 55 whole days (and 15 hours) before LinkedIn's sunset
	run := func(ops string) string {
		t.Helper()
		var logs bytes.Buffer
		a := connections.NewVersionAlerter(connections.VersionDeps{
			Pool: mf.pool, Mailer: email.NewOutbox(inserter, mf.keys, rand.Reader), OpsEmail: ops,
			Versions: pinned, Now: clock.Fixed(at), Log: slog.New(slog.NewJSONHandler(&logs, nil)),
		})
		if err := a.Work(context.Background(), &river.Job[connections.VersionsArgs]{}); err != nil {
			t.Fatal(err)
		}
		return logs.String()
	}

	logs := run("")
	if !strings.Contains(logs, `"level":"ERROR"`) || !strings.Contains(logs, "202610") || strings.Contains(logs, "v26.0") {
		t.Errorf("logs = %s", logs)
	}
	if n := len(mf.emails(t, emailcore.KindVersionSunset)); n != 0 {
		t.Errorf("emailed without OPS_ALERT_EMAIL: %d", n)
	}
	run("ops@tuorps.in")
	got := mf.emails(t, emailcore.KindVersionSunset)
	if len(got) != 1 || got[0].To().Address != "ops@tuorps.in" || got[0].Data()["days"] != "55" || got[0].Data()["version"] != "202610" {
		t.Errorf("alert emails = %+v", got)
	}
	if len(connections.VersionPeriodicJobs()) != 1 {
		t.Error("the version check is not scheduled")
	}
}
