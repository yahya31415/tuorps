package auth_test

import (
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/yahya31415/tuorps/internal/auth"
)

func TestChallengeStore(t *testing.T) {
	rdb := redisClient(t)
	store := auth.NewChallengeStore(rdb, time.Minute, rand.Reader)
	alice := uuid.MustParse("0199c3f0-0000-7000-8000-0000000000a1")

	t.Run("each attempt returns the user until the limit", func(t *testing.T) {
		tok, err := store.Create(t.Context(), alice)
		if err != nil {
			t.Fatal(err)
		}
		for i := range auth.MaxChallengeAttempts {
			u, err := store.Attempt(t.Context(), tok)
			if err != nil || u != alice {
				t.Fatalf("attempt %d: %v %v", i+1, u, err)
			}
		}
		if _, err := store.Attempt(t.Context(), tok); !errors.Is(err, auth.ErrChallengeExhausted) {
			t.Fatalf("attempt beyond the limit: err = %v", err)
		}
		if _, err := store.Attempt(t.Context(), tok); !errors.Is(err, auth.ErrChallengeNotFound) {
			t.Fatalf("exhausted challenge still exists: err = %v", err)
		}
	})

	t.Run("consumed challenges cannot be reused or resurrected", func(t *testing.T) {
		tok, _ := store.Create(t.Context(), alice)
		if err := store.Consume(t.Context(), tok); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Attempt(t.Context(), tok); !errors.Is(err, auth.ErrChallengeNotFound) {
			t.Fatalf("err = %v", err)
		}
		keys, _ := rdb.Keys(t.Context(), "tuorps:v1:mfa:*").Result()
		for _, k := range keys {
			if strings.Contains(k, tok) {
				t.Errorf("key %q contains the token", k)
			}
		}
	})

	t.Run("challenges expire", func(t *testing.T) {
		short := auth.NewChallengeStore(rdb, time.Second, rand.Reader)
		tok, _ := short.Create(t.Context(), alice)
		time.Sleep(1100 * time.Millisecond)
		if _, err := short.Attempt(t.Context(), tok); !errors.Is(err, auth.ErrChallengeNotFound) {
			t.Fatalf("err = %v, want expired", err)
		}
	})

	t.Run("garbage tokens", func(t *testing.T) {
		for _, tok := range []string{"", "nope", strings.Repeat("A", 43)} {
			if _, err := store.Attempt(t.Context(), tok); !errors.Is(err, auth.ErrChallengeNotFound) {
				t.Errorf("Attempt(%q) err = %v", tok, err)
			}
		}
	})
}
