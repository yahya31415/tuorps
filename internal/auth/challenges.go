package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"
	"uuid"

	"github.com/redis/go-redis/v9"

	"github.com/yahya31415/tuorps/internal/auth/core"
)

// MaxChallengeAttempts is how many codes may be tried per password login.
const MaxChallengeAttempts = 5

const challengePrefix = "tuorps:v1:mfa:"

var (
	ErrChallengeNotFound  = errors.New("auth: no such MFA challenge")
	ErrChallengeExhausted = errors.New("auth: too many MFA attempts")
)

// attemptScript counts an attempt on an existing challenge only (never
// recreating an expired or consumed one) and returns {user, attempts}.
const attemptScript = `
if redis.call('EXISTS', KEYS[1]) == 0 then return false end
local n = redis.call('HINCRBY', KEYS[1], 'attempts', 1)
return {redis.call('HGET', KEYS[1], 'user'), n}
`

// ChallengeStore holds the short-lived second step of a 2FA login.
type ChallengeStore struct {
	rdb    *redis.Client
	ttl    time.Duration
	rand   io.Reader
	script *redis.Script
}

// NewChallengeStore returns a ChallengeStore whose challenges live for ttl.
func NewChallengeStore(rdb *redis.Client, ttl time.Duration, rand io.Reader) ChallengeStore {
	return ChallengeStore{rdb: rdb, ttl: ttl, rand: rand, script: redis.NewScript(attemptScript)}
}

func challengeKey(token string) (string, bool) {
	h, ok := core.HashToken(token)
	if !ok {
		return "", false
	}
	return challengePrefix + hex.EncodeToString(h[:]), true
}

// Create issues a challenge for user and returns its token.
func (c ChallengeStore) Create(ctx context.Context, user uuid.UUID) (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(c.rand, raw); err != nil {
		return "", fmt.Errorf("auth: challenge token: %w", err)
	}
	token, err := core.EncodeToken(raw)
	if err != nil {
		return "", err
	}
	key, _ := challengeKey(token)
	if _, err := c.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, key, "user", user.String(), "attempts", 0)
		p.Expire(ctx, key, c.ttl)
		return nil
	}); err != nil {
		return "", fmt.Errorf("auth: store challenge: %w", err)
	}
	return token, nil
}

// Attempt records one code attempt and returns the challenge's user.
func (c ChallengeStore) Attempt(ctx context.Context, token string) (uuid.UUID, error) {
	key, ok := challengeKey(token)
	if !ok {
		return uuid.Nil(), ErrChallengeNotFound
	}
	res, err := c.script.Run(ctx, c.rdb, []string{key}).Slice()
	if errors.Is(err, redis.Nil) {
		return uuid.Nil(), ErrChallengeNotFound
	}
	if err != nil || len(res) != 2 {
		return uuid.Nil(), fmt.Errorf("auth: challenge attempt: %w", err)
	}
	if n, _ := res[1].(int64); n > MaxChallengeAttempts {
		_ = c.rdb.Del(ctx, key).Err()
		return uuid.Nil(), ErrChallengeExhausted
	}
	s, _ := res[0].(string)
	u, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil(), ErrChallengeNotFound
	}
	return u, nil
}

// Consume deletes a challenge after a successful second factor.
func (c ChallengeStore) Consume(ctx context.Context, token string) error {
	key, ok := challengeKey(token)
	if !ok {
		return nil
	}
	return c.rdb.Del(ctx, key).Err()
}
