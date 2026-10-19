package ratelimit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/ratelimit"
	"github.com/yahya31415/tuorps/internal/platform/ratelimit/core"
	"github.com/yahya31415/tuorps/internal/platform/redisx"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

// memStore is an in-memory ratelimit.Store that records the keys it saw.
type memStore struct {
	mu     sync.Mutex
	counts map[string]int64
	keys   []string
	err    error
}

func (m *memStore) store() ratelimit.Store {
	return func(_ context.Context, key string, window time.Duration, now time.Time) (core.Counts, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.err != nil {
			return core.Counts{}, m.err
		}
		if m.counts == nil {
			m.counts = map[string]int64{}
		}
		start := core.WindowStart(now, window)
		cur := key + "@" + start.String()
		prev := key + "@" + start.Add(-window).String()
		m.keys = append(m.keys, key)
		before := m.counts[cur]
		m.counts[cur]++
		return core.Counts{Prev: m.counts[prev], Curr: before}, nil
	}
}

func fixedNow() time.Time { return time.Date(2026, 10, 19, 12, 0, 30, 0, time.UTC) }

func rule(t *testing.T, class string, max int, failClosed bool) ratelimit.Rule {
	t.Helper()
	l, err := core.NewLimit(max, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return ratelimit.Rule{Class: class, Limit: l, FailClosed: failClosed}
}

func TestAllowEnforcesPerSubjectAndClass(t *testing.T) {
	m := &memStore{}
	lim := ratelimit.NewLimiter(m.store(), fixedNow, slog.New(slog.DiscardHandler))
	login := rule(t, "login_ip", 3, true)
	for i := range 3 {
		if d, err := lim.Allow(t.Context(), login, "203.0.113.7"); err != nil || !d.Allowed {
			t.Fatalf("request %d: %+v %v", i+1, d, err)
		}
	}
	d, err := lim.Allow(t.Context(), login, "203.0.113.7")
	if err != nil || d.Allowed || d.RetryAfter <= 0 {
		t.Fatalf("4th request: %+v %v, want denied with RetryAfter", d, err)
	}
	if d, _ := lim.Allow(t.Context(), login, "198.51.100.1"); !d.Allowed {
		t.Error("another subject was limited")
	}
	if d, _ := lim.Allow(t.Context(), rule(t, "signup_ip", 3, true), "203.0.113.7"); !d.Allowed {
		t.Error("another class was limited")
	}
}

func TestKeysNeverContainTheRawSubject(t *testing.T) {
	m := &memStore{}
	lim := ratelimit.NewLimiter(m.store(), fixedNow, slog.New(slog.DiscardHandler))
	_, _ = lim.Allow(t.Context(), rule(t, "login_email", 5, true), "priya@example.com")
	_, _ = lim.Allow(t.Context(), rule(t, "login_ip", 5, true), "203.0.113.7")
	for _, k := range m.keys {
		if strings.Contains(k, "priya") || strings.Contains(k, "203.0.113") {
			t.Errorf("key %q exposes the subject", k)
		}
		if !strings.HasPrefix(k, "tuorps:v1:rl:") {
			t.Errorf("key %q is not namespaced and versioned", k)
		}
	}
}

func TestStoreFailure(t *testing.T) {
	var logs bytes.Buffer
	m := &memStore{err: errors.New("redis: connection refused")}
	lim := ratelimit.NewLimiter(m.store(), fixedNow, slog.New(slog.NewJSONHandler(&logs, nil)))

	if _, err := lim.Allow(t.Context(), rule(t, "login_ip", 5, true), "x"); !errors.Is(err, ratelimit.ErrUnavailable) {
		t.Errorf("fail-closed rule: err = %v, want ErrUnavailable", err)
	}
	d, err := lim.Allow(t.Context(), rule(t, "write_user", 5, false), "x")
	if err != nil || !d.Allowed {
		t.Errorf("fail-open rule: %+v %v, want allowed", d, err)
	}
	if !strings.Contains(logs.String(), "rate limiter unavailable") {
		t.Errorf("fail-open did not log: %s", logs.String())
	}
}

func TestMiddleware(t *testing.T) {
	var served atomic.Int32
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { served.Add(1); w.WriteHeader(http.StatusNoContent) })
	byHeader := func(r *http.Request) string { return r.Header.Get("X-Test-Subject") }

	t.Run("rejects with 429 and Retry-After over the limit", func(t *testing.T) {
		m := &memStore{}
		h := ratelimit.NewLimiter(m.store(), fixedNow, slog.New(slog.DiscardHandler)).Middleware(rule(t, "login_ip", 2, true), byHeader)(next)
		codes := []int{}
		var last *httptest.ResponseRecorder
		for range 3 {
			req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
			req.Header.Set("X-Test-Subject", "a")
			last = httptest.NewRecorder()
			h.ServeHTTP(last, req)
			codes = append(codes, last.Code)
		}
		if codes[0] != 204 || codes[1] != 204 || codes[2] != 429 {
			t.Fatalf("codes = %v", codes)
		}
		if ra := last.Header().Get("Retry-After"); ra == "" || ra == "0" {
			t.Errorf("Retry-After = %q", ra)
		}
		var body struct{ Error struct{ Code string } }
		if err := json.Unmarshal(last.Body.Bytes(), &body); err != nil || body.Error.Code != "rate_limited" {
			t.Errorf("body = %s", last.Body)
		}
	})
	t.Run("503 when a fail-closed limiter is unavailable", func(t *testing.T) {
		m := &memStore{err: errors.New("down")}
		h := ratelimit.NewLimiter(m.store(), fixedNow, slog.New(slog.DiscardHandler)).Middleware(rule(t, "login_ip", 2, true), byHeader)(next)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "rate_limiter_unavailable") {
			t.Errorf("got %d %s", rec.Code, rec.Body)
		}
	})
}

func TestRetryAfterSecondsRoundsUp(t *testing.T) {
	for d, want := range map[time.Duration]string{time.Millisecond: "1", time.Second: "1", 1001 * time.Millisecond: "2", 51 * time.Second: "51"} {
		if got := ratelimit.RetryAfterSeconds(d); got != want {
			t.Errorf("RetryAfterSeconds(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestRedisStore(t *testing.T) {
	rd := testinfra.StartRedis(t)
	client, err := redisx.NewClient(t.Context(), config.Redis{URL: config.NewSecret(rd.URL), PoolSize: 20, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	store := ratelimit.RedisStore(client)
	now := time.Date(2026, 10, 19, 12, 0, 10, 0, time.UTC)

	t.Run("counts before the request, then rolls into the previous window", func(t *testing.T) {
		for want := range int64(3) {
			c, err := store(t.Context(), "tuorps:v1:rl:t:a", time.Minute, now)
			if err != nil || c.Curr != want || c.Prev != 0 {
				t.Fatalf("call %d: %+v %v", want+1, c, err)
			}
		}
		c, err := store(t.Context(), "tuorps:v1:rl:t:a", time.Minute, now.Add(time.Minute))
		if err != nil || c.Prev != 3 || c.Curr != 0 {
			t.Fatalf("next window: %+v %v", c, err)
		}
	})
	t.Run("keys expire after two windows", func(t *testing.T) {
		if _, err := store(t.Context(), "tuorps:v1:rl:t:ttl", time.Minute, now); err != nil {
			t.Fatal(err)
		}
		keys, _ := client.Keys(t.Context(), "tuorps:v1:rl:t:ttl:*").Result()
		if len(keys) != 1 {
			t.Fatalf("keys = %v", keys)
		}
		ttl, _ := client.PTTL(t.Context(), keys[0]).Result()
		if ttl <= time.Minute || ttl > 2*time.Minute {
			t.Errorf("TTL = %v, want (1m, 2m]", ttl)
		}
	})
	t.Run("concurrent requests: exactly the limit passes", func(t *testing.T) {
		lim := ratelimit.NewLimiter(store, func() time.Time { return now }, slog.New(slog.DiscardHandler))
		r := rule(t, "burst", 25, true)
		var allowed atomic.Int32
		var wg sync.WaitGroup
		for range 100 {
			wg.Go(func() {
				if d, err := lim.Allow(context.Background(), r, "same-subject"); err == nil && d.Allowed {
					allowed.Add(1)
				}
			})
		}
		wg.Wait()
		if got := allowed.Load(); got != 25 {
			t.Fatalf("allowed %d of 100, want exactly 25", got)
		}
	})
}

func BenchmarkLimiterAllow(b *testing.B) {
	m := &memStore{}
	l, _ := core.NewLimit(1<<30, time.Minute)
	lim := ratelimit.NewLimiter(m.store(), fixedNow, slog.New(slog.DiscardHandler))
	r := ratelimit.Rule{Class: "write_user", Limit: l}
	b.ReportAllocs()
	for b.Loop() {
		_, _ = lim.Allow(context.Background(), r, "0199c3f0-7a2b-7c3d-8e4f-5a6b7c8d9e0f")
	}
}
