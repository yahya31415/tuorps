package connections_test

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/yahya31415/tuorps/internal/connections"
	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/redisx"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

func newRedis(t *testing.T) *redis.Client {
	t.Helper()
	rd := testinfra.StartRedis(t)
	c, err := redisx.NewClient(t.Context(), config.Redis{URL: config.NewSecret(rd.URL), PoolSize: 25, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// The OAuth state is single-use, short-lived, and opaque at rest (ADR 0008).
func TestOAuthStateStore(t *testing.T) {
	rdb := newRedis(t)
	store := connections.NewStateStore(rdb, testKeyring(t, "k1"), rand.Reader, 10*time.Minute)
	want := connections.StatePayload{
		UserID: ownerA(), SessionHash: sha256.Sum256([]byte("session-token")), WorkspaceID: wsA(),
		BrandID: brA1(), Provider: core.ProviderLinkedIn, ReconnectID: id("000000005a01"),
	}

	state, err := store.Create(t.Context(), want)
	if err != nil {
		t.Fatal(err)
	}
	if len(state) < 43 {
		t.Fatalf("state %q is shorter than 32 random bytes", state)
	}

	t.Run("stored hashed, sealed and expiring", func(t *testing.T) {
		keys, err := rdb.Keys(t.Context(), "tuorps:v1:oauth:*").Result()
		if err != nil || len(keys) != 1 {
			t.Fatalf("keys = %v, %v", keys, err)
		}
		if strings.Contains(keys[0], state) {
			t.Error("the key contains the state itself")
		}
		val, _ := rdb.Get(t.Context(), keys[0]).Result()
		if strings.Contains(val, ownerA().String()) || strings.Contains(val, "linkedin") {
			t.Errorf("the value is readable: %q", val)
		}
		if ttl := rdb.TTL(t.Context(), keys[0]).Val(); ttl <= 0 || ttl > 10*time.Minute {
			t.Errorf("TTL = %v", ttl)
		}
	})

	t.Run("consumed exactly once, with the payload intact", func(t *testing.T) {
		got, err := store.Consume(t.Context(), state)
		if err != nil || got != want {
			t.Fatalf("Consume = %+v, %v", got, err)
		}
		if _, err := store.Consume(t.Context(), state); !errors.Is(err, connections.ErrStateNotFound) {
			t.Errorf("second Consume: %v, want ErrStateNotFound", err)
		}
	})

	t.Run("concurrent callbacks: one winner", func(t *testing.T) {
		s, err := store.Create(t.Context(), want)
		if err != nil {
			t.Fatal(err)
		}
		var wins atomic.Int32
		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() {
				if _, err := store.Consume(t.Context(), s); err == nil {
					wins.Add(1)
				}
			})
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Errorf("%d consumers succeeded, want 1", wins.Load())
		}
	})

	t.Run("unknown or malformed state", func(t *testing.T) {
		for _, s := range []string{"", "nope", strings.Repeat("A", 43), "../../etc"} {
			if _, err := store.Consume(t.Context(), s); !errors.Is(err, connections.ErrStateNotFound) {
				t.Errorf("Consume(%q) = %v", s, err)
			}
		}
	})

	t.Run("a state created before a key rotation still opens", func(t *testing.T) {
		s, err := store.Create(t.Context(), want)
		if err != nil {
			t.Fatal(err)
		}
		other := connections.NewStateStore(rdb, testKeyring(t, "k0"), rand.Reader, time.Minute)
		if _, err := other.Consume(t.Context(), s); err != nil {
			t.Errorf("rotation: a state created before a key change must still open: %v", err)
		}
	})
}
