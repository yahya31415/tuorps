package auth_test

import (
	"crypto/rand"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/redis/go-redis/v9"

	"github.com/yahya31415/tuorps/internal/auth"
	"github.com/yahya31415/tuorps/internal/auth/core"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/redisx"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

// testClock is a settable clock.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func redisClient(t testing.TB) *redis.Client {
	t.Helper()
	rd := testinfra.StartRedis(t)
	c, err := redisx.NewClient(t.Context(), config.Redis{URL: config.NewSecret(rd.URL), PoolSize: 5, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestSessionStore(t *testing.T) {
	rdb := redisClient(t)
	clk := &testClock{now: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	policy := core.SessionPolicy{Idle: 12 * time.Hour, Absolute: 14 * 24 * time.Hour}
	store := auth.NewSessionStore(rdb, policy, clk.Now, rand.Reader)
	alice := uuid.MustParse("0199c3f0-0000-7000-8000-0000000000a1")
	bob := uuid.MustParse("0199c3f0-0000-7000-8000-0000000000b1")

	t.Run("create and look up", func(t *testing.T) {
		tok, err := store.Create(t.Context(), alice, true)
		if err != nil {
			t.Fatal(err)
		}
		s, ok, err := store.Get(t.Context(), tok)
		if err != nil || !ok || s.UserID != alice || !s.MFAVerified {
			t.Fatalf("Get = %+v %v %v", s, ok, err)
		}
	})

	t.Run("the token itself is never stored", func(t *testing.T) {
		tok, _ := store.Create(t.Context(), alice, false)
		keys, _ := rdb.Keys(t.Context(), "*").Result()
		for _, k := range keys {
			if strings.Contains(k, tok) {
				t.Errorf("key %q contains the token", k)
			}
			if !strings.HasPrefix(k, "tuorps:v1:") {
				t.Errorf("key %q is not namespaced", k)
			}
			if v, err := rdb.Get(t.Context(), k).Result(); err == nil && strings.Contains(v, tok) {
				t.Errorf("value of %q contains the token", k)
			}
		}
	})

	t.Run("garbage and unknown tokens are simply not found", func(t *testing.T) {
		for _, tok := range []string{"", "garbage", strings.Repeat("A", 43)} {
			if _, ok, err := store.Get(t.Context(), tok); ok || err != nil {
				t.Errorf("Get(%q) = %v, %v", tok, ok, err)
			}
		}
	})

	t.Run("redis expiry follows the idle timeout", func(t *testing.T) {
		tok, _ := store.Create(t.Context(), alice, false)
		key := auth.SessionKeyForTest(tok)
		ttl, _ := rdb.PTTL(t.Context(), key).Result()
		if ttl <= 11*time.Hour || ttl > 12*time.Hour {
			t.Errorf("TTL = %v, want about 12h", ttl)
		}
	})

	t.Run("activity refreshes last seen; idleness expires the session", func(t *testing.T) {
		tok, _ := store.Create(t.Context(), alice, false)
		clk.Advance(2 * time.Hour)
		s, ok, _ := store.Get(t.Context(), tok)
		if !ok || !s.LastSeen.Equal(clk.Now()) {
			t.Fatalf("after 2h: ok=%v lastSeen=%v", ok, s.LastSeen)
		}
		clk.Advance(12*time.Hour + time.Second)
		if _, ok, _ := store.Get(t.Context(), tok); ok {
			t.Fatal("session survived 12h of inactivity")
		}
		if n, _ := rdb.Exists(t.Context(), auth.SessionKeyForTest(tok)).Result(); n != 0 {
			t.Error("expired session not deleted")
		}
	})

	t.Run("absolute lifetime ends even an active session", func(t *testing.T) {
		tok, _ := store.Create(t.Context(), alice, false)
		for range 40 { // 40 × 11h59m ≈ 20 days, well past the 14-day limit
			clk.Advance(12*time.Hour - time.Minute)
			if _, ok, _ := store.Get(t.Context(), tok); !ok {
				return // expired at or before the absolute limit
			}
		}
		t.Fatal("session outlived its absolute lifetime")
	})

	t.Run("delete and delete-all-but-one", func(t *testing.T) {
		keep, _ := store.Create(t.Context(), alice, false)
		drop1, _ := store.Create(t.Context(), alice, false)
		drop2, _ := store.Create(t.Context(), alice, false)
		other, _ := store.Create(t.Context(), bob, false)
		if err := store.Delete(t.Context(), drop1); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := store.Get(t.Context(), drop1); ok {
			t.Error("deleted session still valid")
		}
		if err := store.DeleteAllForUser(t.Context(), alice, keep); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := store.Get(t.Context(), drop2); ok {
			t.Error("other session of the user survived")
		}
		if _, ok, _ := store.Get(t.Context(), keep); !ok {
			t.Error("kept session was deleted")
		}
		if _, ok, _ := store.Get(t.Context(), other); !ok {
			t.Error("another user's session was deleted")
		}
		if err := store.DeleteAllForUser(t.Context(), alice, ""); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := store.Get(t.Context(), keep); ok {
			t.Error("delete-all left a session")
		}
	})
}

// Session lookup runs on every authenticated request.
func BenchmarkSessionGet(b *testing.B) {
	rdb := redisClient(b)
	clk := &testClock{now: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	store := auth.NewSessionStore(rdb, core.SessionPolicy{Idle: 12 * time.Hour, Absolute: 14 * 24 * time.Hour}, clk.Now, rand.Reader)
	tok, err := store.Create(b.Context(), uuid.MustParse("0199c3f0-0000-7000-8000-0000000000a1"), false)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, ok, err := store.Get(b.Context(), tok); !ok || err != nil {
			b.Fatal(ok, err)
		}
	}
}
