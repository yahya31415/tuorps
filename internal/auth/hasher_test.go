package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/auth/core"
)

func cheapParams() core.Argon2Params { return core.Argon2Params{Memory: 64, Time: 1, Threads: 1} }

func TestHasherHashAndVerify(t *testing.T) {
	h := NewHasher(cheapParams(), 2, rand.Reader)
	a, err := h.Hash(t.Context(), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := h.Hash(t.Context(), "correct horse battery staple")
	if a == b || !strings.HasPrefix(a, "$argon2id$") {
		t.Fatalf("hashes %q / %q: want distinct random salts", a, b)
	}
	ok, rehash, err := h.Verify(t.Context(), "correct horse battery staple", a)
	if err != nil || !ok || rehash {
		t.Fatalf("Verify = %v %v %v", ok, rehash, err)
	}
	if ok, _, _ := h.Verify(t.Context(), "wrong", a); ok {
		t.Fatal("wrong password verified")
	}
}

func TestHasherDummyVerifyNeverSucceeds(t *testing.T) {
	h := NewHasher(cheapParams(), 1, rand.Reader)
	if h.DummyVerify(t.Context(), "anything") {
		t.Fatal("dummy verification succeeded")
	}
}

func TestHasherBoundsConcurrency(t *testing.T) {
	h := NewHasher(cheapParams(), 2, rand.Reader)
	var active, peak atomic.Int32
	h.verify = func(pw, phc string, p core.Argon2Params) (bool, bool, error) {
		n := active.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		active.Add(-1)
		return true, false, nil
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _, _, _ = h.Verify(context.Background(), "pw", "phc") })
	}
	wg.Wait()
	if p := peak.Load(); p > 2 {
		t.Fatalf("%d hashes ran at once, limit 2", p)
	}
}

func TestHasherHonoursCancellationWhileWaiting(t *testing.T) {
	h := NewHasher(cheapParams(), 1, rand.Reader)
	release := make(chan struct{})
	h.verify = func(string, string, core.Argon2Params) (bool, bool, error) { <-release; return true, false, nil }
	go func() { _, _, _ = h.Verify(context.Background(), "pw", "phc") }()
	time.Sleep(20 * time.Millisecond) // let it take the only slot

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, _, err := h.Verify(ctx, "pw", "phc")
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded while waiting for a slot", err)
	}
}

func TestHasherFailsWithoutRandomness(t *testing.T) {
	h := NewHasher(cheapParams(), 1, strings.NewReader(""))
	if _, err := h.Hash(t.Context(), "pw"); err == nil {
		t.Fatal("hashed without a salt")
	}
}
