// Package auth is the imperative shell for authentication: password
// hashing with bounded concurrency, Redis-backed sessions and MFA
// challenges, the users repository and the HTTP handlers.
package auth

import (
	"context"
	"fmt"
	"io"

	"github.com/yahya31415/tuorps/internal/auth/core"
)

const saltBytes = 16

// Hasher hashes and verifies passwords with argon2id. Each hash uses about
// Memory KiB, so at most maxConcurrent run at once; callers wait for a slot
// (or give up when their context ends).
type Hasher struct {
	params core.Argon2Params
	slots  chan struct{}
	rand   io.Reader
	verify func(pw, phc string, p core.Argon2Params) (ok, needsRehash bool, err error)
	dummy  string
}

// NewHasher returns a Hasher. rand supplies salts.
func NewHasher(params core.Argon2Params, maxConcurrent int, rand io.Reader) *Hasher {
	// A fixed-salt hash of a random-looking constant, verified against
	// unknown accounts so their logins cost the same as real ones.
	dummy, _ := core.HashPassword("tuorps-dummy-password-for-timing", make([]byte, saltBytes), params)
	return &Hasher{params: params, slots: make(chan struct{}, maxConcurrent), rand: rand, verify: core.VerifyPassword, dummy: dummy}
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.slots }

// Hash returns a PHC string for pw with a fresh random salt.
func (h *Hasher) Hash(ctx context.Context, pw string) (string, error) {
	salt := make([]byte, saltBytes)
	if _, err := io.ReadFull(h.rand, salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	return core.HashPassword(pw, salt, h.params)
}

// Verify checks pw against phc; needsRehash means the hash should be
// replaced with one using the current parameters.
func (h *Hasher) Verify(ctx context.Context, pw, phc string) (ok, needsRehash bool, err error) {
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	return h.verify(pw, phc, h.params)
}

// DummyVerify spends the same work as Verify and always fails; use it for
// unknown accounts so response time does not reveal whether one exists.
func (h *Hasher) DummyVerify(ctx context.Context, pw string) bool {
	_, _, _ = h.Verify(ctx, pw+"\x00", h.dummy)
	return false
}
