package connections_test

import (
	"testing"

	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/connections"
	"github.com/yahya31415/tuorps/internal/connections/core"
)

func TestSweepEnqueuesDueConnectionsOnce(t *testing.T) {
	mf := newMaintFixture(t)
	_, due := mf.seed(t, core.ProviderMeta, core.KindFacebookPage, mf.keys, "EAAuser", "", "EAApage", "")
	_, later := mf.seed(t, core.ProviderMeta, core.KindFacebookPage, mf.keys, "EAAuser", "", "EAApage2", "")
	if _, err := mf.admin.Exec(t.Context(), `UPDATE tuorps.connections SET next_due_at = CASE WHEN id = $1 THEN $3::timestamptz - interval '1 minute' ELSE $3::timestamptz + interval '1 hour' END WHERE id IN ($1, $2)`, due, later, mf.now); err != nil {
		t.Fatal(err)
	}
	s := connections.NewSweeper(mf.deps)
	for range 2 {
		if err := s.Work(t.Context(), &river.Job[connections.SweepArgs]{}); err != nil {
			t.Fatal(err)
		}
	}
	var n, other int
	_ = mf.admin.QueryRow(t.Context(), `SELECT count(*) FROM tuorps.river_job WHERE kind = 'connections.maintain' AND args->>'connection_id' = $1 AND args->>'workspace_id' = $2`, due.String(), wsA().String()).Scan(&n)
	_ = mf.admin.QueryRow(t.Context(), `SELECT count(*) FROM tuorps.river_job WHERE kind = 'connections.maintain' AND args->>'connection_id' = $1`, later.String()).Scan(&other)
	if n != 1 || other != 0 {
		t.Errorf("maintain jobs: due %d (want 1 despite two sweeps), not due %d", n, other)
	}
}

func TestWipeUnusedGrants(t *testing.T) {
	mf := newMaintFixture(t)
	used, _ := mf.seed(t, core.ProviderLinkedIn, core.KindLinkedInOrg, mf.keys, "AQused", "", "", "")
	unused, c := mf.seed(t, core.ProviderLinkedIn, core.KindLinkedInOrg, mf.keys, "AQunused", "", "", "")
	if _, err := mf.admin.Exec(t.Context(), `UPDATE tuorps.connections SET status = 'disconnected' WHERE id = $1`, c); err != nil {
		t.Fatal(err)
	}
	if _, err := mf.admin.Exec(t.Context(), `UPDATE tuorps.oauth_grants SET created_at = $1::timestamptz - interval '2 hours'`, mf.now); err != nil {
		t.Fatal(err)
	}
	if err := connections.NewUnusedGrantWiper(mf.deps).Work(t.Context(), &river.Job[connections.WipeUnusedArgs]{}); err != nil {
		t.Fatal(err)
	}
	var wiped, kept int
	_ = mf.admin.QueryRow(t.Context(), `SELECT count(*) FROM tuorps.oauth_grants WHERE id = $1 AND access_token_enc IS NULL`, unused).Scan(&wiped)
	_ = mf.admin.QueryRow(t.Context(), `SELECT count(*) FROM tuorps.oauth_grants WHERE id = $1 AND access_token_enc IS NOT NULL`, used).Scan(&kept)
	if wiped != 1 || kept != 1 || mf.audits(t, "connection.grant_wipe") != 1 {
		t.Errorf("wiped %d, kept %d, audit rows %d", wiped, kept, mf.audits(t, "connection.grant_wipe"))
	}
}

// The schedules exist and cover every periodic job kind.
func TestPeriodicJobs(t *testing.T) {
	if got := len(connections.PeriodicJobs()); got != 2 {
		t.Errorf("periodic jobs = %d, want the sweep and the unused-grant wipe", got)
	}
}
