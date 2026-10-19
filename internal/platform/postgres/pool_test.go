package postgres_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

func dbConfig(rawURL string) config.DB {
	return config.DB{
		URL:             config.NewSecret(rawURL),
		MaxConns:        7,
		MinConns:        1,
		MaxConnLifetime: 10 * time.Minute,
		MaxConnIdleTime: 2 * time.Minute,
	}
}

// TestNewPoolAgainstPostgres shares one container across its subtests.
func TestNewPoolAgainstPostgres(t *testing.T) {
	pg := testinfra.StartPostgres(t)
	t.Run("connects as runtime role with limits", func(t *testing.T) { connectsAsRuntimeRole(t, pg) })
	t.Run("fails fast without leaking credentials", func(t *testing.T) { failsWithoutLeaking(t, pg) })
}

func connectsAsRuntimeRole(t *testing.T, pg *testinfra.Postgres) {
	ctx := t.Context()
	pool, err := postgres.NewPool(ctx, dbConfig(pg.AppURL))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	c := pool.Config()
	if c.MaxConns != 7 || c.MinConns != 1 || c.MaxConnLifetime != 10*time.Minute || c.MaxConnIdleTime != 2*time.Minute {
		t.Errorf("pool config = max %d min %d life %v idle %v", c.MaxConns, c.MinConns, c.MaxConnLifetime, c.MaxConnIdleTime)
	}

	var user, searchPath, stmtTimeout, idleTxTimeout, appName string
	var superuser, bypassRLS bool
	err = pool.QueryRow(ctx, `SELECT current_user, current_setting('search_path'), current_setting('statement_timeout'),
		current_setting('idle_in_transaction_session_timeout'), current_setting('application_name'), rolsuper, rolbypassrls
		FROM pg_roles WHERE rolname = current_user`).Scan(&user, &searchPath, &stmtTimeout, &idleTxTimeout, &appName, &superuser, &bypassRLS)
	if err != nil {
		t.Fatal(err)
	}
	if user != "tuorps_app" || superuser || bypassRLS {
		t.Errorf("connected as %s (superuser=%v bypassrls=%v); RLS would not apply", user, superuser, bypassRLS)
	}
	if !strings.Contains(searchPath, "tuorps") {
		t.Errorf("search_path = %q", searchPath)
	}
	if stmtTimeout != "15s" || idleTxTimeout != "30s" || appName != "tuorps" {
		t.Errorf("session settings: statement_timeout=%s idle_tx=%s application_name=%s", stmtTimeout, idleTxTimeout, appName)
	}
}

func failsWithoutLeaking(t *testing.T, pg *testinfra.Postgres) {
	u, err := url.Parse(pg.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("tuorps_app", "wrong-pw-zz9")
	_, err = postgres.NewPool(t.Context(), dbConfig(u.String()))
	if err == nil {
		t.Fatal("NewPool with a wrong password succeeded")
	}
	if strings.Contains(err.Error(), "wrong-pw-zz9") {
		t.Errorf("error leaks the password: %v", err)
	}
}

func TestNewPoolHonoursContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	// 192.0.2.0/24 is TEST-NET-1: never routable, so the dial hangs until the deadline.
	_, err := postgres.NewPool(ctx, dbConfig("postgres://tuorps_app:pw@192.0.2.1:5432/tuorps?connect_timeout=30"))
	if err == nil {
		t.Fatal("want error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v despite a 500ms deadline", d)
	}
}

func TestNewPoolRejectsUnparseableURL(t *testing.T) {
	_, err := postgres.NewPool(t.Context(), dbConfig("postgres://u:secret-zz9@[::1"))
	if err == nil || strings.Contains(err.Error(), "secret-zz9") {
		t.Fatalf("err = %v", err)
	}
}
