package webhooks_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
	"github.com/yahya31415/tuorps/internal/webhooks"
)

// Receipts only need to outlive the platforms' retry windows (Meta 36 h,
// LinkedIn 8 h); a week is kept.
func TestPrunerDropsReceiptsOlderThanAWeek(t *testing.T) {
	pg := testinfra.MigratedPostgres(t)
	mk := func(url string) *pgxpool.Pool {
		p, err := postgres.NewPool(t.Context(), config.DB{URL: config.NewSecret(url), MaxConns: 2, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	app, admin := mk(pg.AppURL), mk(pg.AdminURL)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(t.Context(), `INSERT INTO tuorps.webhook_receipts (provider, dedupe_key, received_at) VALUES
		('meta', 'old', $1::timestamptz - interval '8 days'), ('meta', 'recent', $1::timestamptz - interval '6 days')`, now); err != nil {
		t.Fatal(err)
	}
	if err := webhooks.NewPruner(app, clock.Fixed(now)).Work(t.Context(), &river.Job[webhooks.PruneArgs]{}); err != nil {
		t.Fatal(err)
	}
	var keys []string
	rows, _ := admin.Query(t.Context(), `SELECT dedupe_key FROM tuorps.webhook_receipts ORDER BY dedupe_key`)
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		keys = append(keys, k)
	}
	rows.Close()
	if len(keys) != 1 || keys[0] != "recent" {
		t.Errorf("receipts left = %v", keys)
	}
	if len(webhooks.PeriodicJobs()) != 1 {
		t.Error("the prune is not scheduled")
	}
}
