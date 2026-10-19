package redisx_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/redisx"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

func redisConfig(rawURL string) config.Redis {
	return config.Redis{
		URL:          config.NewSecret(rawURL),
		PoolSize:     9,
		DialTimeout:  700 * time.Millisecond,
		ReadTimeout:  300 * time.Millisecond,
		WriteTimeout: 400 * time.Millisecond,
	}
}

func TestNewClientAgainstRedis(t *testing.T) {
	r := testinfra.StartRedis(t)
	c, err := redisx.NewClient(t.Context(), redisConfig(r.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = c.Close() }()

	o := c.Options()
	if o.PoolSize != 9 || o.DialTimeout != 700*time.Millisecond || o.ReadTimeout != 300*time.Millisecond || o.WriteTimeout != 400*time.Millisecond {
		t.Errorf("options = pool %d dial %v read %v write %v", o.PoolSize, o.DialTimeout, o.ReadTimeout, o.WriteTimeout)
	}
	if !o.ContextTimeoutEnabled {
		t.Error("context deadlines are not honoured")
	}
	if err := c.Set(t.Context(), "k", "v", time.Minute).Err(); err != nil {
		t.Fatalf("SET: %v", err)
	}
	if got, _ := c.Get(t.Context(), "k").Result(); got != "v" {
		t.Errorf("GET = %q", got)
	}
}

func TestNewClientRejectsBadURLWithoutEchoing(t *testing.T) {
	_, err := redisx.NewClient(t.Context(), redisConfig("redis://:secret-zz9@host:notaport/0"))
	if err == nil || strings.Contains(err.Error(), "secret-zz9") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewClientFailsFastWhenUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := redisx.NewClient(ctx, redisConfig("redis://:pw@192.0.2.1:6379/0"))
	if err == nil {
		t.Fatal("want error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
}
