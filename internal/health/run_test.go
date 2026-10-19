package health_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/health"
)

func ok(context.Context) error { return nil }

func TestRunChecksReportsEachOutcomeInOrder(t *testing.T) {
	boom := errors.New("boom")
	got := health.RunChecks(context.Background(), time.Second, []health.NamedCheck{
		{Name: "postgres", Critical: true, Run: ok},
		{Name: "redis", Critical: true, Run: func(context.Context) error { return boom }},
		{Name: "storage", Run: ok},
	})
	if len(got) != 3 {
		t.Fatalf("got %d results", len(got))
	}
	if got[0].Name != "postgres" || got[0].Err != nil || !got[0].Critical {
		t.Errorf("postgres: %+v", got[0])
	}
	if got[1].Name != "redis" || !errors.Is(got[1].Err, boom) {
		t.Errorf("redis: %+v", got[1])
	}
	if got[2].Name != "storage" || got[2].Critical {
		t.Errorf("storage: %+v", got[2])
	}
}

func TestRunChecksRunsConcurrently(t *testing.T) {
	sleep := func(ctx context.Context) error {
		select {
		case <-time.After(150 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	start := time.Now()
	health.RunChecks(context.Background(), time.Second, []health.NamedCheck{{Name: "a", Run: sleep}, {Name: "b", Run: sleep}, {Name: "c", Run: sleep}})
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Errorf("three 150ms checks took %v; not concurrent", d)
	}
}

func TestRunChecksBoundsEachCheckByTimeout(t *testing.T) {
	hang := func(context.Context) error { time.Sleep(2 * time.Second); return nil } // ignores ctx
	start := time.Now()
	got := health.RunChecks(context.Background(), 50*time.Millisecond, []health.NamedCheck{{Name: "stuck", Critical: true, Run: hang}})
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("waited %v for a check that ignores its context", d)
	}
	if !errors.Is(got[0].Err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", got[0].Err)
	}
}

func TestRunChecksHonoursParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := health.RunChecks(ctx, time.Second, []health.NamedCheck{{Name: "x", Run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}})
	if !errors.Is(got[0].Err, context.Canceled) {
		t.Errorf("err = %v, want canceled", got[0].Err)
	}
}
