package adapters_test

import (
	"context"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/health"
	"github.com/yahya31415/tuorps/internal/health/adapters"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/redisx"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

func probe(t *testing.T, c health.Check) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	return c(ctx)
}

func TestPostgresCheck(t *testing.T) {
	pg := testinfra.StartPostgres(t)
	pool, err := postgres.NewPool(t.Context(), config.DB{URL: config.NewSecret(pg.AppURL), MaxConns: 2, MinConns: 0, MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	check := adapters.PostgresCheck(pool)

	if err := probe(t, check); err != nil {
		t.Fatalf("healthy database reported %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := check(ctx); err == nil {
		t.Error("check ignored a cancelled context")
	}
	stop := 5 * time.Second
	if err := pg.Container.Stop(t.Context(), &stop); err != nil {
		t.Fatal(err)
	}
	if err := probe(t, check); err == nil {
		t.Error("stopped database reported healthy")
	}
}

func TestRedisCheck(t *testing.T) {
	r := testinfra.StartRedis(t)
	client, err := redisx.NewClient(t.Context(), config.Redis{URL: config.NewSecret(r.URL), PoolSize: 2, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	check := adapters.RedisCheck(client)

	if err := probe(t, check); err != nil {
		t.Fatalf("healthy redis reported %v", err)
	}
	stop := 5 * time.Second
	if err := r.Container.Stop(t.Context(), &stop); err != nil {
		t.Fatal(err)
	}
	if err := probe(t, check); err == nil {
		t.Error("stopped redis reported healthy")
	}
}
